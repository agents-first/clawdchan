// Package hosts holds the host-agnostic ClawdChan tool surface.
//
// Both the Claude Code stdio host (mark3labs/mcp-go) and the OpenClaw
// bridge host (gateway-protocol JSON) register the same four tools
// with the same argument shapes and return envelopes. This package is
// the single source of truth for:
//
//   - Primitives: peer-ref resolution, envelope serialization,
//     pending-ask detection, intent parsing (this file).
//   - Tool specs: ToolSpec/ParamSpec schema (spec.go), consumed by
//     each host's native metadata format.
//   - Tool handlers: toolkit / pair / message / inbox
//     (tool_*.go), returning JSON-ready map[string]any payloads.
//
// Host packages are thin adapters: they translate ToolSpec into their
// native tool-definition format and wrap Handler into their native
// handler signature. Adding or removing a tool is a single edit here,
// picked up by both hosts automatically.
package hosts

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/agents-first/clawdchan/core/envelope"
	"github.com/agents-first/clawdchan/core/identity"
	"github.com/agents-first/clawdchan/core/node"
	"github.com/agents-first/clawdchan/core/policy"
	"github.com/agents-first/clawdchan/core/store"
)

// --- peer resolution ---------------------------------------------------------

// ParseNodeID decodes a full 64-char hex node id into an identity.NodeID.
func ParseNodeID(s string) (identity.NodeID, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return identity.NodeID{}, fmt.Errorf("bad node id hex: %w", err)
	}
	if len(b) != len(identity.NodeID{}) {
		return identity.NodeID{}, fmt.Errorf("node id must be %d bytes", len(identity.NodeID{}))
	}
	var id identity.NodeID
	copy(id[:], b)
	return id, nil
}

// ResolvePeerRef accepts hex, a unique hex prefix (>=4), or an exact alias.
func ResolvePeerRef(ctx context.Context, n *node.Node, ref string) (identity.NodeID, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return identity.NodeID{}, fmt.Errorf("empty peer reference")
	}
	if len(ref) == 64 {
		if id, err := ParseNodeID(ref); err == nil {
			if _, err := n.GetPeer(ctx, id); err == nil {
				return id, nil
			}
			return identity.NodeID{}, fmt.Errorf("no paired peer with node_id %s", ref)
		}
	}

	peers, err := n.ListPeers(ctx)
	if err != nil {
		return identity.NodeID{}, err
	}

	var aliasMatches []identity.NodeID
	for _, p := range peers {
		if strings.EqualFold(p.Alias, ref) {
			aliasMatches = append(aliasMatches, p.NodeID)
		}
	}
	if len(aliasMatches) == 1 {
		return aliasMatches[0], nil
	}
	if len(aliasMatches) > 1 {
		return identity.NodeID{}, fmt.Errorf("alias %q is ambiguous (%d peers); pass a hex prefix or the full node_id", ref, len(aliasMatches))
	}

	lower := strings.ToLower(ref)
	if len(lower) >= 4 {
		var prefixMatches []identity.NodeID
		for _, p := range peers {
			if strings.HasPrefix(hex.EncodeToString(p.NodeID[:]), lower) {
				prefixMatches = append(prefixMatches, p.NodeID)
			}
		}
		if len(prefixMatches) == 1 {
			return prefixMatches[0], nil
		}
		if len(prefixMatches) > 1 {
			return identity.NodeID{}, fmt.Errorf("hex prefix %q is ambiguous (%d peers); use more characters", ref, len(prefixMatches))
		}
	}

	return identity.NodeID{}, fmt.Errorf("no peer matches %q — call clawdchan_toolkit to see paired peers", ref)
}

// ResolveOrOpenThread returns the most recent thread with peer, or
// opens a new one. Threads persist across sessions, so this yields
// one continuous conversation per peer by default.
func ResolveOrOpenThread(ctx context.Context, n *node.Node, peer identity.NodeID) (envelope.ThreadID, error) {
	threads, err := n.ListThreads(ctx)
	if err != nil {
		return envelope.ThreadID{}, err
	}
	var best store.Thread
	var found bool
	for _, t := range threads {
		if t.PeerID != peer {
			continue
		}
		if !found || t.CreatedMs > best.CreatedMs {
			best = t
			found = true
		}
	}
	if found {
		return best.ID, nil
	}
	return n.OpenThread(ctx, peer, "")
}

// FindThreadWithPendingAsk returns the most recent thread with peer
// that currently has an unanswered ask_human. Used by the as_human
// path on clawdchan_message to route the human reply to the right
// thread.
func FindThreadWithPendingAsk(ctx context.Context, n *node.Node, peer identity.NodeID) (envelope.ThreadID, error) {
	threads, err := n.ListThreads(ctx)
	if err != nil {
		return envelope.ThreadID{}, err
	}
	me := n.Identity()
	var best store.Thread
	var bestMs int64
	var found bool
	for _, t := range threads {
		if t.PeerID != peer {
			continue
		}
		envs, err := n.ListEnvelopes(ctx, t.ID, 0)
		if err != nil {
			continue
		}
		idx := PendingAsks(envs, me)
		for _, e := range envs {
			if !idx[e.EnvelopeID] {
				continue
			}
			if !found || e.CreatedAtMs > bestMs {
				best = t
				bestMs = e.CreatedAtMs
				found = true
			}
		}
	}
	if !found {
		return envelope.ThreadID{}, fmt.Errorf(
			"no pending ask_human from peer %s — as_human=true is only valid when the peer has a standing ask_human awaiting the user. For a free-form send, leave as_human unset",
			hex.EncodeToString(peer[:]))
	}
	return best.ID, nil
}

