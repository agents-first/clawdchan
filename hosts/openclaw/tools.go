package openclaw

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/policy"
	"github.com/vMaroon/ClawdChan/core/store"
	"github.com/vMaroon/ClawdChan/internal/listenerreg"
)

// ToolHandler is the handler signature for OpenClaw-hosted ClawdChan tools.
// params is the decoded JSON object from the tool call; returns a JSON string
// sent back into the session, or an error surfaced as a tool error response.
type ToolHandler func(ctx context.Context, params map[string]any) (string, error)

// RegisterTools registers the ClawdChan tool surface on br, bound to n.
//
// Unlike the Claude Code MCP host (stdio JSON-RPC), the OpenClaw host
// receives tool invocations as structured JSON over the Gateway Protocol
// and dispatches them via Bridge.RegisterTool.
func RegisterTools(br *Bridge, n *node.Node, opts ...Option) {
	cfg := regOpts{}
	for _, opt := range opts {
		opt(&cfg)
	}
	br.RegisterTool("clawdchan_toolkit", toolkitHandler(n, &cfg))
	br.RegisterTool("clawdchan_whoami", whoamiHandler(n))
	br.RegisterTool("clawdchan_peers", peersHandler(n))
	br.RegisterTool("clawdchan_pair", pairHandler(n))
	br.RegisterTool("clawdchan_consume", consumeHandler(n))
	br.RegisterTool("clawdchan_message", messageHandler(n))
	br.RegisterTool("clawdchan_inbox", inboxHandler(n))
	br.RegisterTool("clawdchan_subagent_await", awaitHandler(n))
	br.RegisterTool("clawdchan_reply", replyHandler(n))
	br.RegisterTool("clawdchan_decline", declineHandler(n))
	br.RegisterTool("clawdchan_peer_rename", peerRenameHandler(n))
	br.RegisterTool("clawdchan_peer_revoke", peerRevokeHandler(n))
	br.RegisterTool("clawdchan_peer_remove", peerRemoveHandler(n))
}

// Option tunes RegisterTools. The only option so far is WithDispatchEnabled;
// it's a variadic slot now so callers don't break when we add more.
type Option func(*regOpts)

type regOpts struct {
	dispatchEnabled bool
}

// WithDispatchEnabled tells the toolkit to report that the local daemon
// has agent-dispatch configured. The MCP binary reads this from the same
// config.json the daemon reads. When enabled, incoming collab=true asks
// will be auto-answered by the configured subprocess — the agent can set
// its expectations about conversation cadence accordingly, and knows
// that some outbound envelopes it finds in its own inbox may have been
// dispatcher-produced rather than sent by this session's agent.
func WithDispatchEnabled(enabled bool) Option {
	return func(o *regOpts) { o.dispatchEnabled = enabled }
}

// --- toolkit ----------------------------------------------------------------

func toolkitHandler(n *node.Node, opts *regOpts) ToolHandler {
	return func(ctx context.Context, _ map[string]any) (string, error) {
		id := n.Identity()
		setup := buildSetupStatus(n)

		// Dispatch awareness. When the local daemon has agent-dispatch
		// configured, inbound collab=true asks are auto-answered by a
		// subprocess rather than waiting for the human — and that fact
		// shapes how the agent should describe cadence and attribute
		// outbound envelopes it didn't itself send this session.
		dispatch := map[string]any{"enabled": opts != nil && opts.dispatchEnabled}
		if opts != nil && opts.dispatchEnabled {
			dispatch["note"] = "Incoming collab=true asks are auto-answered at agent speed by a local subprocess. If you see direction=out collab=true envelopes in a thread that you don't remember sending this session, your dispatcher handled them while the user was away — describe them that way, not as something you said."
		}

		return jsonResult(map[string]any{
			"version": "0.4",
			"self": map[string]any{
				"node_id": hex.EncodeToString(id[:]),
				"alias":   n.Alias(),
				"relay":   n.RelayURL(),
			},
			"setup":    setup,
			"dispatch": dispatch,
			"peer_refs": "Anywhere you need a peer_id, pass hex, a unique hex prefix (>=4), or an exact alias. " +
				"'alice' resolves if exactly one peer carries that alias; '19466' resolves if exactly one node id starts with those chars.",
			"intents": []map[string]string{
				{"name": "say", "desc": "Agent→agent FYI, no reply expected (default)."},
				{"name": "ask", "desc": "Agent→agent, peer's AGENT is expected to reply."},
				{"name": "notify_human", "desc": "Agent→peer's HUMAN, FYI, no reply expected."},
				{"name": "ask_human", "desc": "Agent→peer's HUMAN specifically; the peer's agent is forbidden from replying."},
			},
			"behavior_guide": "Conduct rules (send and end the turn; surface mnemonics verbatim; never answer ask_human; delegate live loops to a Task sub-agent) are in /clawdchan and in CLAWDCHAN_GUIDE.md. Don't re-derive them from the inbox shape.",
		})
	}
}

