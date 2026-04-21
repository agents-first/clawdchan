// Package pairing binds two nodes together using a 128-bit shared secret
// encoded as a 12-word BIP39 mnemonic. Both sides rendezvous at the relay's
// /pair endpoint, exchange identity cards under AEAD keyed from the secret,
// and derive a Short Authentication String for optional out-of-band
// verification.
package pairing

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/gorilla/websocket"
	"github.com/tyler-smith/go-bip39"
	bip39wl "github.com/tyler-smith/go-bip39/wordlists"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"github.com/agents-first/ClawdChan/core/identity"
)

const (
	// CodeEntropyBytes is the number of secret bytes behind a pairing code.
	// 16 bytes = 128 bits = 12 BIP39 words.
	CodeEntropyBytes = 16

	pairKeyLabel  = "clawdchan-pair-v1"
	pairSasLabel  = "clawdchan-pair-sas-v1"
	pairMaxFrame  = 1 << 14
	defaultDialTO = 10 * time.Second
)

// Code is the raw 128-bit pairing secret. It is converted to and from a human
// mnemonic via Mnemonic and ParseCode.
type Code [CodeEntropyBytes]byte

// GenerateCode returns a fresh random pairing code.
func GenerateCode() (Code, error) {
	var c Code
	if _, err := rand.Read(c[:]); err != nil {
		return Code{}, err
	}
	return c, nil
}

// Mnemonic returns the 12-word BIP39 representation of the code.
func (c Code) Mnemonic() string {
	m, err := bip39.NewMnemonic(c[:])
	if err != nil {
		panic(fmt.Errorf("pairing: bip39 encode: %w", err))
	}
	return m
}

// ParseCode decodes a 12-word BIP39 mnemonic back into a Code.
func ParseCode(mnemonic string) (Code, error) {
	mnemonic = strings.Join(strings.Fields(strings.ToLower(mnemonic)), " ")
	if !bip39.IsMnemonicValid(mnemonic) {
		return Code{}, errors.New("pairing: invalid mnemonic")
	}
	ent, err := bip39.EntropyFromMnemonic(mnemonic)
	if err != nil {
		return Code{}, err
	}
	if len(ent) != CodeEntropyBytes {
		return Code{}, fmt.Errorf("pairing: unexpected mnemonic length %d", len(ent))
	}
	var c Code
	copy(c[:], ent)
	return c, nil
}

// Trust is the per-peer trust level. See docs/design.md § Trust levels.
type Trust uint8

const (
	TrustPaired  Trust = 1
	TrustBridged Trust = 2
	TrustRevoked Trust = 3
)

// Peer is the record persisted after successful pairing.
type Peer struct {
	NodeID         identity.NodeID
	KexPub         identity.KexKey
	Alias          string
	HumanReachable bool
	Trust          Trust
	PairedAtMs     int64
	SAS            [4]string
}

// Card is the identity info two nodes exchange during pairing.
type Card struct {
	NodeID         identity.NodeID `cbor:"1,keyasint"`
	KexPub         identity.KexKey `cbor:"2,keyasint"`
	Alias          string          `cbor:"3,keyasint"`
	HumanReachable bool            `cbor:"4,keyasint"`
}

// MyCard builds the local Card to send to the peer.
func MyCard(self *identity.Identity, alias string, reachable bool) Card {
	return Card{
		NodeID:         self.SigningPublic,
		KexPub:         self.KexPublic,
		Alias:          alias,
		HumanReachable: reachable,
	}
}

