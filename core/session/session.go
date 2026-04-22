// Package session derives a per-peer symmetric key from the two long-term
// X25519 keypairs exchanged during pairing, and wraps messages with
// XChaCha20-Poly1305 using random nonces.
//
// There is no handshake: once two nodes have paired, they can Seal/Open
// messages to each other at any time, which makes queued offline delivery
// trivial. The trade-off is no forward secrecy — if a long-term kex private
// key leaks, all past messages to/from that node become decryptable. A future
// version can layer Noise_IK on top without changing the public API.
package session

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"github.com/agents-first/clawdchan/core/identity"
)

// Label is the HKDF info string for session key derivation. Changing it is a
// wire-format break.
const Label = "clawdchan-session-v1"

// Session holds the AEAD primitive for one peer pair.
type Session struct {
	aead interface {
		NonceSize() int
		Overhead() int
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
	}
}

// New derives a Session for talking to peer. Both sides reach the same key
// because the HKDF salt is the pair of public kex keys sorted lexicographically.
func New(self *identity.Identity, peerKex identity.KexKey) (*Session, error) {
	selfPriv, err := ecdh.X25519().NewPrivateKey(self.KexPrivate[:])
	if err != nil {
		return nil, fmt.Errorf("session: self kex priv: %w", err)
	}
	peerPub, err := ecdh.X25519().NewPublicKey(peerKex[:])
	if err != nil {
		return nil, fmt.Errorf("session: peer kex pub: %w", err)
	}
	shared, err := selfPriv.ECDH(peerPub)
	if err != nil {
		return nil, fmt.Errorf("session: ecdh: %w", err)
	}
	lo, hi := sortedKeys(self.KexPublic[:], peerKex[:])
	salt := append(append([]byte{}, lo...), hi...)

	h := hkdf.New(sha256.New, shared, salt, []byte(Label))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(h, key); err != nil {
		return nil, fmt.Errorf("session: hkdf: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("session: aead: %w", err)
	}
	return &Session{aead: aead}, nil
}

// Seal encrypts plaintext. The output is nonce || ciphertext with the nonce
// prepended; the nonce is generated with crypto/rand.
func (s *Session) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(nonce)+len(plaintext)+s.aead.Overhead())
	out = append(out, nonce...)
	out = s.aead.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// Open reverses Seal.
func (s *Session) Open(frame []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(frame) < ns+s.aead.Overhead() {
		return nil, errors.New("session: frame too short")
	}
	return s.aead.Open(nil, frame[:ns], frame[ns:], nil)
}

func sortedKeys(a, b []byte) ([]byte, []byte) {
	if bytes.Compare(a, b) < 0 {
		return a, b
	}
	return b, a
}
