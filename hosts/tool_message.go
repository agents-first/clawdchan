package hosts

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/agents-first/ClawdChan/core/envelope"
	"github.com/agents-first/ClawdChan/core/identity"
	"github.com/agents-first/ClawdChan/core/node"
	"github.com/agents-first/ClawdChan/core/policy"
)

func messageSpec() ToolSpec {
	return ToolSpec{
		Name: "clawdchan_message",
		Description: "Send a message to a paired peer. Non-blocking — returns on relay ack, not on peer reply. " +
			"Thread is resolved automatically. Set as_human=true to submit with role=human (use ONLY for the " +
			"user's literal answer to a peer's standing ask_human; to decline, pass text \"[declined] <reason>\" " +
			"with as_human=true). Set collab=true only from inside a Task sub-agent running a live iterative " +
			"loop — it tags the envelope as a live-collab invite for the peer's daemon.",
		Params: []ParamSpec{
			{Name: "peer_id", Type: ParamString, Required: true, Description: "Hex node id, unique hex prefix (>=4), or exact alias."},
			{Name: "text", Type: ParamString, Required: true, Description: "Message body. Plain text. When as_human=true, the user's literal words — not your paraphrase."},
			{Name: "intent", Type: ParamString, Description: "Routing hint: say (default) | ask | notify_human | ask_human. Ignored when as_human=true (forced to say)."},
			{Name: "collab", Type: ParamBoolean, Description: "Sub-agent only: mark envelope as part of a live exchange. Do not set from the main agent."},
			{Name: "as_human", Type: ParamBoolean, Description: "True to submit with role=human for answering a peer's standing ask_human. Requires an unanswered ask_human from the peer — the reply is routed to that thread. Use ONLY with the user's literal words."},
		},
	}
}

func messageHandler(n *node.Node) Handler {
	return func(ctx context.Context, params map[string]any) (map[string]any, error) {
		peerStr, err := requireString(params, "peer_id")
		if err != nil {
			return nil, err
		}
		text, err := requireString(params, "text")
		if err != nil {
			return nil, err
		}
		peerID, err := ResolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return nil, err
		}

		if getBool(params, "as_human", false) {
			return sendAsHuman(ctx, n, peerID, text)
		}

		intent, err := ParseMessageIntent(getString(params, "intent", "say"))
		if err != nil {
			return nil, err
		}
		collab := getBool(params, "collab", false)
		tid, err := ResolveOrOpenThread(ctx, n, peerID)
		if err != nil {
			return nil, err
		}
		content := envelope.Content{Kind: envelope.ContentText, Text: text}
		if collab {
			// Wrap as ContentDigest with a reserved title. The peer's
			// inbox output shows the title explicitly, and the daemon
			// switches notification copy when it sees this marker —
			// so the receiver's side knows a live-collab sub-agent
			// is waiting.
			content = envelope.Content{
				Kind:  envelope.ContentDigest,
				Title: policy.CollabSyncTitle,
				Body:  text,
			}
		}
		if err := n.Send(ctx, tid, intent, content); err != nil {
			return nil, err
		}
		out := map[string]any{
			"ok":         true,
			"peer_id":    hex.EncodeToString(peerID[:]),
			"sent_at_ms": time.Now().UnixMilli(),
			"collab":     collab,
		}
		// Pending-ask awareness: if this peer has an unanswered
		// ask_human from us, warn. The agent should answer those
		// with as_human=true (user's literal words) rather than a
		// free-form agent-role message. Non-blocking hint — the
		// send already happened.
		if HasOpenAskHumanFromPeer(ctx, n, peerID) {
			out["pending_ask_hint"] = "This peer has an unanswered ask_human pending the user. If your text was meant as the user's answer, re-send with as_human=true. If it's an additional agent-level message, disregard this hint."
		}
		return out, nil
	}
}

// sendAsHuman is the merged reply+decline path. Requires a pending
// ask_human on some thread with peer — sending role=human without a
// standing ask would let the agent fabricate "the user said X"
// out of thin air, which is the exact failure mode we want to
// structurally prevent.
func sendAsHuman(ctx context.Context, n *node.Node, peer identity.NodeID, text string) (map[string]any, error) {
	tid, err := FindThreadWithPendingAsk(ctx, n, peer)
	if err != nil {
		return nil, err
	}
	if err := n.SubmitHumanReply(ctx, tid, envelope.Content{Kind: envelope.ContentText, Text: text}); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":         true,
		"peer_id":    hex.EncodeToString(peer[:]),
		"sent_at_ms": time.Now().UnixMilli(),
		"as_human":   true,
	}, nil
}
