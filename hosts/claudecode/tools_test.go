package claudecode

import (
	"testing"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
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

	in := serializeEnvelope(plain, me, false)
	if in["direction"] != "in" || in["collab"] != false {
		t.Fatalf("plain peer envelope: direction=%v collab=%v", in["direction"], in["collab"])
	}
	if _, ok := in["content"].(map[string]any); !ok {
		t.Fatalf("full render should include content, got %v", in["content"])
	}

	out := serializeEnvelope(collab, me, false)
	if out["direction"] != "out" || out["collab"] != true {
		t.Fatalf("self collab envelope: direction=%v collab=%v", out["direction"], out["collab"])
	}

	hdr := serializeEnvelope(plain, me, true)
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
