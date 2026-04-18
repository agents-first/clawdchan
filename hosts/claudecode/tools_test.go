package claudecode

import (
	"testing"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
)

// TestRedactPendingHumanAsks verifies the hard ask_human enforcement: remote
// ask_human envelopes that have not received a local role=human reply get
// their content scrubbed before the agent can see them via poll/wait.
func TestRedactPendingHumanAsks(t *testing.T) {
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
	otherSay := mkEnv(2, peer, envelope.RoleAgent, envelope.IntentSay, "chitchat", 200)

	t.Run("unanswered ask_human is redacted", func(t *testing.T) {
		all := []envelope.Envelope{ask, otherSay}
		out, ids := redactPendingHumanAsks(all, all, me)
		if len(ids) != 1 {
			t.Fatalf("expected 1 pending id, got %d", len(ids))
		}
		if out[0].Content.Text == "secret question" {
			t.Fatalf("ask_human content leaked to poll: %q", out[0].Content.Text)
		}
		if out[1].Content.Text != "chitchat" {
			t.Fatalf("unrelated envelope was redacted: %q", out[1].Content.Text)
		}
	})

	t.Run("ask_human answered by local human is not redacted", func(t *testing.T) {
		reply := mkEnv(3, me, envelope.RoleHuman, envelope.IntentSay, "my answer", 300)
		all := []envelope.Envelope{ask, reply}
		out, ids := redactPendingHumanAsks(all, all, me)
		if len(ids) != 0 {
			t.Fatalf("expected 0 pending ids once answered, got %d", len(ids))
		}
		if out[0].Content.Text != "secret question" {
			t.Fatalf("answered ask_human was still redacted")
		}
	})

	t.Run("agent-role reply does not unlock the ask", func(t *testing.T) {
		// Crucially, a role=agent message from us must NOT count as a human reply.
		sneaky := mkEnv(4, me, envelope.RoleAgent, envelope.IntentSay, "i'll answer for them", 300)
		all := []envelope.Envelope{ask, sneaky}
		_, ids := redactPendingHumanAsks(all, all, me)
		if len(ids) != 1 {
			t.Fatalf("agent reply unexpectedly closed the ask; ids=%v", ids)
		}
	})

	t.Run("locally-originated ask_human is not redacted", func(t *testing.T) {
		myAsk := mkEnv(5, me, envelope.RoleAgent, envelope.IntentAskHuman, "for peer", 100)
		all := []envelope.Envelope{myAsk}
		_, ids := redactPendingHumanAsks(all, all, me)
		if len(ids) != 0 {
			t.Fatalf("our own outgoing ask_human should not be redacted from us: %v", ids)
		}
	})
}