// --- setup ------------------------------------------------------------------

// buildSetupStatus inspects the listener registry and returns a structured
// blob. In OpenClaw host mode the daemon always owns the relay link —
// the agent doesn't need to start anything.
func buildSetupStatus(n *node.Node) map[string]any {
	entries, err := listenerreg.List(n.DataDir())
	if err != nil {
		return map[string]any{
			"error":                      err.Error(),
			"openclaw_host":              true,
			"needs_persistent_listener":  false,
			"persistent_listener_active": false,
			"user_message":               "Couldn't read the listener registry. Running inside OpenClaw host usually means the daemon owns the relay link, but listener status is currently unknown.",
		}
	}

	nid := n.Identity()
	me := hex.EncodeToString(nid[:])

	var hasCLI bool
	listeners := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		if !strings.EqualFold(e.NodeID, me) {
			continue
		}
		listeners = append(listeners, map[string]any{
			"pid":        e.PID,
			"kind":       string(e.Kind),
			"started_ms": e.StartedMs,
			"relay":      e.RelayURL,
			"alias":      e.Alias,
		})
		if e.Kind == listenerreg.KindCLI {
			hasCLI = true
		}
	}

	userMsg := "Running inside OpenClaw host — daemon owns the relay link and this session. Nothing to set up."
	if !hasCLI {
		userMsg = "Warning: no CLI daemon found in listener registry. The OpenClaw bridge is active but the relay link may not be running."
	}

	return map[string]any{
		"openclaw_host":              true,
		"persistent_listener_active": hasCLI,
		"needs_persistent_listener":  false,
		"listeners":                  listeners,
		"user_message":               userMsg,
	}
}

// --- whoami -----------------------------------------------------------------

func whoamiHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, _ map[string]any) (string, error) {
		id := n.Identity()
		return jsonResult(map[string]any{
			"node_id": hex.EncodeToString(id[:]),
			"alias":   n.Alias(),
		})
	}
}

// --- peers ------------------------------------------------------------------

func peersHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, _ map[string]any) (string, error) {
		peers, err := n.ListPeers(ctx)
		if err != nil {
			return "", err
		}
		threads, err := n.ListThreads(ctx)
		if err != nil {
			return "", err
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
			pending := pendingAsks(envs, me)
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
				"trust":            trustName(uint8(p.Trust)),
				"human_reachable":  p.HumanReachable,
				"paired_at_ms":     p.PairedAtMs,
				"sas":              strings.Join(p.SAS[:], "-"),
				"inbound_count":    s.inbound,
				"pending_asks":     s.pending,
				"last_activity_ms": s.lastActivity,
			})
		}
		return jsonResult(map[string]any{"peers": out})
	}
}

// --- message ----------------------------------------------------------------

func messageHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, params map[string]any) (string, error) {
		peerStr, err := requireString(params, "peer_id")
		if err != nil {
			return "", err
		}
		text, err := requireString(params, "text")
		if err != nil {
			return "", err
		}
		peerID, err := resolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return "", err
		}
		intent, err := parseMessageIntent(getString(params, "intent", "say"))
		if err != nil {
			return "", err
		}
		collab := getBool(params, "collab", false)
		tid, err := resolveOrOpenThread(ctx, n, peerID)
		if err != nil {
			return "", err
		}
		content := envelope.Content{Kind: envelope.ContentText, Text: text}
		if collab {
			// Wrap as ContentDigest with a reserved title. The peer's inbox
			// output shows the title explicitly, and the daemon switches
			// notification copy when it sees this marker — so the receiver's
			// side knows a live-collab sub-agent is waiting.
			content = envelope.Content{
				Kind:  envelope.ContentDigest,
				Title: policy.CollabSyncTitle,
				Body:  text,
			}
		}
		if err := n.Send(ctx, tid, intent, content); err != nil {
			return "", err
		}
		out := map[string]any{
			"ok":         true,
			"peer_id":    hex.EncodeToString(peerID[:]),
			"sent_at_ms": time.Now().UnixMilli(),
			"collab":     collab,
		}
		if hasOpenAskHumanFromPeer(ctx, n, peerID) {
			out["pending_ask_hint"] = "This peer has an unanswered ask_human pending the user. If your text was meant as the user's answer, use clawdchan_reply instead of clawdchan_message. If it's an additional message for the peer's agent, disregard this hint."
		}
		return jsonResult(out)
	}
}

// hasOpenAskHumanFromPeer reports whether any thread with peer has a
// remote ask_human that has not yet received a role=human reply from us.
// Used by messageHandler to warn when a free-form message might be
// misrouted (the agent should use clawdchan_reply for the user's answer).
func hasOpenAskHumanFromPeer(ctx context.Context, n *node.Node, peer identity.NodeID) bool {
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
		if len(pendingAsks(envs, me)) > 0 {
			return true
		}
	}
	return false
}

// resolveOrOpenThread returns the most recent thread with the peer, or opens
// a new one if none exists. Threads are persisted across sessions, so this
// yields one continuous conversation per peer by default.
func resolveOrOpenThread(ctx context.Context, n *node.Node, peer identity.NodeID) (envelope.ThreadID, error) {
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

// --- inbox ------------------------------------------------------------------

// maxInboxWaitSeconds caps how long a single inbox call can block.
const maxInboxWaitSeconds = 15

func inboxHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, params map[string]any) (string, error) {
		since := int64(getFloat(params, "since_ms", 0))
		wait := getFloat(params, "wait_seconds", 0)
		if wait < 0 {
			wait = 0
		}
		if wait > maxInboxWaitSeconds {
			wait = maxInboxWaitSeconds
		}
		headersOnly := strings.EqualFold(strings.TrimSpace(getString(params, "include", "full")), "headers")
		notesSeen := getBool(params, "notes_seen", false)

		deadline := time.Now().Add(time.Duration(wait * float64(time.Second)))
		const pollInterval = 400 * time.Millisecond
		for {
			out, anyTraffic, hasPending, hasCollab, nowMs, err := collectInbox(ctx, n, since, headersOnly)
			if err != nil {
				return "", err
			}
			if anyTraffic || wait == 0 || !time.Now().Before(deadline) {
				resp := map[string]any{
					"now_ms": nowMs,
					"peers":  out,
				}
				if !notesSeen {
					resp["notes"] = inboxNotes(hasPending, hasCollab)
				}
				return jsonResult(resp)
			}
			select {
			case <-ctx.Done():
				return jsonResult(map[string]any{
					"now_ms":    time.Now().UnixMilli(),
					"peers":     []any{},
					"cancelled": true,
				})
			case <-time.After(pollInterval):
			}
		}
	}
}

// inboxNotes fires a note only when it's relevant to the response payload.
// Keeps the guidance dense and stops the agent from re-reading the same
// four reminders on every poll.
func inboxNotes(hasPending, hasCollab bool) []string {
	var notes []string
	if hasPending {
		notes = append(notes, "pending_asks carry the peer's ask_human verbatim. Present to the user, then clawdchan_reply with their literal words or clawdchan_decline. Do not compose an answer yourself.")
	}
	if hasCollab {
		notes = append(notes, "Envelopes with collab=true are part of a live agent-to-agent exchange. If direction='in' and you didn't initiate, the peer has a sub-agent waiting. If their side has no dispatcher, ask the user whether to engage live or reply at their own pace.")
	}
	return notes
}

