// Package envelope defines the ClawdChan wire message format and its signing
// rules. See docs/design.md for the canonical specification.
package envelope

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/fxamacker/cbor/v2"

	"github.com/agents-first/clawdchan/core/identity"
)

const Version uint8 = 1

// ULID is a 128-bit sortable identifier used for envelope_id, thread_id, and
// parent_id.
type ULID [16]byte

// ThreadID identifies a conversation thread between two paired nodes.
type ThreadID = ULID

// Signature is the 64-byte Ed25519 signature over the canonical envelope.
type Signature [64]byte

func (u ULID) MarshalCBOR() ([]byte, error) { return cbor.Marshal(u[:]) }

func (u *ULID) UnmarshalCBOR(data []byte) error {
	var b []byte
	if err := cbor.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("envelope.ULID: %w", err)
	}
	if len(b) != 16 {
		return fmt.Errorf("envelope.ULID: expected 16 bytes, got %d", len(b))
	}
	copy(u[:], b)
	return nil
}

func (s Signature) MarshalCBOR() ([]byte, error) { return cbor.Marshal(s[:]) }

func (s *Signature) UnmarshalCBOR(data []byte) error {
	var b []byte
	if err := cbor.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("envelope.Signature: %w", err)
	}
	if len(b) != 64 {
		return fmt.Errorf("envelope.Signature: expected 64 bytes, got %d", len(b))
	}
	copy(s[:], b)
	return nil
}

// Role distinguishes agent and human principals on a node.
type Role uint8

const (
	RoleAgent Role = 1
	RoleHuman Role = 2
)

// Intent is the verb of an envelope. See docs/design.md § Intents.
type Intent uint8

const (
	IntentSay         Intent = 0
	IntentAsk         Intent = 1
	IntentNotifyHuman Intent = 2
	IntentAskHuman    Intent = 3
	IntentHandoff     Intent = 4
	IntentAck         Intent = 5
	IntentClose       Intent = 6
)

// ContentKind discriminates the Content union.
type ContentKind uint8

const (
	ContentText   ContentKind = 1
	ContentDigest ContentKind = 2
)

// Principal names a speaker on a node.
type Principal struct {
	NodeID identity.NodeID `cbor:"1,keyasint"`
	Role   Role            `cbor:"2,keyasint"`
	Alias  string          `cbor:"3,keyasint"`
}

// Content is the payload union. Exactly one of Text/Digest-pair is populated
// per Kind; other string fields remain empty.
type Content struct {
	Kind  ContentKind `cbor:"1,keyasint"`
	Text  string      `cbor:"2,keyasint"`
	Title string      `cbor:"3,keyasint"`
	Body  string      `cbor:"4,keyasint"`
}

// Envelope is the signed, end-to-end unit of communication.
type Envelope struct {
	Version     uint8     `cbor:"1,keyasint"`
	EnvelopeID  ULID      `cbor:"2,keyasint"`
	ThreadID    ThreadID  `cbor:"3,keyasint"`
	ParentID    ULID      `cbor:"4,keyasint"`
	From        Principal `cbor:"5,keyasint"`
	Intent      Intent    `cbor:"6,keyasint"`
	CreatedAtMs int64     `cbor:"7,keyasint"`
	Content     Content   `cbor:"8,keyasint"`
	Signature   Signature `cbor:"9,keyasint"`
}

var (
	canonicalEnc cbor.EncMode
	stdDec       cbor.DecMode
)

func init() {
	enc, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(fmt.Errorf("envelope: init canonical encoder: %w", err))
	}
	canonicalEnc = enc

	dec, err := cbor.DecOptions{}.DecMode()
	if err != nil {
		panic(fmt.Errorf("envelope: init decoder: %w", err))
	}
	stdDec = dec
}

// Canonical returns the deterministic CBOR encoding of env with Signature
// zeroed. It is the exact byte string that Sign signs and Verify verifies.
func Canonical(env Envelope) ([]byte, error) {
	env.Signature = Signature{}
	return canonicalEnc.Marshal(env)
}

// Sign computes env.Signature using id over Canonical(env).
func Sign(env *Envelope, id *identity.Identity) error {
	b, err := Canonical(*env)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(id.SigningPrivate, b)
	copy(env.Signature[:], sig)
	return nil
}

// ErrBadSignature is returned by Verify when an envelope does not verify
// against the NodeID in its From principal.
var ErrBadSignature = errors.New("envelope: signature verification failed")

// Verify checks env.Signature against env.From.NodeID.
func Verify(env Envelope) error {
	sig := env.Signature
	b, err := Canonical(env)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(env.From.NodeID[:]), b, sig[:]) {
		return ErrBadSignature
	}
	return nil
}

// Marshal serializes env (including Signature) for transport. The output is
// byte-for-byte equal to Canonical(env) with the signature field populated.
func Marshal(env Envelope) ([]byte, error) {
	return canonicalEnc.Marshal(env)
}

// Unmarshal parses b into an Envelope. It does not verify the signature; call
// Verify afterwards.
func Unmarshal(b []byte) (Envelope, error) {
	var env Envelope
	if err := stdDec.Unmarshal(b, &env); err != nil {
		return Envelope{}, fmt.Errorf("envelope unmarshal: %w", err)
	}
	return env, nil
}
