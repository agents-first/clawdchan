package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/pairing"
)

func openTemp(t *testing.T) Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clawdchan.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestIdentityPersistence(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.LoadIdentity(ctx); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveIdentity(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.SigningPublic != id.SigningPublic || got.KexPublic != id.KexPublic {
		t.Fatal("identity roundtrip mismatch")
	}
}

func TestPeerCRUD(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	id, _ := identity.Generate()
	p := pairing.Peer{
		NodeID:         id.SigningPublic,
		KexPub:         id.KexPublic,
		Alias:          "alice",
		HumanReachable: true,
		Trust:          pairing.TrustPaired,
		PairedAtMs:     time.Now().UnixMilli(),
		SAS:            [4]string{"apple", "bowl", "cat", "drum"},
	}
	if err := s.UpsertPeer(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPeer(ctx, p.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Alias != "alice" || got.Trust != pairing.TrustPaired {
		t.Fatalf("peer roundtrip wrong: %+v", got)
	}
	list, err := s.ListPeers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(list))
	}
	if err := s.RevokePeer(ctx, p.NodeID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetPeer(ctx, p.NodeID)
	if got.Trust != pairing.TrustRevoked {
		t.Fatalf("expected revoked, got %v", got.Trust)
	}
}

func TestThreadAndEnvelopes(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	id, _ := identity.Generate()
	thread := Thread{
		ID:        envelope.ULID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		PeerID:    id.SigningPublic,
		Topic:     "test",
		CreatedMs: time.Now().UnixMilli(),
	}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatal(err)
	}

	env := envelope.Envelope{
		Version:     envelope.Version,
		EnvelopeID:  envelope.ULID{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9},
		ThreadID:    thread.ID,
		From:        envelope.Principal{NodeID: id.SigningPublic, Role: envelope.RoleAgent, Alias: "me"},
		Intent:      envelope.IntentSay,
		CreatedAtMs: time.Now().UnixMilli(),
		Content:     envelope.Content{Kind: envelope.ContentText, Text: "hi"},
	}
	if err := envelope.Sign(&env, id); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEnvelope(ctx, env, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListEnvelopes(ctx, thread.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 envelope, got %d", len(got))
	}
	if err := envelope.Verify(got[0]); err != nil {
		t.Fatalf("stored envelope no longer verifies: %v", err)
	}
}

func TestOutbox(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	id, _ := identity.Generate()
	peer, _ := identity.Generate()

	env := envelope.Envelope{
		Version:     envelope.Version,
		EnvelopeID:  envelope.ULID{1},
		ThreadID:    envelope.ULID{2},
		From:        envelope.Principal{NodeID: id.SigningPublic, Role: envelope.RoleAgent},
		Intent:      envelope.IntentSay,
		CreatedAtMs: time.Now().UnixMilli(),
		Content:     envelope.Content{Kind: envelope.ContentText, Text: "queued"},
	}
	envelope.Sign(&env, id)

	if err := s.EnqueueOutbox(ctx, peer.SigningPublic, env); err != nil {
		t.Fatal(err)
	}
	got, err := s.DrainOutbox(ctx, peer.SigningPublic)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content.Text != "queued" {
		t.Fatalf("drain mismatch: %+v", got)
	}
	empty, _ := s.DrainOutbox(ctx, peer.SigningPublic)
	if len(empty) != 0 {
		t.Fatalf("expected empty after drain, got %d", len(empty))
	}
}