// collectInbox assembles the grouped-by-peer inbox view. Also returns
// whether any pending_asks are present and whether any visible envelope
// carries the collab marker, so the caller can attach only the notes
// that are contextually relevant.
func collectInbox(ctx context.Context, n *node.Node, since int64, headersOnly bool) ([]map[string]any, bool, bool, bool, int64, error) {
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
		pending := pendingAsks(envs, me)
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
				b.pendingAsks = append(b.pendingAsks, serializeEnvelope(e, me, false))
				hasPending = true
				continue
			}
			if e.CreatedAtMs > since {
				rendered := serializeEnvelope(e, me, headersOnly)
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

// --- subagent_await (live collab primitive) -------------------------------

// awaitTool is the blocking primitive for live agent-to-agent loops.
// Its MCP name is deliberately prefixed `clawdchan_subagent_` so every
// tool listing carries the scope constraint — this tool freezes the
// user-facing turn when called from the main agent, which is almost
// never what you want.
func awaitHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, params map[string]any) (string, error) {
		peerStr, err := requireString(params, "peer_id")
		if err != nil {
			return "", err
		}
		peerID, err := resolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return "", err
		}
		timeout := getFloat(params, "timeout_seconds", 10)
		if timeout < 1 {
			timeout = 1
		}
		if timeout > 60 {
			timeout = 60
		}
		since := int64(getFloat(params, "since_ms", 0))

		tid, err := resolveOrOpenThread(ctx, n, peerID)
		if err != nil {
			return "", err
		}
		me := n.Identity()

		deadline := time.Now().Add(time.Duration(timeout * float64(time.Second)))
		// Poll the store directly — the daemon owns inbound, so SQLite
		// polling covers both in-process and cross-process cases.
		const pollInterval = 500 * time.Millisecond
		for {
			envs, err := n.ListEnvelopes(ctx, tid, since)
			if err != nil {
				return "", err
			}
			var fresh []envelope.Envelope
			for _, e := range envs {
				if e.From.NodeID != me {
					fresh = append(fresh, e)
				}
			}
			if len(fresh) > 0 {
				return awaitResult(ctx, n, tid, fresh)
			}
			if time.Now().After(deadline) {
				return jsonResult(map[string]any{
					"envelopes": []any{},
					"timeout":   true,
					"now_ms":    time.Now().UnixMilli(),
				})
			}
			select {
			case <-ctx.Done():
				return jsonResult(map[string]any{
					"envelopes": []any{},
					"cancelled": true,
					"now_ms":    time.Now().UnixMilli(),
				})
			case <-time.After(pollInterval):
			}
		}
	}
}

// awaitResult redacts unanswered remote ask_human envelopes the same way
// the inbox does — the agent (even a sub-agent running a collab loop) must
// not answer for the human.
func awaitResult(ctx context.Context, n *node.Node, tid envelope.ThreadID, envs []envelope.Envelope) (string, error) {
	all, _ := n.ListEnvelopes(ctx, tid, 0)
	me := n.Identity()
	pending := pendingAsks(all, me)
	serialized := make([]map[string]any, 0, len(envs))
	var pendingIDs []string
	for _, e := range envs {
		if pending[e.EnvelopeID] {
			pendingIDs = append(pendingIDs, hex.EncodeToString(e.EnvelopeID[:]))
			stub := serializeEnvelope(e, me, false)
			stub["content"] = map[string]any{
				"kind": "text",
				"text": "[redacted: ask_human awaiting human reply; use clawdchan_inbox then clawdchan_reply/clawdchan_decline]",
			}
			serialized = append(serialized, stub)
			continue
		}
		serialized = append(serialized, serializeEnvelope(e, me, false))
	}
	out := map[string]any{
		"envelopes": serialized,
		"now_ms":    time.Now().UnixMilli(),
	}
	if len(pendingIDs) > 0 {
		out["pending_human_asks"] = pendingIDs
		out["notice"] = "One or more ask_human envelopes are pending a human reply. Do not answer them yourself."
	}
	return jsonResult(out)
}

// --- reply / decline --------------------------------------------------------

func replyHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, params map[string]any) (string, error) {
		peerStr, err := requireString(params, "peer_id")
		if err != nil {
			return "", err
		}
		text, err := requireString(params, "text")
		if err != nil {
			return "", err
		}
		peerID, err := resolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return "", err
		}
		tid, err := findThreadWithPendingAsk(ctx, n, peerID)
		if err != nil {
			return "", err
		}
		if err := n.SubmitHumanReply(ctx, tid, envelope.Content{Kind: envelope.ContentText, Text: text}); err != nil {
			return "", err
		}
		return jsonResult(map[string]any{"ok": true})
	}
}

func declineHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, params map[string]any) (string, error) {
		peerStr, err := requireString(params, "peer_id")
		if err != nil {
			return "", err
		}
		reason := getString(params, "reason", "declined by user")
		peerID, err := resolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return "", err
		}
		tid, err := findThreadWithPendingAsk(ctx, n, peerID)
		if err != nil {
			return "", err
		}
		if err := n.SubmitHumanReply(ctx, tid, envelope.Content{Kind: envelope.ContentText, Text: "[declined] " + reason}); err != nil {
			return "", err
		}
		return jsonResult(map[string]any{"ok": true})
	}
}

func findThreadWithPendingAsk(ctx context.Context, n *node.Node, peer identity.NodeID) (envelope.ThreadID, error) {
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
		idx := pendingAsks(envs, me)
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

// pendingAsks returns the set of envelope ids for remote ask_human envelopes
// that have not yet received a subsequent role=human reply from me. The
// relative order of envelopes in the slice is preserved, so a later human
// reply closes an earlier ask correctly.
func pendingAsks(envs []envelope.Envelope, me identity.NodeID) map[envelope.ULID]bool {
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

// --- pair / consume ---------------------------------------------------------

func pairHandler(n *node.Node) ToolHandler {
	return func(_ context.Context, params map[string]any) (string, error) {
		timeout := time.Duration(getFloat(params, "timeout_seconds", 300)) * time.Second
		// Pairing intentionally outlives a single tool-call context. Bound it by
		// timeout and completion channel so host shutdown still terminates it.
		pairCtx, cancel := context.WithTimeout(context.Background(), timeout)
		code, ch, err := n.Pair(pairCtx)
		if err != nil {
			cancel()
			return "", err
		}
		go func() {
			defer cancel()
			<-ch
		}()
		return jsonResult(map[string]any{
			"mnemonic":        code.Mnemonic(),
			"status":          "pending_peer_consume",
			"timeout_seconds": int(timeout.Seconds()),
		})
	}
}

func consumeHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, params map[string]any) (string, error) {
		m, err := requireString(params, "mnemonic")
		if err != nil {
			return "", err
		}
		peer, err := n.Consume(ctx, m)
		if err != nil {
			return "", err
		}
		return jsonResult(map[string]any{
			"peer": map[string]any{
				"node_id":         hex.EncodeToString(peer.NodeID[:]),
				"alias":           peer.Alias,
				"human_reachable": peer.HumanReachable,
				"sas":             strings.Join(peer.SAS[:], "-"),
			},
		})
	}
}

// --- peer management (rename / revoke / remove) ---------------------------

func peerRenameHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, params map[string]any) (string, error) {
		ref, err := requireString(params, "peer_id")
		if err != nil {
			return "", err
		}
		alias, err := requireString(params, "alias")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(alias) == "" {
			return "", fmt.Errorf("alias cannot be empty")
		}
		peerID, err := resolvePeerRef(ctx, n, ref)
		if err != nil {
			return "", err
		}
		before, _ := n.GetPeer(ctx, peerID)
		if err := n.SetPeerAlias(ctx, peerID, alias); err != nil {
			return "", err
		}
		return jsonResult(map[string]any{
			"ok":       true,
			"peer_id":  hex.EncodeToString(peerID[:]),
			"previous": before.Alias,
			"alias":    alias,
		})
	}
}

func peerRevokeHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, params map[string]any) (string, error) {
		ref, err := requireString(params, "peer_id")
		if err != nil {
			return "", err
		}
		peerID, err := resolvePeerRef(ctx, n, ref)
		if err != nil {
			return "", err
		}
		p, _ := n.GetPeer(ctx, peerID)
		if err := n.RevokePeer(ctx, peerID); err != nil {
			return "", err
		}
		return jsonResult(map[string]any{
			"ok":      true,
			"peer_id": hex.EncodeToString(peerID[:]),
			"alias":   p.Alias,
			"note":    "trust=revoked. Inbound dropped; history preserved. Use clawdchan_peer_remove for a hard delete.",
		})
	}
}

func peerRemoveHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, params map[string]any) (string, error) {
		ref, err := requireString(params, "peer_id")
		if err != nil {
			return "", err
		}
		confirmed := getBool(params, "confirmed", false)
		if !confirmed {
			return "", fmt.Errorf("refusing to delete without confirmed=true. Ask the user explicitly and only then retry with confirmed=true")
		}
		peerID, err := resolvePeerRef(ctx, n, ref)
		if err != nil {
			return "", err
		}
		p, _ := n.GetPeer(ctx, peerID)
		if err := n.DeletePeer(ctx, peerID); err != nil {
			return "", err
		}
		return jsonResult(map[string]any{
			"ok":      true,
			"peer_id": hex.EncodeToString(peerID[:]),
			"alias":   p.Alias,
			"note":    "hard-deleted. All threads, envelopes, outbox entries for this peer are gone. Pairing would require a fresh mnemonic.",
		})
	}
}

// --- parsing / serialization helpers ---------------------------------------

func parseNodeID(s string) (identity.NodeID, error) {
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

// resolvePeerRef accepts any of: full 64-char hex node id, a unique hex
// prefix (>=4), or an exact (case-insensitive) alias match. Returns the
// resolved NodeID or a descriptive error. This is what peer-taking tools
// use so Claude can pass "bruce" from user speech instead of 64 chars of
// hex the model has to carry in context.
func resolvePeerRef(ctx context.Context, n *node.Node, ref string) (identity.NodeID, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return identity.NodeID{}, fmt.Errorf("empty peer reference")
	}
	if len(ref) == 64 {
		if id, err := parseNodeID(ref); err == nil {
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

func parseMessageIntent(s string) (envelope.Intent, error) {
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

func trustName(t uint8) string {
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

func roleName(r envelope.Role) string {
	if r == envelope.RoleHuman {
		return "human"
	}
	return "agent"
}

func contentPayload(c envelope.Content) map[string]any {
	switch c.Kind {
	case envelope.ContentText:
		return map[string]any{"kind": "text", "text": c.Text}
	case envelope.ContentDigest:
		return map[string]any{"kind": "digest", "title": c.Title, "body": c.Body}
	default:
		return map[string]any{"kind": "unknown"}
	}
}

// serializeEnvelope renders one stored envelope into the JSON shape Claude
// sees. Two derived fields save the agent work: direction ("in" for peer-
// origin, "out" for local-origin — no hex compare needed) and collab
// (true when the envelope is part of a live agent-to-agent exchange, i.e.
// Content.Title is the reserved CollabSyncTitle — no title pattern-match
// needed). headersOnly drops the content body for cheap polling over
// long threads.
func serializeEnvelope(e envelope.Envelope, me identity.NodeID, headersOnly bool) map[string]any {
	dir := "in"
	if e.From.NodeID == me {
		dir = "out"
	}
	collab := e.Content.Kind == envelope.ContentDigest && e.Content.Title == policy.CollabSyncTitle
	out := map[string]any{
		"envelope_id":   hex.EncodeToString(e.EnvelopeID[:]),
		"from_node":     hex.EncodeToString(e.From.NodeID[:]),
		"from_alias":    e.From.Alias,
		"from_role":     roleName(e.From.Role),
		"intent":        intentName(e.Intent),
		"created_at_ms": e.CreatedAtMs,
		"direction":     dir,
		"collab":        collab,
	}
	if !headersOnly {
		out["content"] = contentPayload(e.Content)
	}
	return out
}

// jsonResult marshals v to indented JSON.
func jsonResult(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// --- param helpers ----------------------------------------------------------

func requireString(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok || v == nil {
		return "", fmt.Errorf("missing required parameter %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("parameter %q must be a string", key)
	}
	return s, nil
}

func getString(params map[string]any, key, def string) string {
	v, ok := params[key]
	if !ok || v == nil {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return def
	}
	return s
}

func getFloat(params map[string]any, key string, def float64) float64 {
	v, ok := params[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return def
}

func getBool(params map[string]any, key string, def bool) bool {
	v, ok := params[key]
	if !ok || v == nil {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}
