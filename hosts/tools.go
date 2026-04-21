package hosts

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/policy"
	"github.com/vMaroon/ClawdChan/core/store"
)

// MaxInboxWaitSeconds caps how long a single inbox call can block. Anything
// longer than ~15s fights with MCP call timeouts and with the user's
// "why is Claude idle" impression. For tighter loops, use clawdchan_await.
const MaxInboxWaitSeconds = 15

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

	return identity.NodeID{}, fmt.Errorf("no peer matches %q — use clawdchan_peers to see paired peers", ref)
}

// ResolveOrOpenThread returns the most recent thread with the peer, or opens
// a new one if none exists. Threads are persisted across sessions, so this
// yields one continuous conversation per peer by default.
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

// FindThreadWithPendingAsk returns the thread containing the most recent
// unanswered ask_human from peer, or an error if none exists.
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
			"no pending ask_human from peer %s — clawdchan_reply / clawdchan_decline "+
				"are only for answering a peer's ask_human. For free-form messages to the peer, use clawdchan_message",
			hex.EncodeToString(peer[:]))
	}
	return best.ID, nil
}

// HasOpenAskHumanFromPeer reports whether any thread with peer has an
// unanswered ask_human.
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

// --- envelope helpers --------------------------------------------------------

// PendingAsks returns the set of envelope ids for remote ask_human envelopes
// that have not yet received a subsequent role=human reply from me. The
// relative order of envelopes in the slice is preserved, so a later human
// reply closes an earlier ask correctly.
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

// SerializeEnvelope renders one stored envelope into the JSON shape agents
// see. Two derived fields save the agent work: direction ("in"/"out") and
// collab (true when the content carries the reserved CollabSyncTitle).
// headersOnly drops the content body for cheap polling over long threads.
func SerializeEnvelope(e envelope.Envelope, me identity.NodeID, headersOnly bool) map[string]any {
	dir := "in"
	if e.From.NodeID == me {
		dir = "out"
	}
	collab := e.Content.Kind == envelope.ContentDigest && e.Content.Title == policy.CollabSyncTitle
	out := map[string]any{
		"envelope_id":   hex.EncodeToString(e.EnvelopeID[:]),
		"from_node":     hex.EncodeToString(e.From.NodeID[:]),
		"from_alias":    e.From.Alias,
		"from_role":     RoleName(e.From.Role),
		"intent":        IntentName(e.Intent),
		"created_at_ms": e.CreatedAtMs,
		"direction":     dir,
		"collab":        collab,
	}
	if !headersOnly {
		out["content"] = ContentPayload(e.Content)
	}
	return out
}

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

func RoleName(r envelope.Role) string {
	if r == envelope.RoleHuman {
		return "human"
	}
	return "agent"
}

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

// --- peers list --------------------------------------------------------------

// BuildPeersList returns the per-peer summary slice used by clawdchan_peers.
func BuildPeersList(ctx context.Context, n *node.Node) ([]map[string]any, error) {
	peers, err := n.ListPeers(ctx)
	if err != nil {
		return nil, err
	}
	threads, err := n.ListThreads(ctx)
	if err != nil {
		return nil, err
	}
	me := n.Identity()
	stats := map[identity.NodeID]struct {
		inbound      int
		pending      int
		lastActivity int64
	}{}
	for _, t := range threads {
		envs, err := n.ListEnvelopes(ctx, t.ID, 0)
		if err != nil {
			continue
		}
		pending := PendingAsks(envs, me)
		s := stats[t.PeerID]
		for _, e := range envs {
			if e.From.NodeID != me {
				s.inbound++
			}
			if e.CreatedAtMs > s.lastActivity {
				s.lastActivity = e.CreatedAtMs
			}
		}
		s.pending += len(pending)
		stats[t.PeerID] = s
	}
	out := make([]map[string]any, 0, len(peers))
	for _, p := range peers {
		s := stats[p.NodeID]
		out = append(out, map[string]any{
			"node_id":          hex.EncodeToString(p.NodeID[:]),
			"alias":            p.Alias,
			"trust":            TrustName(uint8(p.Trust)),
			"human_reachable":  p.HumanReachable,
			"paired_at_ms":     p.PairedAtMs,
			"sas":              strings.Join(p.SAS[:], "-"),
			"inbound_count":    s.inbound,
			"pending_asks":     s.pending,
			"last_activity_ms": s.lastActivity,
		})
	}
	return out, nil
}

// --- toolkit base ------------------------------------------------------------

