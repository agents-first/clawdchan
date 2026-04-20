package claudecode

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/policy"
	"github.com/vMaroon/ClawdChan/core/store"
	"github.com/vMaroon/ClawdChan/internal/listenerreg"
)

// RegisterTools registers the ClawdChan MCP surface on s, bound to n.
//
// The surface is deliberately peer-centric. Threads are an internal plumbing
// detail the agent never sees: you message a peer, reply to a peer, read an
// aggregate inbox. This keeps Claude's mental state small — "who am I talking
// to" — and matches the UX the daemon enforces: toasts like "Alice's agent
// replied — ask me about it" bring the user back to the Claude Code session,
// where the agent resumes naturally from inbox state.
//
// opts tune what the toolkit tool reports back — notably whether the local
// node has agent-dispatch configured, which the agent wants to know so it
// can set expectations about cadence.
func RegisterTools(s *server.MCPServer, n *node.Node, opts ...Option) {
	cfg := regOpts{}
	for _, opt := range opts {
		opt(&cfg)
	}
	s.AddTool(toolkitTool(), toolkitHandler(n, &cfg))
	s.AddTool(whoamiTool(), whoamiHandler(n))
	s.AddTool(peersTool(), peersHandler(n))
	s.AddTool(pairTool(), pairHandler(n))
	s.AddTool(consumeTool(), consumeHandler(n))
	s.AddTool(messageTool(), messageHandler(n))
	s.AddTool(inboxTool(), inboxHandler(n))
	s.AddTool(awaitTool(), awaitHandler(n))
	s.AddTool(replyTool(), replyHandler(n))
	s.AddTool(declineTool(), declineHandler(n))
	s.AddTool(peerRenameTool(), peerRenameHandler(n))
	s.AddTool(peerRevokeTool(), peerRevokeHandler(n))
	s.AddTool(peerRemoveTool(), peerRemoveHandler(n))
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

func toolkitTool() mcp.Tool {
	return mcp.NewTool("clawdchan_toolkit",
		mcp.WithDescription("Return the full ClawdChan tool surface with a recommended workflow. "+
			"Call once at session start."),
	)
}

func toolkitHandler(n *node.Node, opts *regOpts) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
			"version": "0.3",
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
			"model": []string{
				"Threads are internal. You talk to peers. First message opens a conversation; later messages continue it.",
				"Every envelope carries `direction` (in/out) and `collab` (true for live-exchange markers). No hex compares, no title pattern-matching.",
				"`clawdchan_message` is non-blocking even for ask. Default flow: send, end the turn, let the daemon toast the user when a reply lands. The main agent does NOT poll.",
				"Live iterative collab loops belong in a Task sub-agent. Brief it (peer, problem, convergence criterion, max rounds), let it run `clawdchan_message(collab=true)` + `clawdchan_subagent_await` until done, fold its summary into your reply to the user.",
				"Receiving side, collab=true inbound, no local dispatcher: ask the user once — engage live (spawn your own sub-agent) or reply at their pace.",
				"ask_human is the peer's human's to answer. Use `clawdchan_reply` with the user's literal words or `clawdchan_decline`. Never compose an answer yourself.",
				"The daemon fires OS notifications on inbound. Tell the user you'll surface replies on their next turn — don't implement polling yourself.",
			},
			"notes": []string{
				"Mnemonics are 12 BIP-39 words — one-time pairing codes, not wallet seeds. Always surface them to the user verbatim.",
				"SAS is a 4-word fingerprint. Confirm it matches on both sides over a trusted channel before sharing sensitive material.",
			},
		}), nil
	}
}

// --- setup / listener nudge -------------------------------------------------

