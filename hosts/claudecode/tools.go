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
func RegisterTools(s *server.MCPServer, n *node.Node) {
	s.AddTool(toolkitTool(), toolkitHandler(n))
	s.AddTool(whoamiTool(), whoamiHandler(n))
	s.AddTool(peersTool(), peersHandler(n))
	s.AddTool(pairTool(), pairHandler(n))
	s.AddTool(consumeTool(), consumeHandler(n))
	s.AddTool(messageTool(), messageHandler(n))
	s.AddTool(inboxTool(), inboxHandler(n))
	s.AddTool(awaitTool(), awaitHandler(n))
	s.AddTool(replyTool(), replyHandler(n))
	s.AddTool(declineTool(), declineHandler(n))
}

// --- toolkit ----------------------------------------------------------------

func toolkitTool() mcp.Tool {
	return mcp.NewTool("clawdchan_toolkit",
		mcp.WithDescription("Return the full ClawdChan tool surface with a recommended workflow. "+
			"Call once at session start."),
	)
}

func toolkitHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := n.Identity()
		setup := buildSetupStatus(n)
		return jsonResult(map[string]any{
			"version": "0.2",
			"self": map[string]any{
				"node_id": hex.EncodeToString(id[:]),
				"alias":   n.Alias(),
				"relay":   n.RelayURL(),
			},
			"setup": setup,
			"tools": []map[string]any{
				{"name": "clawdchan_whoami", "summary": "This node's id and alias."},
				{"name": "clawdchan_peers", "summary": "Paired peers, with pending-ask and last-activity info per peer."},
				{"name": "clawdchan_pair", "summary": "Generate a pairing mnemonic and return it immediately; rendezvous completes in the background. SHOW THE MNEMONIC TO THE USER — they can't share it otherwise."},
				{"name": "clawdchan_consume", "summary": "Accept a peer's pairing mnemonic."},
				{"name": "clawdchan_message", "summary": "Send a message to a peer. Threads are managed for you. Non-blocking."},
				{"name": "clawdchan_inbox", "summary": "New envelopes grouped by peer, plus any pending ask_human surfaces for the user."},
				{"name": "clawdchan_await", "summary": "Short blocking wait (≤60s) for the next inbound envelope from a specific peer. Primitive for live agent-to-agent collaboration loops — use from a sub-agent, not the main agent."},
				{"name": "clawdchan_reply", "summary": "Submit the user's answer to the peer's pending ask_human."},
				{"name": "clawdchan_decline", "summary": "Decline the peer's pending ask_human on behalf of the user."},
			},
			"intents": []map[string]string{
				{"name": "say", "desc": "Default agent→agent message."},
				{"name": "ask", "desc": "Agent→agent; peer is expected to reply."},
				{"name": "notify_human", "desc": "FYI for the peer's human; no reply expected."},
				{"name": "ask_human", "desc": "The peer's human must answer. Their agent is forbidden from replying."},
			},
			"model": []string{
				"Threads are internal. You talk to peers. The first clawdchan_message to a peer implicitly opens a conversation; later messages continue it.",
				"Inbound surfaces two ways: (1) the daemon fires an OS notification like 'Alice replied — ask me about it', which brings the user back to this session; (2) call clawdchan_inbox to fetch aggregated traffic when you have reason to check.",
				"clawdchan_message is non-blocking, even for intent=ask. Default mode (passive): send and return to the user; the reply surfaces on the next turn via clawdchan_inbox. Do NOT poll in a loop from the main agent.",
				"ask_human envelopes from a peer are for the human, not you. pending_asks in the inbox contains them verbatim; present them to the user and call clawdchan_reply with the user's actual words (or clawdchan_decline).",
				"ACTIVE COLLAB MODE — when the user signals live collaboration with a peer (phrases like 'collaborate with Alice on X', 'iterate with her agent until you converge', 'work it out with Bruce', or they explicitly start a real problem and say both sides are on it): spawn a sub-agent via the Task tool. Do NOT run the loop on the main agent's turn — it blocks the user and burns main-agent context. Give the sub-agent a self-contained brief: the peer_id, the problem, what 'convergence' means, a max round count (e.g. 20), and permission to use clawdchan_message + clawdchan_await in a tight loop. The sub-agent returns a final summary when converged / stuck / max rounds. Main agent immediately returns control to the user and surfaces the sub-agent's summary when it lands.",
				"Sub-agent loop shape: clawdchan_message(peer, text, intent='ask') → clawdchan_await(peer, timeout_seconds=10) → if envelope: integrate + respond + repeat; if timeout: send a nudge OR report 'peer went silent' and stop after 2-3 consecutive timeouts. Always exit on user-visible errors.",
			},
			"notes": []string{
				"Mnemonics are 12 BIP-39 words — one-time pairing codes, not wallet seeds.",
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
		mcp.WithDescription("List paired peers, with inbound_count and pending_ask_count per peer. "+
			"Use node_id as the peer_id argument for clawdchan_message / clawdchan_reply / clawdchan_decline."),
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
		mcp.WithDescription("Send a message to a paired peer. Thread bookkeeping is automatic: the first message "+
			"to a peer opens a conversation; later messages continue it. Non-blocking — returns on relay ack, "+
			"NOT on peer reply. Never wait in a loop for the peer's reply; return to the user, and read "+
			"clawdchan_inbox on the next turn (or when the daemon toast prompts the user)."),
		mcp.WithString("peer_id", mcp.Required(), mcp.Description("Hex peer node id. Get it from clawdchan_peers.")),
		mcp.WithString("text", mcp.Required(), mcp.Description("Message body.")),
		mcp.WithString("intent", mcp.Description("say | ask | notify_human | ask_human. Default 'say'.")),
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
		peerID, err := parseNodeID(peerStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		intent, err := parseMessageIntent(req.GetString("intent", "say"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tid, err := resolveOrOpenThread(ctx, n, peerID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := n.Send(ctx, tid, intent, envelope.Content{Kind: envelope.ContentText, Text: text}); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := map[string]any{
			"ok":         true,
			"peer_id":    hex.EncodeToString(peerID[:]),
			"sent_at_ms": time.Now().UnixMilli(),
		}
		if w := maybeAttachListenerWarning(n); w != nil {
			out["setup_warning"] = w
		}
		return jsonResult(out), nil
	}
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

func inboxTool() mcp.Tool {
	return mcp.NewTool("clawdchan_inbox",
		mcp.WithDescription("Return recent envelopes grouped by peer, plus pending ask_human surfaces awaiting the user. "+
			"Pass since_ms to limit to traffic newer than a given timestamp. Pending asks are always included regardless "+
			"of since_ms — they linger until the user answers (clawdchan_reply) or declines (clawdchan_decline). "+
			"Each response carries now_ms; feed that back as since_ms on the next call to get only new traffic."),
		mcp.WithNumber("since_ms", mcp.Description("Only include non-pending envelopes with created_ms > since_ms. Default 0.")),
	)
}

func inboxHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		since := int64(req.GetFloat("since_ms", 0))
		threads, err := n.ListThreads(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		me := n.Identity()

		type bucket struct {
			envelopes    []map[string]any
			pendingAsks  []map[string]any
			lastActivity int64
		}
		buckets := map[identity.NodeID]*bucket{}

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
					b.pendingAsks = append(b.pendingAsks, serializeEnvelope(e))
					continue
				}
				if e.CreatedAtMs > since {
					b.envelopes = append(b.envelopes, serializeEnvelope(e))
				}
			}
		}

		peers, _ := n.ListPeers(ctx)
		aliasByID := map[identity.NodeID]string{}
		for _, p := range peers {
			aliasByID[p.NodeID] = p.Alias
		}
		out := make([]map[string]any, 0, len(buckets))
		for pid, b := range buckets {
			if len(b.envelopes) == 0 && len(b.pendingAsks) == 0 {
				continue
			}
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

		return jsonResult(map[string]any{
			"now_ms": time.Now().UnixMilli(),
			"peers":  out,
			"notes": []string{
				"pending_asks contain the raw ask_human text from the peer — present to the user verbatim. Do not answer yourself; use clawdchan_reply with the user's literal words or clawdchan_decline.",
				"Pass now_ms back as since_ms on your next call to get only newer traffic.",
			},
		}), nil
	}
}

// --- await (live collab primitive) -----------------------------------------

// awaitTool is the blocking primitive that enables rapid agent-to-agent
// ping-pong. It is intentionally scoped to a single peer and a short
// timeout, and is intended for use from a sub-agent (spawned via Claude
// Code's Task tool) that owns the live collaboration loop — not the main
// user-facing agent. The model field in the toolkit response describes the
// orchestration pattern in detail.
func awaitTool() mcp.Tool {
	return mcp.NewTool("clawdchan_await",
		mcp.WithDescription("Block up to timeout_seconds waiting for the next inbound envelope from peer_id. "+
			"Returns immediately if there's already an envelope newer than since_ms. Intended for live agent-to-agent "+
			"loops run from a Task sub-agent: message → await → message → await. Do NOT call from the main agent — "+
			"it freezes the user-facing turn. Unanswered ask_human envelopes are redacted the same way they are in "+
			"clawdchan_inbox; you should not try to answer them as the agent."),
		mcp.WithString("peer_id", mcp.Required(), mcp.Description("Hex peer node id. Get from clawdchan_peers.")),
		mcp.WithNumber("timeout_seconds", mcp.Description("Max seconds to block. Default 10, min 1, max 60. Short timeouts keep the sub-agent turn responsive to cancellation.")),
		mcp.WithNumber("since_ms", mcp.Description("Only return envelopes with created_ms > since_ms. Default 0. Pass now_ms from the previous await response to get only newer traffic.")),
	)
}

func awaitHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		peerStr, err := req.RequireString("peer_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		peerID, err := parseNodeID(peerStr)
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
	pending := pendingAsks(all, n.Identity())
	serialized := make([]map[string]any, 0, len(envs))
	var pendingIDs []string
	for _, e := range envs {
		if pending[e.EnvelopeID] {
			pendingIDs = append(pendingIDs, hex.EncodeToString(e.EnvelopeID[:]))
			stub := serializeEnvelope(e)
			stub["content"] = map[string]any{
				"kind": "text",
				"text": "[redacted: ask_human awaiting human reply; use clawdchan_inbox then clawdchan_reply/clawdchan_decline]",
			}
			serialized = append(serialized, stub)
			continue
		}
		serialized = append(serialized, serializeEnvelope(e))
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
		peerID, err := parseNodeID(peerStr)
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
		peerID, err := parseNodeID(peerStr)
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
		return envelope.ThreadID{}, fmt.Errorf("no pending ask_human from peer %s", hex.EncodeToString(peer[:]))
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

func serializeEnvelope(e envelope.Envelope) map[string]any {
	return map[string]any{
		"envelope_id":   hex.EncodeToString(e.EnvelopeID[:]),
		"from_node":     hex.EncodeToString(e.From.NodeID[:]),
		"from_alias":    e.From.Alias,
		"from_role":     roleName(e.From.Role),
		"intent":        intentName(e.Intent),
		"created_at_ms": e.CreatedAtMs,
		"content":       contentPayload(e.Content),
	}
}

func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	return mcp.NewToolResultText(string(b))
}