// Rendezvous performs the paired exchange. Both sides derive the same AEAD
// key from code and meet at the relay's /pair endpoint under
// SHA-256(code) as the rendezvous hash. The initiator sends first.
func Rendezvous(ctx context.Context, relayURL string, code Code, myCard Card, isInitiator bool) (Peer, error) {
	aead, err := aeadForCode(code)
	if err != nil {
		return Peer{}, err
	}

	u, err := resolvePairURL(relayURL, code)
	if err != nil {
		return Peer{}, err
	}

	dialer := &websocket.Dialer{HandshakeTimeout: defaultDialTO}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return Peer{}, fmt.Errorf("pair dial: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(pairMaxFrame)

	myBlob, err := cbor.Marshal(myCard)
	if err != nil {
		return Peer{}, fmt.Errorf("encode card: %w", err)
	}
	sealed, err := sealAEAD(aead, myBlob)
	if err != nil {
		return Peer{}, err
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Minute)
	}
	conn.SetWriteDeadline(deadline)
	conn.SetReadDeadline(deadline)

	var peerBlob []byte
	if isInitiator {
		if err := conn.WriteMessage(websocket.BinaryMessage, sealed); err != nil {
			return Peer{}, fmt.Errorf("send card: %w", err)
		}
		_, in, err := conn.ReadMessage()
		if err != nil {
			return Peer{}, fmt.Errorf("recv card: %w", err)
		}
		peerBlob, err = openAEAD(aead, in)
		if err != nil {
			return Peer{}, fmt.Errorf("decrypt peer card: %w", err)
		}
	} else {
		_, in, err := conn.ReadMessage()
		if err != nil {
			return Peer{}, fmt.Errorf("recv card: %w", err)
		}
		peerBlob, err = openAEAD(aead, in)
		if err != nil {
			return Peer{}, fmt.Errorf("decrypt peer card: %w", err)
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, sealed); err != nil {
			return Peer{}, fmt.Errorf("send card: %w", err)
		}
	}

	var peerCard Card
	if err := cbor.Unmarshal(peerBlob, &peerCard); err != nil {
		return Peer{}, fmt.Errorf("parse peer card: %w", err)
	}

	sas := computeSAS(code, myCard.KexPub, peerCard.KexPub)

	return Peer{
		NodeID:         peerCard.NodeID,
		KexPub:         peerCard.KexPub,
		Alias:          peerCard.Alias,
		HumanReachable: peerCard.HumanReachable,
		Trust:          TrustPaired,
		PairedAtMs:     time.Now().UnixMilli(),
		SAS:            sas,
	}, nil
}

func resolvePairURL(relayURL string, code Code) (*url.URL, error) {
	u, err := url.Parse(relayURL)
	if err != nil {
		return nil, fmt.Errorf("parse relay url: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	u.Path = "/pair"
	codeHash := sha256.Sum256(code[:])
	q := u.Query()
	q.Set("code_hash", hex.EncodeToString(codeHash[:]))
	u.RawQuery = q.Encode()
	return u, nil
}

func aeadForCode(code Code) (cipher.AEAD, error) {
	key := make([]byte, chacha20poly1305.KeySize)
	h := hkdf.New(sha256.New, code[:], nil, []byte(pairKeyLabel))
	if _, err := io.ReadFull(h, key); err != nil {
		return nil, err
	}
	return chacha20poly1305.NewX(key)
}

func sealAEAD(aead cipher.AEAD, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(nonce)+len(plaintext)+aead.Overhead())
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, nil), nil
}

func openAEAD(aead cipher.AEAD, frame []byte) ([]byte, error) {
	ns := aead.NonceSize()
	if len(frame) < ns+aead.Overhead() {
		return nil, errors.New("pairing: ciphertext too short")
	}
	return aead.Open(nil, frame[:ns], frame[ns:], nil)
}

func computeSAS(code Code, myKex, peerKex identity.KexKey) [4]string {
	lo, hi := myKex[:], peerKex[:]
	if bytes.Compare(lo, hi) > 0 {
		lo, hi = hi, lo
	}
	transcript := append(append([]byte{}, lo...), hi...)
	h := hkdf.New(sha256.New, code[:], transcript, []byte(pairSasLabel))
	var buf [8]byte
	io.ReadFull(h, buf[:])
	bits := binary.BigEndian.Uint64(buf[:])
	var sas [4]string
	wl := bip39wl.English
	for i := 0; i < 4; i++ {
		idx := (bits >> (11 * uint(i))) & 0x7FF
		sas[i] = wl[idx]
	}
	return sas
}