// BuildToolkitBase builds the toolkit payload shared by all hosts. setup is
// the host-specific listener/readiness block.
func BuildToolkitBase(n *node.Node, setup map[string]any) map[string]any {
	id := n.Identity()
	return map[string]any{
		"version": "0.4",
		"self": map[string]any{
			"node_id": hex.EncodeToString(id[:]),
			"alias":   n.Alias(),
			"relay":   n.RelayURL(),
		},
		"setup": setup,
		"peer_refs": "Anywhere you need a peer_id, pass hex, a unique hex prefix (>=4), or an exact alias. " +
			"'alice' resolves if exactly one peer carries that alias; '19466' resolves if exactly one node id starts with those chars.",
		"intents": []map[string]string{
			{"name": "say", "desc": "Agent→agent FYI, no reply expected (default)."},
			{"name": "ask", "desc": "Agent→agent, peer's AGENT is expected to reply."},
			{"name": "notify_human", "desc": "Agent→peer's HUMAN, FYI, no reply expected."},
			{"name": "ask_human", "desc": "Agent→peer's HUMAN specifically; the peer's agent is forbidden from replying."},
		},
		"behavior_guide": "Conduct rules (send and end the turn; surface mnemonics verbatim; never answer ask_human; delegate live loops to a Task sub-agent) are in /clawdchan and in CLAWDCHAN_GUIDE.md. Don't re-derive them from the inbox shape.",
	}
}

// --- inbox -------------------------------------------------------------------

// CollectInbox assembles the grouped-by-peer inbox view. Returns the peer
// buckets, whether any traffic or pending asks exist, whether any collab
// envelope is present, and the current timestamp.
func CollectInbox(ctx context.Context, n *node.Node, since int64, headersOnly bool) ([]map[string]any, bool, bool, bool, int64, error) {
	threads, err := n.ListThreads(ctx)
	if err != nil {
		return nil, false, false, false, 0, err
	}
	me := n.Identity()

	type bucket struct {
		envelopes    []map[string]any
		pendingAsks  []map[string]any
		lastActivity int64
	}
	buckets := map[identity.NodeID]*bucket{}
	hasPending, hasCollab := false, false

	for _, t := range threads {
		envs, err := n.ListEnvelopes(ctx, t.ID, 0)
		if err != nil {
			continue
		}
		pending := PendingAsks(envs, me)
		b := buckets[t.PeerID]
		if b == nil {
			b = &bucket{}
			buckets[t.PeerID] = b
		}
		for _, e := range envs {
			if e.CreatedAtMs > b.lastActivity {
				b.lastActivity = e.CreatedAtMs
			}
			if pending[e.EnvelopeID] {
				b.pendingAsks = append(b.pendingAsks, SerializeEnvelope(e, me, false))
				hasPending = true
				continue
			}
			if e.CreatedAtMs > since {
				rendered := SerializeEnvelope(e, me, headersOnly)
				if c, ok := rendered["collab"].(bool); ok && c {
					hasCollab = true
				}
				b.envelopes = append(b.envelopes, rendered)
			}
		}
	}

	peers, _ := n.ListPeers(ctx)
	aliasByID := map[identity.NodeID]string{}
	for _, p := range peers {
		aliasByID[p.NodeID] = p.Alias
	}
	out := make([]map[string]any, 0, len(buckets))
	haveAny := false
	for pid, b := range buckets {
		if len(b.envelopes) == 0 && len(b.pendingAsks) == 0 {
			continue
		}
		haveAny = true
		out = append(out, map[string]any{
			"peer_id":          hex.EncodeToString(pid[:]),
			"alias":            aliasByID[pid],
			"envelopes":        b.envelopes,
			"pending_asks":     b.pendingAsks,
			"last_activity_ms": b.lastActivity,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		ai, _ := out[i]["last_activity_ms"].(int64)
		aj, _ := out[j]["last_activity_ms"].(int64)
		return ai > aj
	})
	return out, haveAny, hasPending, hasCollab, time.Now().UnixMilli(), nil
}

// InboxNotes returns contextually relevant agent notes for the inbox response.
// Keeping guidance conditional prevents the agent from re-reading the same
// reminders on every poll.
func InboxNotes(hasPending, hasCollab bool) []string {
	var notes []string
	if hasPending {
		notes = append(notes, "pending_asks carry the peer's ask_human verbatim. Present to the user, then clawdchan_reply with their literal words or clawdchan_decline. Do not compose an answer yourself.")
	}
	if hasCollab {
		notes = append(notes, "Envelopes with collab=true are part of a live agent-to-agent exchange. If direction='in' and you didn't initiate, the peer has a sub-agent waiting. If their side has no dispatcher, ask the user whether to engage live or reply at their own pace.")
	}
	return notes
}

// --- await -------------------------------------------------------------------

// BuildAwaitPayload serializes fresh envelopes for a subagent_await response,
// redacting unanswered ask_human entries. Returns the serialized list and the
// IDs of any redacted pending asks.
func BuildAwaitPayload(ctx context.Context, n *node.Node, tid envelope.ThreadID, envs []envelope.Envelope) (serialized []map[string]any, pendingIDs []string) {
	all, _ := n.ListEnvelopes(ctx, tid, 0)
	me := n.Identity()
	pending := PendingAsks(all, me)
	for _, e := range envs {
		if pending[e.EnvelopeID] {
			pendingIDs = append(pendingIDs, hex.EncodeToString(e.EnvelopeID[:]))
			stub := SerializeEnvelope(e, me, false)
			stub["content"] = map[string]any{
				"kind": "text",
				"text": "[redacted: ask_human awaiting human reply; use clawdchan_inbox then clawdchan_reply/clawdchan_decline]",
			}
			serialized = append(serialized, stub)
			continue
		}
		serialized = append(serialized, SerializeEnvelope(e, me, false))
	}
	return
}
