// Package identity holds the two long-term keypairs a ClawdChan node owns:
// Ed25519 for envelope signing and Noise-handshake static authentication, and
// X25519 for the Noise_IK key exchange.
package identity

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/fxamacker/cbor/v2"
)

// NodeID is the 32-byte Ed25519 public key that uniquely names a node.
type NodeID [ed25519.PublicKeySize]byte

// MarshalCBOR encodes a NodeID as a CBOR byte string, not an array of uint8.
func (n NodeID) MarshalCBOR() ([]byte, error) {
	return cbor.Marshal(n[:])
}

// UnmarshalCBOR decodes a CBOR byte string into a NodeID, enforcing length.
func (n *NodeID) UnmarshalCBOR(data []byte) error {
	var b []byte
	if err := cbor.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("identity.NodeID: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return fmt.Errorf("identity.NodeID: expected %d bytes, got %d", ed25519.PublicKeySize, len(b))
	}
	copy(n[:], b)
	return nil
}

// KexKey is the 32-byte X25519 public key used in the Noise_IK handshake.
type KexKey [32]byte

func (k KexKey) MarshalCBOR() ([]byte, error) {
	return cbor.Marshal(k[:])
}

func (k *KexKey) UnmarshalCBOR(data []byte) error {
	var b []byte
	if err := cbor.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("identity.KexKey: %w", err)
	}
	if len(b) != 32 {
		return fmt.Errorf("identity.KexKey: expected 32 bytes, got %d", len(b))
	}
	copy(k[:], b)
	return nil
}

// Identity is the long-term key material for a node.
type Identity struct {
	SigningPublic  NodeID
	SigningPrivate ed25519.PrivateKey
	KexPublic      KexKey
	KexPrivate     [32]byte // x25519 scalar, kept secret
}

type persisted struct {
	SignPub  []byte `json:"sign_pub"`
	SignPriv []byte `json:"sign_priv"`
	KexPub   []byte `json:"kex_pub"`
	KexPriv  []byte `json:"kex_priv"`
}

// Generate creates a fresh identity with Ed25519 and X25519 keypairs.
func Generate() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519: %w", err)
	}
	kex, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate x25519: %w", err)
	}
	id := &Identity{SigningPrivate: priv}
	copy(id.SigningPublic[:], pub)
	copy(id.KexPublic[:], kex.PublicKey().Bytes())
	copy(id.KexPrivate[:], kex.Bytes())
	return id, nil
}

// Load reads an identity from disk at path.
func Load(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read identity: %w", err)
	}
	var p persisted
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse identity: %w", err)
	}
	if len(p.SignPub) != ed25519.PublicKeySize {
		return nil, errors.New("identity: invalid signing public key length")
	}
	if len(p.SignPriv) != ed25519.PrivateKeySize {
		return nil, errors.New("identity: invalid signing private key length")
	}
	if len(p.KexPub) != 32 || len(p.KexPriv) != 32 {
		return nil, errors.New("identity: invalid kex key length")
	}
	id := &Identity{SigningPrivate: ed25519.PrivateKey(append([]byte(nil), p.SignPriv...))}
	copy(id.SigningPublic[:], p.SignPub)
	copy(id.KexPublic[:], p.KexPub)
	copy(id.KexPrivate[:], p.KexPriv)
	return id, nil
}

// Save persists id to disk at path with owner-only permissions.
func Save(id *Identity, path string) error {
	p := persisted{
		SignPub:  append([]byte(nil), id.SigningPublic[:]...),
		SignPriv: append([]byte(nil), id.SigningPrivate...),
		KexPub:   append([]byte(nil), id.KexPublic[:]...),
		KexPriv:  append([]byte(nil), id.KexPrivate[:]...),
	}
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal identity: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write identity: %w", err)
	}
	return nil
}