// buildSetupStatus inspects the listener registry and returns a structured
// blob plus a ready-to-speak user_message. The daemon is the recommended
// listener; the MCP server is a fallback that dies with the CC session.
func buildSetupStatus(n *node.Node) map[string]any {
	entries, err := listenerreg.List(n.DataDir())
	if err != nil {
		return map[string]any{
			"error":                      err.Error(),
			"needs_persistent_listener":  true,
			"user_message":               "Couldn't read the listener registry. A persistent daemon (`clawdchan daemon`) will make sure inbound messages keep arriving and trigger OS notifications — want me to walk you through it?",
			"mcp_self_is_listener":       true,
			"persistent_listener_active": false,
		}
	}

	nid := n.Identity()
	me := hex.EncodeToString(nid[:])
	myPID := osGetpid()

	var hasCLI, hasOtherMCP bool
	listeners := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		if !strings.EqualFold(e.NodeID, me) {
			continue
		}
		isSelf := e.PID == myPID
		listeners = append(listeners, map[string]any{
			"pid":        e.PID,
			"kind":       string(e.Kind),
			"started_ms": e.StartedMs,
			"is_self":    isSelf,
			"relay":      e.RelayURL,
			"alias":      e.Alias,
		})
		switch e.Kind {
		case listenerreg.KindCLI:
			hasCLI = true
		case listenerreg.KindMCP:
			if !isSelf {
				hasOtherMCP = true
			}
		}
	}

	needs := !hasCLI
	var userMsg string
	switch {
	case hasCLI:
		userMsg = "You have a persistent `clawdchan daemon` running — OS notifications will fire on inbound, and the relay link survives this Claude Code session. Nothing to set up."
	case hasOtherMCP:
		userMsg = "Another Claude Code session on this machine is acting as your listener. For ambient, always-on delivery with OS notifications, open a terminal and run `clawdchan daemon`."
	default:
		userMsg = "Heads-up: right now I'm your only listener (via this MCP server). Messages will stop arriving the moment this Claude Code session closes. " +
			"For a persistent inbox with OS notifications, open a terminal and run:\n\n    clawdchan daemon\n\n" +
			"Want me to wait while you start that, or proceed without it?"
	}

	return map[string]any{
		"mcp_self_is_listener":       true,
		"persistent_listener_active": hasCLI,
		"needs_persistent_listener":  needs,
		"listeners":                  listeners,
		"listener_command":           "clawdchan daemon",
		"user_message":               userMsg,
	}
}

var osGetpid = func() int { return os.Getpid() }

func maybeAttachListenerWarning(n *node.Node) map[string]any {
	setup := buildSetupStatus(n)
	needs, _ := setup["needs_persistent_listener"].(bool)
	if !needs {
		return nil
	}
	return map[string]any{
		"needs_persistent_listener": true,
		"user_message":              setup["user_message"],
		"listener_command":          setup["listener_command"],
	}
}

// --- whoami -----------------------------------------------------------------

func whoamiTool() mcp.Tool {
	return mcp.NewTool("clawdchan_whoami",
		mcp.WithDescription("Return this node's alias and node id."),
	)
}

func whoamiHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := n.Identity()
		return jsonResult(map[string]any{
			"node_id": hex.EncodeToString(id[:]),
			"alias":   n.Alias(),
		}), nil
	}
}

// --- peers ------------------------------------------------------------------

func peersTool() mcp.Tool {
	return mcp.NewTool("clawdchan_peers",
		mcp.WithDescription("List paired peers with per-peer inbound_count, pending_asks, and last_activity_ms. "+
			"Use node_id as the peer_id argument for clawdchan_message / clawdchan_reply / clawdchan_decline. "+
			"Accepts hex, hex-prefix (>=4), or exact alias everywhere a peer_id is required."),
	)
}

func peersHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		peers, err := n.ListPeers(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		threads, err := n.ListThreads(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
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
		return jsonResult(map[string]any{"peers": out}), nil
	}
}

// --- message ----------------------------------------------------------------

