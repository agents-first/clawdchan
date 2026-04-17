package envelope

import (
	"bytes"
	"testing"

	"github.com/vMaroon/ClawdChan/core/identity"
)

func fixture(t *testing.T) (*identity.Identity, Envelope) {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id, Envelope{
		Version:     Version,
		EnvelopeID:  ULID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		ThreadID:    ULID{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		From:        Principal{NodeID: id.SigningPublic, Role: RoleAgent, Alias: "tester"},
		Intent:      IntentSay,
		CreatedAtMs: 1700000000000,
		Content:     Content{Kind: ContentText, Text: "hello from ClawdChan"},
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	id, env := fixture(t)
	if err := Sign(&env, id); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := Verify(env); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyDetectsContentTamper(t *testing.T) {
	id, env := fixture(t)
	if err := Sign(&env, id); err != nil {
		t.Fatal(err)
	}
	env.Content.Text = "tampered"
	if err := Verify(env); err == nil {
		t.Fatal("expected verify to fail on tampered content")
	}
}

func TestVerifyDetectsIntentTamper(t *testing.T) {
	id, env := fixture(t)
	if err := Sign(&env, id); err != nil {
		t.Fatal(err)
	}
	env.Intent = IntentAskHuman
	if err := Verify(env); err == nil {
		t.Fatal("expected verify to fail on tampered intent")
	}
}

func TestCanonicalDeterministic(t *testing.T) {
	_, env := fixture(t)
	a, err := Canonical(env)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Canonical(env)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("canonical encoding not deterministic across calls")
	}
}

func TestCanonicalIgnoresSignatureField(t *testing.T) {
	_, env := fixture(t)
	a, err := Canonical(env)
	if err != nil {
		t.Fatal(err)
	}
	for i := range env.Signature {
		env.Signature[i] = 0xAB
	}
	b, err := Canonical(env)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("Canonical should zero the signature before encoding")
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	id, env := fixture(t)
	if err := Sign(&env, id); err != nil {
		t.Fatal(err)
	}
	wire, err := Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unmarshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(got); err != nil {
		t.Fatalf("verify after wire round trip: %v", err)
	}
	if got.From.Alias != env.From.Alias || got.Content.Text != env.Content.Text {
		t.Fatalf("fields mismatched after round trip")
	}
}

func TestVerifyFailsWithWrongSigner(t *testing.T) {
	id, env := fixture(t)
	if err := Sign(&env, id); err != nil {
		t.Fatal(err)
	}
	other, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env.From.NodeID = other.SigningPublic
	if err := Verify(env); err == nil {
		t.Fatal("expected verify to fail when signer NodeID swapped")
	}
}
