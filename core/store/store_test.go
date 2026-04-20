package store

import (
	"context"
	"database/sql"
	"errors"
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

func TestOpenClawSessionPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "clawdchan.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := identity.Generate()
	nodeID := id.SigningPublic

	if sid, ok, err := s1.GetOpenClawSession(ctx, nodeID); err != nil {
		s1.Close()
		t.Fatal(err)
	} else if ok || sid != "" {
		s1.Close()
		t.Fatalf("expected missing openclaw session, got sid=%q ok=%v", sid, ok)
	}
	if err := s1.SetOpenClawSession(ctx, nodeID, "sid-openclaw"); err != nil {
		s1.Close()
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	sid, ok, err := s2.GetOpenClawSession(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || sid != "sid-openclaw" {
		t.Fatalf("openclaw session roundtrip mismatch: sid=%q ok=%v", sid, ok)
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

// TestPurgeConversations verifies the MCP ephemeral-boot contract: threads,
// envelopes, and outbox are wiped; identity and peers (pairings) survive.
func TestPurgeConversations(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	id, _ := identity.Generate()
	if err := s.SaveIdentity(ctx, id); err != nil {
		t.Fatal(err)
	}
	peerID, _ := identity.Generate()
	peer := pairing.Peer{
		NodeID:     peerID.SigningPublic,
		KexPub:     peerID.KexPublic,
		Alias:      "bruce",
		Trust:      pairing.TrustPaired,
		PairedAtMs: time.Now().UnixMilli(),
	}
	if err := s.UpsertPeer(ctx, peer); err != nil {
		t.Fatal(err)
	}

	thread := Thread{
		ID:        envelope.ULID{7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7},
		PeerID:    peer.NodeID,
		Topic:     "ephemeral",
		CreatedMs: time.Now().UnixMilli(),
	}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
	env := envelope.Envelope{
		Version:     envelope.Version,
		EnvelopeID:  envelope.ULID{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3},
		ThreadID:    thread.ID,
		From:        envelope.Principal{NodeID: id.SigningPublic, Role: envelope.RoleAgent},
		Intent:      envelope.IntentSay,
		CreatedAtMs: time.Now().UnixMilli(),
		Content:     envelope.Content{Kind: envelope.ContentText, Text: "x"},
	}
	if err := envelope.Sign(&env, id); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEnvelope(ctx, env, true); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueOutbox(ctx, peer.NodeID, env); err != nil {
		t.Fatal(err)
	}

	if err := s.PurgeConversations(ctx); err != nil {
		t.Fatalf("purge: %v", err)
	}

	// Threads gone.
	ts, err := s.ListThreads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 0 {
		t.Fatalf("expected 0 threads after purge, got %d", len(ts))
	}
	// Envelopes gone.
	es, _ := s.ListEnvelopes(ctx, thread.ID, 0)
	if len(es) != 0 {
		t.Fatalf("expected 0 envelopes after purge, got %d", len(es))
	}
	// Outbox gone.
	drained, _ := s.DrainOutbox(ctx, peer.NodeID)
	if len(drained) != 0 {
		t.Fatalf("expected 0 outbox entries after purge, got %d", len(drained))
	}
	// Identity survives.
	if _, err := s.LoadIdentity(ctx); err != nil {
		t.Fatalf("identity lost on purge: %v", err)
	}
	// Peer survives.
	got, err := s.GetPeer(ctx, peer.NodeID)
	if err != nil {
		t.Fatalf("peer lost on purge: %v", err)
	}
	if got.Alias != "bruce" {
		t.Fatalf("peer alias mangled: %q", got.Alias)
	}
}

func TestMarkEnvelopeSent_Idempotent(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	me := mustSaveIdentity(t, s, ctx)
	peer := mustGenerateIdentity(t)
	thread := mustCreateThread(t, s, ctx, envelope.ULID{0x10}, peer.SigningPublic)
	env := mustAppendSignedEnvelope(t, s, ctx, me, envelope.ULID{0x11}, thread.ID, 100, "hello")

	if err := s.MarkEnvelopeSent(ctx, env.EnvelopeID, 111); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEnvelopeSent(ctx, env.EnvelopeID, 222); err != nil {
		t.Fatal(err)
	}

	recs, err := s.ListEnvelopeRecords(ctx, thread.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	if recs[0].Status != StatusSent {
		t.Fatalf("expected status sent, got %v", recs[0].Status)
	}
	if recs[0].SentAtMs != 111 {
		t.Fatalf("expected sent_at_ms=111, got %d", recs[0].SentAtMs)
	}
	if recs[0].DeliveredAtMs != 0 {
		t.Fatalf("expected delivered_at_ms unset, got %d", recs[0].DeliveredAtMs)
	}
}

func TestMarkEnvelopeDelivered_Idempotent(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	me := mustSaveIdentity(t, s, ctx)
	peer := mustGenerateIdentity(t)
	thread := mustCreateThread(t, s, ctx, envelope.ULID{0x20}, peer.SigningPublic)
	env := mustAppendSignedEnvelope(t, s, ctx, me, envelope.ULID{0x21}, thread.ID, 100, "hello")

	if err := s.MarkEnvelopeDelivered(ctx, env.EnvelopeID, 333); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEnvelopeDelivered(ctx, env.EnvelopeID, 444); err != nil {
		t.Fatal(err)
	}

	recs, err := s.ListEnvelopeRecords(ctx, thread.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	if recs[0].Status != StatusDelivered {
		t.Fatalf("expected status delivered, got %v", recs[0].Status)
	}
	if recs[0].DeliveredAtMs != 333 {
		t.Fatalf("expected delivered_at_ms=333, got %d", recs[0].DeliveredAtMs)
	}
}

func TestMarkEnvelopeSent_DoesNotRegressFromDelivered(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	me := mustSaveIdentity(t, s, ctx)
	peer := mustGenerateIdentity(t)
	thread := mustCreateThread(t, s, ctx, envelope.ULID{0x30}, peer.SigningPublic)
	env := mustAppendSignedEnvelope(t, s, ctx, me, envelope.ULID{0x31}, thread.ID, 100, "hello")

	if err := s.MarkEnvelopeDelivered(ctx, env.EnvelopeID, 500); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEnvelopeSent(ctx, env.EnvelopeID, 100); err != nil {
		t.Fatal(err)
	}

	recs, err := s.ListEnvelopeRecords(ctx, thread.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	if recs[0].Status != StatusDelivered {
		t.Fatalf("expected status delivered, got %v", recs[0].Status)
	}
	if recs[0].DeliveredAtMs != 500 {
		t.Fatalf("expected delivered_at_ms=500, got %d", recs[0].DeliveredAtMs)
	}
}

func TestGetOutboundEnvelope_RejectsInbound(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	_ = mustSaveIdentity(t, s, ctx)
	peer := mustGenerateIdentity(t)
	thread := mustCreateThread(t, s, ctx, envelope.ULID{0x40}, peer.SigningPublic)
	inbound := mustAppendSignedEnvelope(t, s, ctx, peer, envelope.ULID{0x41}, thread.ID, 100, "inbound")

	_, _, ok, err := s.GetOutboundEnvelope(ctx, inbound.EnvelopeID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected inbound envelope to be rejected")
	}
}

func TestGetOutboundEnvelope_AcceptsOurOwn(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	me := mustSaveIdentity(t, s, ctx)
	peer := mustGenerateIdentity(t)
	thread := mustCreateThread(t, s, ctx, envelope.ULID{0x50}, peer.SigningPublic)
	outbound := mustAppendSignedEnvelope(t, s, ctx, me, envelope.ULID{0x51}, thread.ID, 100, "outbound")

	got, gotPeer, ok, err := s.GetOutboundEnvelope(ctx, outbound.EnvelopeID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected outbound envelope to be returned")
	}
	if gotPeer != peer.SigningPublic {
		t.Fatalf("expected peer %x, got %x", peer.SigningPublic, gotPeer)
	}
	if got.EnvelopeID != outbound.EnvelopeID {
		t.Fatalf("wrong envelope returned: %x", got.EnvelopeID)
	}
}

func TestListEnvelopeRecords_ReturnsStatusFields(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	me := mustSaveIdentity(t, s, ctx)
	peer := mustGenerateIdentity(t)
	thread := mustCreateThread(t, s, ctx, envelope.ULID{0x60}, peer.SigningPublic)
	outbound := mustAppendSignedEnvelope(t, s, ctx, me, envelope.ULID{0x61}, thread.ID, 100, "outbound")
	inbound := mustAppendSignedEnvelope(t, s, ctx, peer, envelope.ULID{0x62}, thread.ID, 200, "inbound")

	if err := s.MarkEnvelopeSent(ctx, outbound.EnvelopeID, 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEnvelopeDelivered(ctx, outbound.EnvelopeID, 2000); err != nil {
		t.Fatal(err)
	}

	recs, err := s.ListEnvelopeRecords(ctx, thread.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}

	var gotOutbound, gotInbound *EnvelopeRecord
	for i := range recs {
		switch recs[i].Envelope.EnvelopeID {
		case outbound.EnvelopeID:
			gotOutbound = &recs[i]
		case inbound.EnvelopeID:
			gotInbound = &recs[i]
		}
	}
	if gotOutbound == nil || gotInbound == nil {
		t.Fatalf("missing outbound/inbound records: outbound=%v inbound=%v", gotOutbound != nil, gotInbound != nil)
	}
	if gotOutbound.Status != StatusDelivered {
		t.Fatalf("outbound status: want delivered, got %v", gotOutbound.Status)
	}
	if gotOutbound.SentAtMs != 1000 || gotOutbound.DeliveredAtMs != 2000 {
		t.Fatalf("outbound timestamps wrong: sent=%d delivered=%d", gotOutbound.SentAtMs, gotOutbound.DeliveredAtMs)
	}
	if gotInbound.Status != StatusQueued {
		t.Fatalf("inbound status: want queued, got %v", gotInbound.Status)
	}
	if gotInbound.SentAtMs != 0 || gotInbound.DeliveredAtMs != 0 {
		t.Fatalf("inbound timestamps should be unset, got sent=%d delivered=%d", gotInbound.SentAtMs, gotInbound.DeliveredAtMs)
	}
}

func TestMigration_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE envelopes (
    envelope_id BLOB PRIMARY KEY,
    thread_id   BLOB NOT NULL,
    from_node   BLOB NOT NULL,
    from_role   INTEGER NOT NULL,
    intent      INTEGER NOT NULL,
    created_ms  INTEGER NOT NULL,
    blob        BLOB NOT NULL,
    delivered   INTEGER NOT NULL DEFAULT 0
)`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}

	cols := envelopeColumns(t, path)
	for _, want := range []string{"status", "sent_at_ms", "delivered_at_ms"} {
		if _, ok := cols[want]; !ok {
			t.Fatalf("missing migrated column %q; columns=%v", want, cols)
		}
	}
}

func mustGenerateIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustSaveIdentity(t *testing.T, s Store, ctx context.Context) *identity.Identity {
	t.Helper()
	id := mustGenerateIdentity(t)
	if err := s.SaveIdentity(ctx, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func mustCreateThread(t *testing.T, s Store, ctx context.Context, threadID envelope.ThreadID, peer identity.NodeID) Thread {
	t.Helper()
	th := Thread{
		ID:        threadID,
		PeerID:    peer,
		Topic:     "status-test",
		CreatedMs: time.Now().UnixMilli(),
	}
	if err := s.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	return th
}

func mustAppendSignedEnvelope(t *testing.T, s Store, ctx context.Context, signer *identity.Identity, envelopeID, threadID envelope.ULID, createdAtMs int64, text string) envelope.Envelope {
	t.Helper()
	env := envelope.Envelope{
		Version:     envelope.Version,
		EnvelopeID:  envelopeID,
		ThreadID:    threadID,
		From:        envelope.Principal{NodeID: signer.SigningPublic, Role: envelope.RoleAgent},
		Intent:      envelope.IntentSay,
		CreatedAtMs: createdAtMs,
		Content:     envelope.Content{Kind: envelope.ContentText, Text: text},
	}
	if err := envelope.Sign(&env, signer); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEnvelope(ctx, env, false); err != nil {
		t.Fatal(err)
	}
	return env
}

func envelopeColumns(t *testing.T, path string) map[string]struct{} {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(`PRAGMA table_info(envelopes)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		out[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMarkEnvelopeDelivered_NotFound(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	_ = mustSaveIdentity(t, s, ctx)
	err := s.MarkEnvelopeDelivered(ctx, envelope.ULID{0xFF}, 1234)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