func messageTool() mcp.Tool {
	return mcp.NewTool("clawdchan_message",
		mcp.WithDescription("Send a message to a paired peer. Non-blocking — returns on relay ack, not on peer reply. "+
			"Thread is resolved automatically. If this peer has an open ask_human pending the user, use "+
			"clawdchan_reply (user's answer) or clawdchan_decline (user declines) instead; clawdchan_message is "+
			"for free-form additional messages to the peer's agent."),
		mcp.WithString("peer_id", mcp.Required(), mcp.Description("Hex node id, unique hex prefix (>=4), or exact alias.")),
		mcp.WithString("text", mcp.Required(), mcp.Description("Message body. Plain text.")),
		mcp.WithString("intent", mcp.Description(
			"Routing hint. Choices:\n"+
				"  say          — agent→agent FYI, no reply expected (default)\n"+
				"  ask          — agent→agent, peer's agent is expected to reply\n"+
				"  notify_human — agent→peer's human, FYI, no reply expected\n"+
				"  ask_human    — agent→peer's HUMAN specifically; the peer's agent is forbidden from replying (their human must answer or decline)")),
		mcp.WithBoolean("collab", mcp.Description("Set true only from inside a Task sub-agent running a live iterative loop. The envelope gets the clawdchan:collab_sync marker so the peer's daemon can auto-answer (if dispatch configured) or surface the live-loop choice to their human. Do not set this from the main agent.")),
	)
}

func messageHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		peerStr, err := req.RequireString("peer_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		text, err := req.RequireString("text")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		peerID, err := resolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		intent, err := parseMessageIntent(req.GetString("intent", "say"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		collab := req.GetBool("collab", false)
		tid, err := resolveOrOpenThread(ctx, n, peerID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		content := envelope.Content{Kind: envelope.ContentText, Text: text}
		if collab {
			// Wrap as ContentDigest with a reserved title. The peer's inbox
			// output shows the title explicitly, and the daemon switches
			// notification copy when it sees this marker — so the receiver's
			// side knows a live-collab sub-agent is waiting.
			content = envelope.Content{
				Kind:  envelope.ContentDigest,
				Title: collabSyncTitle,
				Body:  text,
			}
		}
		if err := n.Send(ctx, tid, intent, content); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := map[string]any{
			"ok":         true,
			"peer_id":    hex.EncodeToString(peerID[:]),
			"sent_at_ms": time.Now().UnixMilli(),
			"collab":     collab,
		}
		// Pending-ask awareness: if this peer has an unanswered ask_human
		// from us, warn. The agent should almost always answer those via
		// clawdchan_reply (user's words) rather than a free-form message.
		// Non-blocking — the send already happened; this is a hint for
		// the agent's next action.
		if hasOpenAskHumanFromPeer(ctx, n, peerID) {
			out["pending_ask_hint"] = "This peer has an unanswered ask_human pending the user. If your text was meant as the user's answer, use clawdchan_reply instead of clawdchan_message. If it's an additional message for the peer's agent, disregard this hint."
		}
		if w := maybeAttachListenerWarning(n); w != nil {
			out["setup_warning"] = w
		}
		return jsonResult(out), nil
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

// collabSyncTitle is the reserved Content.Title value that marks an
// envelope as part of an active live-collab exchange. This is re-exported
// from core/policy.CollabSyncTitle so host-internal code reads naturally
// without importing policy just for a string constant.
const collabSyncTitle = policy.CollabSyncTitle

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

func inboxTool() mcp.Tool {
	return mcp.NewTool("clawdchan_inbox",
		mcp.WithDescription("Envelopes per peer, plus pending ask_human awaiting the user. Each envelope carries "+
			"`direction` (in/out) and `collab` (true for live-exchange markers). Feed the returned `now_ms` back "+
			"as `since_ms` next call for only new traffic. Pass `wait_seconds` (≤15) to long-poll when you've just "+
			"sent something and want to check back cheaply — returns early if anything lands."),
		mcp.WithNumber("since_ms", mcp.Description("Only include non-pending envelopes created after this ms timestamp.")),
		mcp.WithNumber("wait_seconds", mcp.Description("Long-poll up to N seconds (max 15). 0 = non-blocking.")),
		mcp.WithString("include", mcp.Description("'full' (default) or 'headers' to drop content bodies for cheap long-thread polling.")),
		mcp.WithBoolean("notes_seen", mcp.Description("Omit the usage-notes field once you've internalized the pattern.")),
	)
}

// maxInboxWaitSeconds caps how long a single inbox call can block. Anything
// longer than ~15s fights with MCP call timeouts and with the user's
// "why is Claude idle" impression. For tighter loops, use clawdchan_await.
const maxInboxWaitSeconds = 15

func inboxHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		since := int64(req.GetFloat("since_ms", 0))
		wait := req.GetFloat("wait_seconds", 0)
		if wait < 0 {
			wait = 0
		}
		if wait > maxInboxWaitSeconds {
			wait = maxInboxWaitSeconds
		}
		headersOnly := strings.EqualFold(strings.TrimSpace(req.GetString("include", "full")), "headers")
		notesSeen := req.GetBool("notes_seen", false)

		deadline := time.Now().Add(time.Duration(wait * float64(time.Second)))
		const pollInterval = 400 * time.Millisecond
		for {
			out, anyTraffic, hasPending, hasCollab, hasUndeliveredOutbound, nowMs, err := collectInbox(ctx, n, since, headersOnly)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if anyTraffic || wait == 0 || !time.Now().Before(deadline) {
				resp := map[string]any{
					"now_ms": nowMs,
					"peers":  out,
				}
				if !notesSeen {
					resp["notes"] = inboxNotes(hasPending, hasCollab, hasUndeliveredOutbound)
				}
				return jsonResult(resp), nil
			}
			select {
			case <-ctx.Done():
				resp := map[string]any{
					"now_ms":    time.Now().UnixMilli(),
					"peers":     []any{},
					"cancelled": true,
				}
				return jsonResult(resp), nil
			case <-time.After(pollInterval):
			}
		}
	}
}

