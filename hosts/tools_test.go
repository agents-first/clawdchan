package hosts

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agents-first/clawdchan/core/envelope"
	"github.com/agents-first/clawdchan/core/identity"
	"github.com/agents-first/clawdchan/core/node"
	"github.com/agents-first/clawdchan/core/pairing"
	"github.com/agents-first/clawdchan/core/policy"
	"github.com/agents-first/clawdchan/internal/relayserver"
)

// TestPendingAsks verifies the ask_human enforcement invariant: a
// remote ask_human is considered pending (must be surfaced to the
// human) until a subsequent role=human envelope from us exists on
// the same thread. An agent-role reply from us does NOT close the
// ask — the whole point of the as_human=true requirement.
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
		idx := PendingAsks([]envelope.Envelope{ask, chatter}, me)
		if !idx[ask.EnvelopeID] {
			t.Fatalf("unanswered ask_human not flagged as pending")
		}
		if idx[chatter.EnvelopeID] {
			t.Fatalf("unrelated envelope flagged as pending")
		}
	})

	t.Run("ask_human answered by local human is not pending", func(t *testing.T) {
		reply := mkEnv(3, me, envelope.RoleHuman, envelope.IntentSay, "my answer", 300)
		idx := PendingAsks([]envelope.Envelope{ask, reply}, me)
		if len(idx) != 0 {
			t.Fatalf("answered ask_human still flagged; idx=%v", idx)
		}
	})

	t.Run("agent-role reply does not close the ask", func(t *testing.T) {
		sneaky := mkEnv(4, me, envelope.RoleAgent, envelope.IntentSay, "i'll answer for them", 300)
		idx := PendingAsks([]envelope.Envelope{ask, sneaky}, me)
		if !idx[ask.EnvelopeID] {
			t.Fatalf("agent-role reply unexpectedly closed the ask; idx=%v", idx)
		}
	})

	t.Run("locally-originated ask_human is not pending", func(t *testing.T) {
		myAsk := mkEnv(5, me, envelope.RoleAgent, envelope.IntentAskHuman, "for peer", 100)
		idx := PendingAsks([]envelope.Envelope{myAsk}, me)
		if len(idx) != 0 {
			t.Fatalf("our own outgoing ask_human flagged as pending: %v", idx)
		}
	})
}

// TestSerializeEnvelopeDirection verifies the two derived fields
// Claude sees on every envelope: direction (in/out, from comparing
// From.NodeID to me) and collab (true when the content carries the
// reserved CollabSyncTitle). The agent should not have to compare
// hex strings or pattern-match title strings to get either piece of
// information.
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

	in := SerializeEnvelope(plain, me, false)
	if in["direction"] != "in" || in["collab"] != false {
		t.Fatalf("plain peer envelope: direction=%v collab=%v", in["direction"], in["collab"])
	}
	if _, ok := in["content"].(map[string]any); !ok {
		t.Fatalf("full render should include content, got %v", in["content"])
	}

	out := SerializeEnvelope(collab, me, false)
	if out["direction"] != "out" || out["collab"] != true {
		t.Fatalf("self collab envelope: direction=%v collab=%v", out["direction"], out["collab"])
	}

	hdr := SerializeEnvelope(plain, me, true)
	if _, has := hdr["content"]; has {
		t.Fatalf("headers mode should omit content, got %v", hdr["content"])
	}
	if hdr["direction"] != "in" || hdr["collab"] != false {
		t.Fatalf("headers mode lost derived fields: %v", hdr)
	}
}

