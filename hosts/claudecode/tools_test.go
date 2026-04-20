package claudecode

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/pairing"
	"github.com/vMaroon/ClawdChan/core/store"
)

// TestPendingAsks verifies the ask_human enforcement invariant: a remote
// ask_human is considered pending (must be surfaced to the human) until a
// subsequent role=human envelope from us exists on the same thread. An
// agent-role reply from us does NOT close the ask.
func TestPendingAsks(t *testing.T) {
	var me, peer identity.NodeID
	me[0] = 0xAA
	peer[0] = 0xBB

	mkEnv := func(id byte, from identity.NodeID, role envelope.Role, intent envelope.Intent, text string, ts int64) envelope.Envelope {
		var eid envelope.ULID
		eid[0] = id
		return envelope.Envelope{
			EnvelopeID:  eid,
			From:        envelope.Principal{NodeID: from, Role: role, Alias: "x"},
			Intent:      intent,
			CreatedAtMs: ts,
			Content:     envelope.Content{Kind: envelope.ContentText, Text: text},
		}
	}

	ask := mkEnv(1, peer, envelope.RoleAgent, envelope.IntentAskHuman, "secret question", 100)
	chatter := mkEnv(2, peer, envelope.RoleAgent, envelope.IntentSay, "chitchat", 200)

	t.Run("unanswered ask_human is pending", func(t *testing.T) {
		idx := pendingAsks([]envelope.Envelope{ask, chatter}, me)
		if !idx[ask.EnvelopeID] {
			t.Fatalf("unanswered ask_human not flagged as pending")
		}
		if idx[chatter.EnvelopeID] {
			t.Fatalf("unrelated envelope flagged as pending")
		}
	})

	t.Run("ask_human answered by local human is not pending", func(t *testing.T) {
		reply := mkEnv(3, me, envelope.RoleHuman, envelope.IntentSay, "my answer", 300)
		idx := pendingAsks([]envelope.Envelope{ask, reply}, me)
		if len(idx) != 0 {
			t.Fatalf("answered ask_human still flagged; idx=%v", idx)
		}
	})

	t.Run("agent-role reply does not close the ask", func(t *testing.T) {
		sneaky := mkEnv(4, me, envelope.RoleAgent, envelope.IntentSay, "i'll answer for them", 300)
		idx := pendingAsks([]envelope.Envelope{ask, sneaky}, me)
		if !idx[ask.EnvelopeID] {
			t.Fatalf("agent-role reply unexpectedly closed the ask; idx=%v", idx)
		}
	})

	t.Run("locally-originated ask_human is not pending", func(t *testing.T) {
		myAsk := mkEnv(5, me, envelope.RoleAgent, envelope.IntentAskHuman, "for peer", 100)
		idx := pendingAsks([]envelope.Envelope{myAsk}, me)
		if len(idx) != 0 {
			t.Fatalf("our own outgoing ask_human flagged as pending: %v", idx)
		}
	})
}

// TestSerializeEnvelopeDirection verifies the two derived fields Claude
// sees on every envelope: `direction` (in/out, from comparing From.NodeID
// to me) and `collab` (true when the content carries the reserved
// CollabSyncTitle). The agent should not have to compare hex strings or
// pattern-match title strings to get either piece of information.
func TestSerializeEnvelopeDirection(t *testing.T) {
	var me, peer identity.NodeID
	me[0] = 0xAA
	peer[0] = 0xBB

	plain := envelope.Envelope{
		EnvelopeID:  envelope.ULID{0x01},
		From:        envelope.Principal{NodeID: peer, Role: envelope.RoleAgent, Alias: "x"},
		Intent:      envelope.IntentAsk,
		CreatedAtMs: 100,
		Content:     envelope.Content{Kind: envelope.ContentText, Text: "hello"},
	}
	collab := envelope.Envelope{
		EnvelopeID:  envelope.ULID{0x02},
		From:        envelope.Principal{NodeID: me, Role: envelope.RoleAgent, Alias: "x"},
		Intent:      envelope.IntentAsk,
		CreatedAtMs: 101,
		Content:     envelope.Content{Kind: envelope.ContentDigest, Title: "clawdchan:collab_sync", Body: "live"},
	}

	in := serializeEnvelope(plain, me, false, nil)
	if in["direction"] != "in" || in["collab"] != false {
		t.Fatalf("plain peer envelope: direction=%v collab=%v", in["direction"], in["collab"])
	}
	if _, ok := in["content"].(map[string]any); !ok {
		t.Fatalf("full render should include content, got %v", in["content"])
	}

	out := serializeEnvelope(collab, me, false, nil)
	if out["direction"] != "out" || out["collab"] != true {
		t.Fatalf("self collab envelope: direction=%v collab=%v", out["direction"], out["collab"])
	}

	hdr := serializeEnvelope(plain, me, true, nil)
	if _, has := hdr["content"]; has {
		t.Fatalf("headers mode should omit content, got %v", hdr["content"])
	}
	if hdr["direction"] != "in" || hdr["collab"] != false {
		t.Fatalf("headers mode lost derived fields: %v", hdr)
	}
}