// inboxNotes fires a note only when it's relevant to the response payload.
// Keeps the guidance dense and stops the agent from re-reading the same
// four reminders on every poll.
const outboundStatusNote = "Outbound messages show status (queued/sent/delivered). 'queued' means it hasn't left your machine; 'sent' means the relay has it but the peer hasn't acked; 'delivered' means the peer's node stored and signed for it. Persistent 'sent' across hours likely means the peer is offline."

func inboxNotes(hasPending, hasCollab, hasUndeliveredOutbound bool) []string {
	var notes []string
	if hasPending {
		notes = append(notes, "pending_asks carry the peer's ask_human verbatim. Present to the user, then clawdchan_reply with their literal words or clawdchan_decline. Do not compose an answer yourself.")
	}
	if hasCollab {
		notes = append(notes, "Envelopes with collab=true are part of a live agent-to-agent exchange. If direction='in' and you didn't initiate, the peer has a sub-agent waiting. If their side has no dispatcher, ask the user whether to engage live or reply at their own pace.")
	}
	if hasUndeliveredOutbound {
		notes = append(notes, outboundStatusNote)
	}
	return notes
}

// collectInbox assembles the grouped-by-peer inbox view. Also returns
// whether any pending_asks are present and whether any visible envelope
// carries the collab marker, so the caller can attach only the notes
// that are contextually relevant.
func collectInbox(ctx context.Context, n *node.Node, since int64, headersOnly bool) ([]map[string]any, bool, bool, bool, bool, int64, error) {
	threads, err := n.ListThreads(ctx)
	if err != nil {
		return nil, false, false, false, false, 0, err
	}
	me := n.Identity()

	type bucket struct {
		envelopes    []map[string]any
		pendingAsks  []map[string]any
		lastActivity int64
	}
	buckets := map[identity.NodeID]*bucket{}
	hasPending, hasCollab, hasUndeliveredOutbound := false, false, false

	for _, t := range threads {
		records, err := n.Store().ListEnvelopeRecords(ctx, t.ID, 0)
		if err != nil {
			continue
		}
		envs := make([]envelope.Envelope, 0, len(records))
		for _, rec := range records {
			envs = append(envs, rec.Envelope)
		}
		pending := pendingAsks(envs, me)
		b := buckets[t.PeerID]
		if b == nil {
			b = &bucket{}
			buckets[t.PeerID] = b
		}
		for _, rec := range records {
			e := rec.Envelope
			if e.Intent == envelope.IntentAck {
				continue
			}
			if e.CreatedAtMs > b.lastActivity {
				b.lastActivity = e.CreatedAtMs
			}
			if pending[e.EnvelopeID] {
				b.pendingAsks = append(b.pendingAsks, serializeEnvelope(e, me, false, &rec))
				hasPending = true
				continue
			}
			if e.CreatedAtMs > since {
				rendered := serializeEnvelope(e, me, headersOnly, &rec)
				if c, ok := rendered["collab"].(bool); ok && c {
					hasCollab = true
				}
				if e.From.NodeID == me && rec.Status != store.StatusDelivered {
					hasUndeliveredOutbound = true
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
	return out, haveAny, hasPending, hasCollab, hasUndeliveredOutbound, time.Now().UnixMilli(), nil
}

// --- subagent_await (live collab primitive) -------------------------------

// awaitTool is the blocking primitive for live agent-to-agent loops.
// Its MCP name is deliberately prefixed `clawdchan_subagent_` so every
// tool listing carries the scope constraint — this tool freezes the
// user-facing turn when called from the main agent, which is almost
// never what you want.
func awaitTool() mcp.Tool {
	return mcp.NewTool("clawdchan_subagent_await",
		mcp.WithDescription("SUB-AGENT TOOL. Blocks up to timeout_seconds waiting for the next inbound envelope "+
			"from peer_id. Intended for a Task sub-agent's live loop: message(collab=true) → subagent_await → "+
			"message → subagent_await. Calling this from the main agent freezes the user's turn — use "+
			"clawdchan_inbox(wait_seconds=...) instead for gentle main-agent waits. Pending ask_human envelopes "+
			"are redacted here too; answering as the agent is not permitted."),
		mcp.WithString("peer_id", mcp.Required(), mcp.Description("Hex, hex-prefix (>=4), or exact alias.")),
		mcp.WithNumber("timeout_seconds", mcp.Description("Max seconds to block. Default 10, min 1, max 60.")),
		mcp.WithNumber("since_ms", mcp.Description("Only return envelopes with created_ms > since_ms. Feed the previous response's now_ms here.")),
	)
}

func awaitHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		peerStr, err := req.RequireString("peer_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		peerID, err := resolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		timeout := req.GetFloat("timeout_seconds", 10)
		if timeout < 1 {
			timeout = 1
		}
		if timeout > 60 {
			timeout = 60
		}
		since := int64(req.GetFloat("since_ms", 0))

		tid, err := resolveOrOpenThread(ctx, n, peerID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		me := n.Identity()

		deadline := time.Now().Add(time.Duration(timeout * float64(time.Second)))
		// Poll the store directly (not via node.Subscribe). The daemon owns
		// inbound when running, which means MCP's in-process subscriber never
		// fires for envelopes received by the daemon. SQLite polling is the
		// cheap portable way to observe both the in-process and cross-process
		// cases without new IPC plumbing.
		const pollInterval = 500 * time.Millisecond
		for {
			envs, err := n.ListEnvelopes(ctx, tid, since)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			var fresh []envelope.Envelope
			for _, e := range envs {
				if e.From.NodeID != me {
					fresh = append(fresh, e)
				}
			}
			if len(fresh) > 0 {
				return awaitResult(ctx, n, tid, fresh), nil
			}
			if time.Now().After(deadline) {
				return jsonResult(map[string]any{
					"envelopes": []any{},
					"timeout":   true,
					"now_ms":    time.Now().UnixMilli(),
				}), nil
			}
			select {
			case <-ctx.Done():
				return jsonResult(map[string]any{
					"envelopes": []any{},
					"cancelled": true,
					"now_ms":    time.Now().UnixMilli(),
				}), nil
			case <-time.After(pollInterval):
			}
		}
	}
}

// awaitResult redacts unanswered remote ask_human envelopes the same way
// the inbox does — the agent (even a sub-agent running a collab loop) must
// not answer for the human.
func awaitResult(ctx context.Context, n *node.Node, tid envelope.ThreadID, envs []envelope.Envelope) *mcp.CallToolResult {
	all, _ := n.ListEnvelopes(ctx, tid, 0)
	me := n.Identity()
	pending := pendingAsks(all, me)
	serialized := make([]map[string]any, 0, len(envs))
	var pendingIDs []string
	for _, e := range envs {
		if pending[e.EnvelopeID] {
			pendingIDs = append(pendingIDs, hex.EncodeToString(e.EnvelopeID[:]))
			stub := serializeEnvelope(e, me, false, nil)
			stub["content"] = map[string]any{
				"kind": "text",
				"text": "[redacted: ask_human awaiting human reply; use clawdchan_inbox then clawdchan_reply/clawdchan_decline]",
			}
			serialized = append(serialized, stub)
			continue
		}
		serialized = append(serialized, serializeEnvelope(e, me, false, nil))
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

func replyTool() mcp.Tool {
	return mcp.NewTool("clawdchan_reply",
		mcp.WithDescription("Submit the user's answer to the peer's pending ask_human. Routed to the latest thread "+
			"with the peer that has an unanswered ask_human. The envelope is sent with role=human — only call this "+
			"with the user's actual words, never with your own paraphrase."),
		mcp.WithString("peer_id", mcp.Required()),
		mcp.WithString("text", mcp.Required(), mcp.Description("The user's literal answer.")),
	)
}

func replyHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		peerStr, err := req.RequireString("peer_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		text, err := req.RequireString("text")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		peerID, err := resolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tid, err := findThreadWithPendingAsk(ctx, n, peerID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := n.SubmitHumanReply(ctx, tid, envelope.Content{Kind: envelope.ContentText, Text: text}); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"ok": true}), nil
	}
}

func declineTool() mcp.Tool {
	return mcp.NewTool("clawdchan_decline",
		mcp.WithDescription("Decline the peer's pending ask_human on behalf of the user. Sends role=human with a declination so the peer knows the ask was surfaced but won't be answered."),
		mcp.WithString("peer_id", mcp.Required()),
		mcp.WithString("reason", mcp.Description("Optional short reason shown to the peer. Default: 'declined by user'.")),
	)
}

func declineHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		peerStr, err := req.RequireString("peer_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		reason := req.GetString("reason", "declined by user")
		peerID, err := resolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tid, err := findThreadWithPendingAsk(ctx, n, peerID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := n.SubmitHumanReply(ctx, tid, envelope.Content{Kind: envelope.ContentText, Text: "[declined] " + reason}); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"ok": true}), nil
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

func pairTool() mcp.Tool {
	return mcp.NewTool("clawdchan_pair",
		mcp.WithDescription("Generate a pairing mnemonic and return it IMMEDIATELY — the rendezvous with the peer "+
			"happens in the background. You MUST surface the 12-word mnemonic to the user verbatim in your response; "+
			"it's the only way for them to share it with the peer. Do not hide it behind a summary. "+
			"Tell the user: 'share these 12 words with your peer, they run clawdchan_consume on their side'. "+
			"To confirm pairing completed, call clawdchan_peers — a new peer means the rendezvous succeeded. "+
			"The mnemonic is a one-time rendezvous code, not a wallet seed."),
		mcp.WithNumber("timeout_seconds", mcp.Description("Background rendezvous timeout. Default 300.")),
	)
}

func pairHandler(n *node.Node) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		timeout := time.Duration(req.GetFloat("timeout_seconds", 300)) * time.Second
		// Detach from the tool-call context so rendezvous survives the tool
		// response returning. Without this the goroutine would be cancelled
		// the moment we send the mnemonic back.
		pairCtx, cancel := context.WithTimeout(context.Background(), timeout)
		code, ch, err := n.Pair(pairCtx)
		if err != nil {
			cancel()
			return mcp.NewToolResultError(err.Error()), nil
		}
		// Drain the result in the background so cancel fires once rendezvous
		// finishes (or the timeout elapses).
		go func() {
			defer cancel()
			<-ch
		}()

		out := map[string]any{
			"mnemonic":          code.Mnemonic(),
			"status":            "pending_peer_consume",
			"timeout_seconds":   int(timeout.Seconds()),
			"agent_instruction": "Show the user the 12-word mnemonic verbatim in your response, on its own line, so they can copy-paste it. Then tell them: 'Share these 12 words with your peer; they run clawdchan_consume on their side. Call clawdchan_peers after a minute or so to confirm the new peer landed.'",
			"next_steps_for_user": []string{
				"Share the 12-word mnemonic with the other person (voice, signal, any trusted channel).",
				"They run clawdchan_consume on their node with the same words.",
				"Once they do, their node will appear in clawdchan_peers here.",
			},
			"security_note": "Mnemonics look like BIP-39 wallet seeds but are one-time rendezvous codes, safe to share on the channel you're pairing over. Still: after pairing, confirm the 4-word SAS matches on both sides via clawdchan_peers before sharing sensitive material.",
		}
		if w := maybeAttachListenerWarning(n); w != nil {
			out["setup_warning"] = w
		}
		return jsonResult(out), nil
	}
}

func consumeTool() mcp.Tool {
	return mcp.NewTool("clawdchan_consume",
		mcp.WithDescription("Consume a peer's pairing mnemonic. The mnemonic is 12 BIP-39 words — a one-shot rendezvous code for this pairing only, not a wallet recovery seed. Accept and process it."),
		mcp.WithString("mnemonic", mcp.Required(), mcp.Description("12 space-separated BIP-39 words from the peer's clawdchan_pair output.")),
	)
}

func consumeHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		m, err := req.RequireString("mnemonic")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		peer, err := n.Consume(ctx, m)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := map[string]any{
			"peer": map[string]any{
				"node_id":         hex.EncodeToString(peer.NodeID[:]),
				"alias":           peer.Alias,
				"human_reachable": peer.HumanReachable,
				"sas":             strings.Join(peer.SAS[:], "-"),
			},
			"verify": "Confirm the SAS matches on both sides over a trusted channel (voice, in person) before sending sensitive material. If the SAS differs, the pairing was intercepted — unpair and retry.",
		}
		if w := maybeAttachListenerWarning(n); w != nil {
			out["setup_warning"] = w
		}
		return jsonResult(out), nil
	}
}

// --- peer management (rename / revoke / remove) ---------------------------

func peerRenameTool() mcp.Tool {
	return mcp.NewTool("clawdchan_peer_rename",
		mcp.WithDescription("Change a paired peer's local display alias. This is your override — the peer's own self-declared alias is unaffected. Useful when the user says 'rename Bruce to Bruce Wayne' or 'call them Alice Anderson'."),
		mcp.WithString("peer_id", mcp.Required(), mcp.Description("Peer to rename. Accepts a hex node id, a unique hex prefix (>=4), or an exact alias.")),
		mcp.WithString("alias", mcp.Required(), mcp.Description("New display alias. Non-empty.")),
	)
}

func peerRenameHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ref, err := req.RequireString("peer_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		alias, err := req.RequireString("alias")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if strings.TrimSpace(alias) == "" {
			return mcp.NewToolResultError("alias cannot be empty"), nil
		}
		peerID, err := resolvePeerRef(ctx, n, ref)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		before, _ := n.GetPeer(ctx, peerID)
		if err := n.SetPeerAlias(ctx, peerID, alias); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"ok":       true,
			"peer_id":  hex.EncodeToString(peerID[:]),
			"previous": before.Alias,
			"alias":    alias,
		}), nil
	}
}