// TestParseMessageIntent verifies the restricted intent vocabulary
// Claude can send. handoff / ack / close are deliberately not
// accepted — they'd be confusing for the agent to reason about; the
// node uses them internally.
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
		got, err := ParseMessageIntent(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseMessageIntent(%q): want error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMessageIntent(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseMessageIntent(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestCursorOrder verifies the cursor semantics used by inbox: a
// hex-encoded ULID is a strict bytewise watermark, so an envelope
// whose id is lexicographically greater than the cursor is "fresh"
// and anything equal-or-less has already been seen.
func TestCursorOrder(t *testing.T) {
	a := envelope.ULID{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}
	b := envelope.ULID{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02}

	if !cursorLess(a, b) {
		t.Fatalf("expected a<b bytewise")
	}
	if cursorLess(b, a) {
		t.Fatalf("unexpected b<a")
	}
	if cursorLess(a, a) {
		t.Fatalf("equal cursors must not be 'less'")
	}
}

// TestCursorDecodeRoundtrip ensures the opaque cursor string is
// just hex(envelope_id) and can round-trip.
func TestCursorDecodeRoundtrip(t *testing.T) {
	orig := envelope.ULID{0xAB, 0xCD}
	s := encodeCursor(orig, envelope.ULID{})
	got, err := decodeCursor(s)
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if !bytes.Equal(orig[:], got[:]) {
		t.Fatalf("roundtrip failed: orig=%x got=%x", orig, got)
	}
}

// TestCursorDecodeBadHex rejects malformed cursors.
func TestCursorDecodeBadHex(t *testing.T) {
	if _, err := decodeCursor("not-hex"); err == nil {
		t.Fatal("expected error for non-hex cursor")
	}
	if _, err := decodeCursor("ab"); err == nil {
		t.Fatal("expected error for too-short cursor")
	}
}

func TestCollabToolsAreMCPOnlyRegistrations(t *testing.T) {
	n := newHostTestNode(t)
	for _, reg := range All(n, nil) {
		if bytes.HasPrefix([]byte(reg.Spec.Name), []byte("clawdchan_collab_")) {
			t.Fatalf("collab tool %q should not be in shared host surface", reg.Spec.Name)
		}
	}
	regs := CollabSessionTools(n)
	if len(regs) != 6 {
		t.Fatalf("expected 6 collab MCP tools, got %d", len(regs))
	}
	names := map[string]bool{}
	for _, reg := range regs {
		names[reg.Spec.Name] = true
	}
	for _, want := range []string{
		"clawdchan_collab_start",
		"clawdchan_collab_send",
		"clawdchan_collab_await",
		"clawdchan_collab_heartbeat",
		"clawdchan_collab_status",
		"clawdchan_collab_close",
	} {
		if !names[want] {
			t.Fatalf("missing collab registration %s", want)
		}
	}
}

func TestCollabSessionHandlersLifecycle(t *testing.T) {
	ctx := context.Background()
	n := newHostTestNode(t)
	peerID, peerIdentity := addHostTestPeer(t, n, "bob")

	start, err := collabStartHandler(n)(ctx, map[string]any{
		"peer_id":            hex.EncodeToString(peerID[:]),
		"topic":              "scorer review",
		"definition_of_done": "agree on patch",
		"max_rounds":         float64(2),
		"owner_id":           "agent-a",
		"lease_seconds":      float64(30),
	})
	if err != nil {
		t.Fatal(err)
	}
	session := start["session"].(map[string]any)
	sessionID := session["session_id"].(string)

	if _, err := collabHeartbeatHandler(n)(ctx, map[string]any{
		"session_id":    sessionID,
		"owner_id":      "agent-b",
		"lease_seconds": float64(30),
	}); err == nil {
		t.Fatal("expected live lease to reject another owner")
	}

	if _, err := collabSendHandler(n)(ctx, map[string]any{
		"session_id": sessionID,
		"text":       "Please review this scorer patch.",
		"owner_id":   "agent-a",
	}); err != nil {
		t.Fatal(err)
	}
	cs, err := n.GetCollabSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	env := envelope.Envelope{
		Version:     envelope.Version,
		EnvelopeID:  envelope.ULID{0xFE},
		ThreadID:    cs.ThreadID,
		From:        envelope.Principal{NodeID: peerID, Role: envelope.RoleAgent, Alias: "bob"},
		Intent:      envelope.IntentSay,
		CreatedAtMs: time.Now().UnixMilli(),
		Content:     envelope.Content{Kind: envelope.ContentDigest, Title: policy.CollabSyncTitle, Body: "Looks good with one nit."},
	}
	if err := envelope.Sign(&env, peerIdentity); err != nil {
		t.Fatal(err)
	}
	if err := n.Store().AppendEnvelope(ctx, env, true); err != nil {
		t.Fatal(err)
	}

	await, err := collabAwaitHandler(n)(ctx, map[string]any{
		"session_id":   sessionID,
		"wait_seconds": float64(0),
		"owner_id":     "agent-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if await["new"] != 1 {
		t.Fatalf("expected one peer envelope, got %v", await)
	}
	envelopes := await["envelopes"].([]map[string]any)
	if envelopes[0]["direction"] != "in" || envelopes[0]["collab"] != true {
		t.Fatalf("await lost peer/collab envelope details: %+v", envelopes[0])
	}

	closed, err := collabCloseHandler(n)(ctx, map[string]any{
		"session_id":   sessionID,
		"status":       "converged",
		"summary":      "Reviewed and converged.",
		"close_reason": "definition_of_done",
	})
	if err != nil {
		t.Fatal(err)
	}
	closedSession := closed["session"].(map[string]any)
	if closedSession["status"] != node.CollabStatusConverged {
		t.Fatalf("expected converged close, got %+v", closedSession)
	}
}

func TestCollabAwaitTimeoutReturnsNoNewState(t *testing.T) {
	ctx := context.Background()
	n := newHostTestNode(t)
	peerID, _ := addHostTestPeer(t, n, "bob")
	start, err := collabStartHandler(n)(ctx, map[string]any{
		"peer_id":  hex.EncodeToString(peerID[:]),
		"owner_id": "agent-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := start["session"].(map[string]any)["session_id"].(string)
	await, err := collabAwaitHandler(n)(ctx, map[string]any{
		"session_id":   sessionID,
		"wait_seconds": float64(0),
		"owner_id":     "agent-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if await["new"] != 0 {
		t.Fatalf("expected no-new timeout response, got %+v", await)
	}
	if _, ok := await["next_cursor"].(string); !ok {
		t.Fatalf("expected next_cursor in no-new response, got %+v", await)
	}
}

func TestCollabSessionTwoNodeRoundTrip(t *testing.T) {
	relay := hostSpinRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	alice, err := node.New(node.Config{DataDir: t.TempDir(), RelayURL: relay, Alias: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = alice.Close() })
	bob, err := node.New(node.Config{DataDir: t.TempDir(), RelayURL: relay, Alias: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bob.Close() })
	if err := alice.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := bob.Start(ctx); err != nil {
		t.Fatal(err)
	}
	code, pairCh, err := alice.Pair(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Consume(ctx, code.Mnemonic()); err != nil {
		t.Fatal(err)
	}
	if res := <-pairCh; res.Err != nil {
		t.Fatal(res.Err)
	}

	bobID := bob.Identity()
	start, err := collabStartHandler(alice)(ctx, map[string]any{
		"peer_id":  hex.EncodeToString(bobID[:]),
		"topic":    "two node",
		"owner_id": "agent-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := start["session"].(map[string]any)["session_id"].(string)
	if _, err := collabSendHandler(alice)(ctx, map[string]any{
		"session_id": sessionID,
		"text":       "live?",
		"intent":     "ask",
		"owner_id":   "agent-a",
	}); err != nil {
		t.Fatal(err)
	}

	var bobThread envelope.ThreadID
	hostEventually(t, 4*time.Second, func() bool {
		threads, err := bob.ListThreads(ctx)
		if err != nil {
			return false
		}
		for _, th := range threads {
			envs, _ := bob.ListEnvelopes(ctx, th.ID, 0)
			if len(envs) > 0 && envs[0].Content.Body == "live?" {
				bobThread = th.ID
				return true
			}
		}
		return false
	})
	if err := bob.Send(ctx, bobThread, envelope.IntentSay, envelope.Content{
		Kind:  envelope.ContentDigest,
		Title: policy.CollabSyncTitle,
		Body:  "yes, live.",
	}); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	hostEventually(t, 4*time.Second, func() bool {
		got, err = collabAwaitHandler(alice)(ctx, map[string]any{
			"session_id":   sessionID,
			"wait_seconds": float64(0),
			"owner_id":     "agent-a",
		})
		return err == nil && got["new"] == 1
	})
	envelopes := got["envelopes"].([]map[string]any)
	content := envelopes[0]["content"].(map[string]any)
	if content["body"] != "yes, live." {
		t.Fatalf("unexpected peer reply: %+v", envelopes[0])
	}
}

func newHostTestNode(t *testing.T) *node.Node {
	t.Helper()
	n, err := node.New(node.Config{
		DataDir:  t.TempDir(),
		RelayURL: "ws://127.0.0.1:1",
		Alias:    "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func hostSpinRelay(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(relayserver.New(relayserver.Config{PairRendezvousTTL: 5 * time.Second}).Handler())
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func hostEventually(t *testing.T, timeout time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func addHostTestPeer(t *testing.T, n *node.Node, alias string) (identity.NodeID, *identity.Identity) {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	peer := pairing.Peer{
		NodeID:     id.SigningPublic,
		KexPub:     id.KexPublic,
		Alias:      alias,
		Trust:      pairing.TrustPaired,
		PairedAtMs: time.Now().UnixMilli(),
	}
	if err := n.Store().UpsertPeer(context.Background(), peer); err != nil {
		t.Fatal(err)
	}
	return peer.NodeID, id
}