// TestOpenAskHumanDetection is the plumbing behind the pending_ask_hint
// field returned by clawdchan_message: any peer with an unanswered
// ask_human should register as having an open ask until the user either
// replies or declines. This is the safety check that nudges a confused
// agent away from answering via clawdchan_message instead of
// clawdchan_reply.
func TestOpenAskHumanDetection(t *testing.T) {
	var me, peer identity.NodeID
	me[0] = 0xAA
	peer[0] = 0xBB

	mk := func(from identity.NodeID, role envelope.Role, intent envelope.Intent, id byte, ts int64) envelope.Envelope {
		return envelope.Envelope{
			EnvelopeID:  envelope.ULID{id},
			From:        envelope.Principal{NodeID: from, Role: role, Alias: "x"},
			Intent:      intent,
			CreatedAtMs: ts,
			Content:     envelope.Content{Kind: envelope.ContentText, Text: "q?"},
		}
	}

	ask := mk(peer, envelope.RoleAgent, envelope.IntentAskHuman, 1, 100)

	t.Run("open ask shows as pending", func(t *testing.T) {
		idx := pendingAsks([]envelope.Envelope{ask}, me)
		if !idx[ask.EnvelopeID] {
			t.Fatalf("open ask_human not flagged as pending")
		}
	})

	t.Run("human reply closes the ask", func(t *testing.T) {
		reply := mk(me, envelope.RoleHuman, envelope.IntentSay, 2, 200)
		idx := pendingAsks([]envelope.Envelope{ask, reply}, me)
		if len(idx) != 0 {
			t.Fatalf("answered ask still pending: %v", idx)
		}
	})

	t.Run("agent-role send does not close the ask", func(t *testing.T) {
		sneaky := mk(me, envelope.RoleAgent, envelope.IntentSay, 3, 200)
		idx := pendingAsks([]envelope.Envelope{ask, sneaky}, me)
		if !idx[ask.EnvelopeID] {
			t.Fatalf("agent-role send unexpectedly closed the ask — this would defeat the pending_ask_hint safety")
		}
	})
}

