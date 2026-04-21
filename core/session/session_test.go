package session_test

import (
	"bytes"
	"testing"

	"github.com/agents-first/ClawdChan/core/identity"
	"github.com/agents-first/ClawdChan/core/session"
)

func TestBothSidesDeriveSameKey(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

	aSession, err := session.New(alice, bob.KexPublic)
	if err != nil {
		t.Fatal(err)
	}
	bSession, err := session.New(bob, alice.KexPublic)
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("secrets across the wire")
	ct, err := aSession.Seal(msg)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := bSession.Open(ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, msg) {
		t.Fatalf("got %q want %q", pt, msg)
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()
	aSession, _ := session.New(alice, bob.KexPublic)
	bSession, _ := session.New(bob, alice.KexPublic)

	ct, _ := aSession.Seal([]byte("hi"))
	ct[len(ct)-1] ^= 0x01
	if _, err := bSession.Open(ct); err == nil {
		t.Fatal("expected open to fail on tampered ct")
	}
}

func TestCannotOpenWithWrongPeer(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()
	mallory, _ := identity.Generate()
	aSession, _ := session.New(alice, bob.KexPublic)
	mSession, _ := session.New(mallory, alice.KexPublic)

	ct, _ := aSession.Seal([]byte("for bob only"))
	if _, err := mSession.Open(ct); err == nil {
		t.Fatal("expected open to fail with wrong peer key")
	}
}
