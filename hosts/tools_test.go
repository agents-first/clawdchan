package hosts

import (
	"bytes"
	"testing"

	"github.com/agents-first/clawdchan/core/envelope"
	"github.com/agents-first/clawdchan/core/identity"
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