// HasOpenAskHumanFromPeer reports whether any thread with peer has
// an unanswered remote ask_human. Used by the message handler to
// warn when a free-form agent-role message might be misrouted.
func HasOpenAskHumanFromPeer(ctx context.Context, n *node.Node, peer identity.NodeID) bool {
	threads, err := n.ListThreads(ctx)
	if err != nil {
		return false
	}
	me := n.Identity()
	for _, t := range threads {
		if t.PeerID != peer {
			continue
		}
		envs, err := n.ListEnvelopes(ctx, t.ID, 0)
		if err != nil {
			continue
		}
		if len(PendingAsks(envs, me)) > 0 {
			return true
		}
	}
	return false
}

// PendingAsks returns the set of envelope IDs for remote ask_human
// envelopes that have not yet received a subsequent role=human reply
// from me. The relative order of envs is preserved, so a later
// human reply closes an earlier ask correctly.
func PendingAsks(envs []envelope.Envelope, me identity.NodeID) map[envelope.ULID]bool {
	out := map[envelope.ULID]bool{}
	for i, e := range envs {
		if e.Intent != envelope.IntentAskHuman {
			continue
		}
		if e.From.NodeID == me {
			continue
		}
		answered := false
		for j := i + 1; j < len(envs); j++ {
			if envs[j].From.Role == envelope.RoleHuman && envs[j].From.NodeID == me {
				answered = true
				break
			}
		}
		if !answered {
			out[e.EnvelopeID] = true
		}
	}
	return out
}

// SerializeEnvelope renders one stored envelope into the JSON shape
// agents see. Two derived fields save the agent work: direction
// ("in"/"out") and collab (true when the content carries the
// reserved CollabSyncTitle). headersOnly drops the content body for
// cheap polling over long threads. dedupeInBucket omits from_node
// and from_alias when the envelope lives inside a peer bucket that
// already carries peer_id and alias — saves ~25 tokens per envelope.
func SerializeEnvelope(e envelope.Envelope, me identity.NodeID, headersOnly, dedupeInBucket bool) map[string]any {
	dir := "in"
	if e.From.NodeID == me {
		dir = "out"
	}
	collab := e.Content.Kind == envelope.ContentDigest && e.Content.Title == policy.CollabSyncTitle
	out := map[string]any{
		"envelope_id":   hex.EncodeToString(e.EnvelopeID[:]),
		"from_role":     RoleName(e.From.Role),
		"intent":        IntentName(e.Intent),
		"created_at_ms": e.CreatedAtMs,
		"direction":     dir,
		"collab":        collab,
	}
	if !dedupeInBucket {
		out["from_node"] = hex.EncodeToString(e.From.NodeID[:])
		out["from_alias"] = e.From.Alias
	}
	if !headersOnly {
		out["content"] = ContentPayload(e.Content)
	}
	return out
}

// ContentPayload renders envelope content into the JSON shape agents
// see. Unknown kinds surface as {"kind":"unknown"} — the agent falls
// back to alias + intent headers.
func ContentPayload(c envelope.Content) map[string]any {
	switch c.Kind {
	case envelope.ContentText:
		return map[string]any{"kind": "text", "text": c.Text}
	case envelope.ContentDigest:
		return map[string]any{"kind": "digest", "title": c.Title, "body": c.Body}
	default:
		return map[string]any{"kind": "unknown"}
	}
}

// RoleName is the stable string name for an envelope role.
func RoleName(r envelope.Role) string {
	if r == envelope.RoleHuman {
		return "human"
	}
	return "agent"
}

// TrustName is the stable string name for a peer trust level.
func TrustName(t uint8) string {
	switch t {
	case 1:
		return "paired"
	case 2:
		return "bridged"
	case 3:
		return "revoked"
	default:
		return "unknown"
	}
}

// IntentName is the stable string name for an envelope intent, used
// in serialized envelopes and notification copy.
func IntentName(i envelope.Intent) string {
	switch i {
	case envelope.IntentSay:
		return "say"
	case envelope.IntentAsk:
		return "ask"
	case envelope.IntentNotifyHuman:
		return "notify_human"
	case envelope.IntentAskHuman:
		return "ask_human"
	case envelope.IntentHandoff:
		return "handoff"
	case envelope.IntentAck:
		return "ack"
	case envelope.IntentClose:
		return "close"
	default:
		return fmt.Sprintf("intent_%d", i)
	}
}

// ParseMessageIntent converts a user-supplied intent string into the
// restricted set exposed to Claude. handoff / ack / close are
// deliberately not accepted via the tool surface — they'd be
// confusing for the agent to reason about; the node uses them
// internally.
func ParseMessageIntent(s string) (envelope.Intent, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "say":
		return envelope.IntentSay, nil
	case "ask":
		return envelope.IntentAsk, nil
	case "notify_human", "notify-human":
		return envelope.IntentNotifyHuman, nil
	case "ask_human", "ask-human":
		return envelope.IntentAskHuman, nil
	default:
		return 0, fmt.Errorf("unknown or unsupported intent %q (use say|ask|notify_human|ask_human)", s)
	}
}