// TestParseMessageIntent verifies the restricted intent vocabulary Claude can
// send. handoff / ack / close are deliberately not accepted via the MCP
// surface — they'd be confusing for the agent to reason about.
func TestParseMessageIntent(t *testing.T) {
	cases := []struct {
		in   string
		want envelope.Intent
		err  bool
	}{
		{"", envelope.IntentSay, false},
		{"say", envelope.IntentSay, false},
		{"ask", envelope.IntentAsk, false},
		{"notify_human", envelope.IntentNotifyHuman, false},
		{"notify-human", envelope.IntentNotifyHuman, false},
		{"ask_human", envelope.IntentAskHuman, false},
		{"ASK_HUMAN", envelope.IntentAskHuman, false},
		{"handoff", 0, true},
		{"close", 0, true},
		{"garbage", 0, true},
	}
	for _, c := range cases {
		got, err := parseMessageIntent(c.in)
		if c.err {
			if err == nil {
				t.Errorf("parseMessageIntent(%q): want error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMessageIntent(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseMessageIntent(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestUnreadStatsForThread(t *testing.T) {
	var me, peer identity.NodeID
	me[0] = 0xAA
	peer[0] = 0xBB

	mkRecord := func(id byte, from identity.NodeID, role envelope.Role, intent envelope.Intent, ts int64) store.EnvelopeRecord {
		return store.EnvelopeRecord{
			Envelope: envelope.Envelope{
				EnvelopeID:  envelope.ULID{id},
				From:        envelope.Principal{NodeID: from, Role: role, Alias: "x"},
				Intent:      intent,
				CreatedAtMs: ts,
				Content:     envelope.Content{Kind: envelope.ContentText, Text: "msg"},
			},
			Status: store.StatusDelivered,
		}
	}

	t.Run("counts inbound newer than latest local outbound", func(t *testing.T) {
		records := []store.EnvelopeRecord{
			mkRecord(1, peer, envelope.RoleAgent, envelope.IntentSay, 100),
			mkRecord(2, me, envelope.RoleAgent, envelope.IntentSay, 150),
			mkRecord(3, peer, envelope.RoleAgent, envelope.IntentSay, 200),
		}
		unread, pending, last := unreadStatsForThread(records, me)
		if unread != 1 || pending != 0 || last != 200 {
			t.Fatalf("unread=%d pending=%d last=%d", unread, pending, last)
		}
	})

	t.Run("pending ask_human remains unread and outbound ack does not clear unread", func(t *testing.T) {
		records := []store.EnvelopeRecord{
			mkRecord(4, peer, envelope.RoleAgent, envelope.IntentAskHuman, 100),
			mkRecord(5, me, envelope.RoleAgent, envelope.IntentAck, 200),
		}
		unread, pending, _ := unreadStatsForThread(records, me)
		if unread != 1 || pending != 1 {
			t.Fatalf("want unread=1 pending=1, got unread=%d pending=%d", unread, pending)
		}
	})
}

func TestStartupDigestSummary(t *testing.T) {
	cases := []struct {
		name    string
		unread  int
		pending int
		want    string
	}{
		{name: "empty", unread: 0, pending: 0, want: "You have no unread messages."},
		{name: "unread only", unread: 3, pending: 0, want: "You have 3 unread messages."},
		{name: "with pending", unread: 3, pending: 1, want: "You have 3 unread messages. 1 pending human ask is waiting on your answer."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := startupDigestSummary(tc.unread, tc.pending)
			if got != tc.want {
				t.Fatalf("summary=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestInbox_OutboundEnvelopesCarryStatus(t *testing.T) {
	ctx := context.Background()
	n, peer, tid := setupInboxTestNode(t)
	me := n.Identity()

	inbound := testEnvelope(0x01, tid, peer, envelope.RoleAgent, envelope.IntentSay, 100)
	outQueued := testEnvelope(0x02, tid, me, envelope.RoleAgent, envelope.IntentSay, 200)
	outSent := testEnvelope(0x03, tid, me, envelope.RoleAgent, envelope.IntentSay, 300)

	appendInboxEnvelope(t, n, inbound)
	appendInboxEnvelope(t, n, outQueued)
	appendInboxEnvelope(t, n, outSent)
	if err := n.Store().MarkEnvelopeSent(ctx, outSent.EnvelopeID, 350); err != nil {
		t.Fatalf("MarkEnvelopeSent: %v", err)
	}

	peers, _, _, _, _, _, err := collectInbox(ctx, n, 0, false)
	if err != nil {
		t.Fatalf("collectInbox: %v", err)
	}
	envs := mustPeerEnvelopes(t, peers)
	gotByID := map[string]map[string]any{}
	for _, e := range envs {
		gotByID[e["envelope_id"].(string)] = e
	}

	if gotByID[hex.EncodeToString(inbound.EnvelopeID[:])]["direction"] != "in" {
		t.Fatalf("inbound direction = %v, want in", gotByID[hex.EncodeToString(inbound.EnvelopeID[:])]["direction"])
	}
	if _, ok := gotByID[hex.EncodeToString(inbound.EnvelopeID[:])]["status"]; ok {
		t.Fatalf("inbound envelope unexpectedly has status: %v", gotByID[hex.EncodeToString(inbound.EnvelopeID[:])])
	}

	queued := gotByID[hex.EncodeToString(outQueued.EnvelopeID[:])]
	if queued["status"] != "queued" {
		t.Fatalf("queued outbound status = %v, want queued", queued["status"])
	}
	if _, ok := queued["sent_at_ms"]; ok {
		t.Fatalf("queued outbound unexpectedly has sent_at_ms: %v", queued["sent_at_ms"])
	}
	if _, ok := queued["delivered_at_ms"]; ok {
		t.Fatalf("queued outbound unexpectedly has delivered_at_ms: %v", queued["delivered_at_ms"])
	}

	sent := gotByID[hex.EncodeToString(outSent.EnvelopeID[:])]
	if sent["status"] != "sent" {
		t.Fatalf("sent outbound status = %v, want sent", sent["status"])
	}
	if sent["sent_at_ms"] != int64(350) {
		t.Fatalf("sent outbound sent_at_ms = %v, want 350", sent["sent_at_ms"])
	}
	if _, ok := sent["delivered_at_ms"]; ok {
		t.Fatalf("sent outbound unexpectedly has delivered_at_ms: %v", sent["delivered_at_ms"])
	}
}

func TestInbox_DeliveredOutboundHasTimestamp(t *testing.T) {
	ctx := context.Background()
	n, _, tid := setupInboxTestNode(t)
	me := n.Identity()
	out := testEnvelope(0x11, tid, me, envelope.RoleAgent, envelope.IntentSay, 100)
	appendInboxEnvelope(t, n, out)
	if err := n.Store().MarkEnvelopeSent(ctx, out.EnvelopeID, 150); err != nil {
		t.Fatalf("MarkEnvelopeSent: %v", err)
	}
	if err := n.Store().MarkEnvelopeDelivered(ctx, out.EnvelopeID, 200); err != nil {
		t.Fatalf("MarkEnvelopeDelivered: %v", err)
	}

	peers, _, _, _, _, _, err := collectInbox(ctx, n, 0, false)
	if err != nil {
		t.Fatalf("collectInbox: %v", err)
	}
	envs := mustPeerEnvelopes(t, peers)
	if len(envs) != 1 {
		t.Fatalf("len(envelopes) = %d, want 1", len(envs))
	}
	got := envs[0]
	if got["status"] != "delivered" {
		t.Fatalf("status = %v, want delivered", got["status"])
	}
	if got["sent_at_ms"] != int64(150) {
		t.Fatalf("sent_at_ms = %v, want 150", got["sent_at_ms"])
	}
	if got["delivered_at_ms"] != int64(200) {
		t.Fatalf("delivered_at_ms = %v, want 200", got["delivered_at_ms"])
	}
}

func TestInbox_FiltersIntentAck(t *testing.T) {
	ctx := context.Background()
	n, peer, tid := setupInboxTestNode(t)
	me := n.Identity()

	visible := testEnvelope(0x21, tid, peer, envelope.RoleAgent, envelope.IntentSay, 100)
	ack := testEnvelope(0x22, tid, peer, envelope.RoleAgent, envelope.IntentAck, 200)
	appendInboxEnvelope(t, n, visible)
	appendInboxEnvelope(t, n, ack)

	peers, _, _, _, _, _, err := collectInbox(ctx, n, 0, false)
	if err != nil {
		t.Fatalf("collectInbox: %v", err)
	}
	envs := mustPeerEnvelopes(t, peers)
	if len(envs) != 1 {
		t.Fatalf("len(envelopes) = %d, want 1", len(envs))
	}
	if envs[0]["intent"] != "say" {
		t.Fatalf("visible envelope intent = %v, want say", envs[0]["intent"])
	}
	for _, e := range envs {
		if e["intent"] == "ack" {
			t.Fatalf("ack envelope leaked into inbox output: %v", e)
		}
	}

	// Ensure outbound delivery metadata remains absent on inbound envelopes.
	if envs[0]["direction"] != "in" {
		t.Fatalf("direction = %v, want in", envs[0]["direction"])
	}
	if _, ok := envs[0]["status"]; ok {
		t.Fatalf("inbound envelope unexpectedly has status: %v", envs[0])
	}
	if me == peer {
		t.Fatal("test setup invalid: me and peer are equal")
	}
}

func TestInbox_NoteFiresOnlyForUnfinishedOutbound(t *testing.T) {
	ctx := context.Background()

	t.Run("fires for sent outbound", func(t *testing.T) {
		n, _, tid := setupInboxTestNode(t)
		me := n.Identity()
		out := testEnvelope(0x31, tid, me, envelope.RoleAgent, envelope.IntentSay, 100)
		appendInboxEnvelope(t, n, out)
		if err := n.Store().MarkEnvelopeSent(ctx, out.EnvelopeID, 120); err != nil {
			t.Fatalf("MarkEnvelopeSent: %v", err)
		}
		_, _, hasPending, hasCollab, hasUndeliveredOutbound, _, err := collectInbox(ctx, n, 0, false)
		if err != nil {
			t.Fatalf("collectInbox: %v", err)
		}
		notes := inboxNotes(hasPending, hasCollab, hasUndeliveredOutbound)
		if !containsNote(notes, outboundStatusNote) {
			t.Fatalf("status note missing; notes=%v", notes)
		}
	})

	t.Run("does not fire when all outbound are delivered", func(t *testing.T) {
		n, _, tid := setupInboxTestNode(t)
		me := n.Identity()
		out := testEnvelope(0x32, tid, me, envelope.RoleAgent, envelope.IntentSay, 100)
		appendInboxEnvelope(t, n, out)
		if err := n.Store().MarkEnvelopeSent(ctx, out.EnvelopeID, 120); err != nil {
			t.Fatalf("MarkEnvelopeSent: %v", err)
		}
		if err := n.Store().MarkEnvelopeDelivered(ctx, out.EnvelopeID, 130); err != nil {
			t.Fatalf("MarkEnvelopeDelivered: %v", err)
		}
		_, _, hasPending, hasCollab, hasUndeliveredOutbound, _, err := collectInbox(ctx, n, 0, false)
		if err != nil {
			t.Fatalf("collectInbox: %v", err)
		}
		notes := inboxNotes(hasPending, hasCollab, hasUndeliveredOutbound)
		if containsNote(notes, outboundStatusNote) {
			t.Fatalf("status note should not fire; notes=%v", notes)
		}
	})
}

func setupInboxTestNode(t *testing.T) (*node.Node, identity.NodeID, envelope.ThreadID) {
	t.Helper()
	n, err := node.New(node.Config{
		DataDir:  t.TempDir(),
		RelayURL: "ws://127.0.0.1:8787",
		Alias:    "me",
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	var peer identity.NodeID
	peer[0] = 0xBB
	if err := n.Store().UpsertPeer(context.Background(), pairing.Peer{
		NodeID:         peer,
		Alias:          "peer",
		HumanReachable: true,
		Trust:          pairing.TrustPaired,
		PairedAtMs:     1,
	}); err != nil {
		t.Fatalf("UpsertPeer: %v", err)
	}

	var tid envelope.ThreadID
	tid[0] = 0xAB
	if err := n.Store().CreateThread(context.Background(), store.Thread{
		ID:        tid,
		PeerID:    peer,
		Topic:     "test",
		CreatedMs: 1,
	}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	return n, peer, tid
}

func testEnvelope(id byte, tid envelope.ThreadID, from identity.NodeID, role envelope.Role, intent envelope.Intent, createdAt int64) envelope.Envelope {
	return envelope.Envelope{
		Version:     envelope.Version,
		EnvelopeID:  envelope.ULID{id},
		ThreadID:    tid,
		From:        envelope.Principal{NodeID: from, Role: role, Alias: "x"},
		Intent:      intent,
		CreatedAtMs: createdAt,
		Content:     envelope.Content{Kind: envelope.ContentText, Text: "msg"},
	}
}

func appendInboxEnvelope(t *testing.T, n *node.Node, env envelope.Envelope) {
	t.Helper()
	if err := n.Store().AppendEnvelope(context.Background(), env, true); err != nil {
		t.Fatalf("AppendEnvelope(%x): %v", env.EnvelopeID[:], err)
	}
}

func mustPeerEnvelopes(t *testing.T, peers []map[string]any) []map[string]any {
	t.Helper()
	if len(peers) != 1 {
		t.Fatalf("len(peers) = %d, want 1", len(peers))
	}
	envs, ok := peers[0]["envelopes"].([]map[string]any)
	if !ok {
		t.Fatalf("peer envelopes type = %T, want []map[string]any", peers[0]["envelopes"])
	}
	return envs
}

func containsNote(notes []string, needle string) bool {
	for _, note := range notes {
		if note == needle {
			return true
		}
	}
	return false
}