func peerRevokeTool() mcp.Tool {
	return mcp.NewTool("clawdchan_peer_revoke",
		mcp.WithDescription("Mark a peer's trust as revoked. Inbound envelopes from them will be dropped; outbound sends will error. The record and history stay — use clawdchan_peer_remove for a full delete. Only call with explicit user intent ('revoke Alice', 'stop trusting Bruce', 'cut off X')."),
		mcp.WithString("peer_id", mcp.Required(), mcp.Description("Peer to revoke. Accepts hex, prefix, or alias.")),
	)
}

func peerRevokeHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ref, err := req.RequireString("peer_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		peerID, err := resolvePeerRef(ctx, n, ref)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		p, _ := n.GetPeer(ctx, peerID)
		if err := n.RevokePeer(ctx, peerID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"ok":      true,
			"peer_id": hex.EncodeToString(peerID[:]),
			"alias":   p.Alias,
			"note":    "trust=revoked. Inbound dropped; history preserved. Use clawdchan_peer_remove for a hard delete.",
		}), nil
	}
}

func peerRemoveTool() mcp.Tool {
	return mcp.NewTool("clawdchan_peer_remove",
		mcp.WithDescription("HARD DELETE a peer plus all threads, envelopes, and outbox entries tied to them. Irreversible. Confirm with the user first using their own words — this is destructive. Only call when the user explicitly asks to 'remove', 'delete', or 'forget' a peer."),
		mcp.WithString("peer_id", mcp.Required(), mcp.Description("Peer to delete. Accepts hex, prefix, or alias.")),
		mcp.WithBoolean("confirmed", mcp.Required(), mcp.Description("Must be true. Set only after the user has explicitly confirmed the destructive action in plain English.")),
	)
}

func peerRemoveHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ref, err := req.RequireString("peer_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		confirmed := req.GetBool("confirmed", false)
		if !confirmed {
			return mcp.NewToolResultError("refusing to delete without confirmed=true. Ask the user explicitly and only then retry with confirmed=true."), nil
		}
		peerID, err := resolvePeerRef(ctx, n, ref)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		p, _ := n.GetPeer(ctx, peerID)
		if err := n.DeletePeer(ctx, peerID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"ok":      true,
			"peer_id": hex.EncodeToString(peerID[:]),
			"alias":   p.Alias,
			"note":    "hard-deleted. All threads, envelopes, outbox entries for this peer are gone. Pairing would require a fresh mnemonic.",
		}), nil
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

func intentName(i envelope.Intent) string {
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
func statusName(s store.DeliveryStatus) string {
	switch s {
	case store.StatusSent:
		return "sent"
	case store.StatusDelivered:
		return "delivered"
	default:
		return "queued"
	}
}

func serializeEnvelope(e envelope.Envelope, me identity.NodeID, headersOnly bool, rec *store.EnvelopeRecord) map[string]any {
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
	if dir == "out" {
		status := store.StatusQueued
		var sentAtMs, deliveredAtMs int64
		if rec != nil {
			status = rec.Status
			sentAtMs = rec.SentAtMs
			deliveredAtMs = rec.DeliveredAtMs
		}
		out["status"] = statusName(status)
		if status != store.StatusQueued {
			out["sent_at_ms"] = sentAtMs
		}
		if status == store.StatusDelivered {
			out["delivered_at_ms"] = deliveredAtMs
		}
	}
	return out
}

func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	return mcp.NewToolResultText(string(b))
}
