package claudecode

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/internal/listenerreg"
)

// RegisterTools registers the full ClawdChan MCP tool surface on s, bound to n.
func RegisterTools(s *server.MCPServer, n *node.Node) {
	s.AddTool(toolkitTool(), toolkitHandler(n))
	s.AddTool(sessionStatusTool(), sessionStatusHandler(n))
	s.AddTool(whoamiTool(), whoamiHandler(n))
	s.AddTool(peersTool(), peersHandler(n))
	s.AddTool(threadsTool(), threadsHandler(n))
	s.AddTool(openThreadTool(), openThreadHandler(n))
	s.AddTool(sendTool(), sendHandler(n))
	s.AddTool(pollTool(), pollHandler(n))
	s.AddTool(waitTool(), waitHandler(n))
	s.AddTool(pairTool(), pairHandler(n))
	s.AddTool(consumeTool(), consumeHandler(n))
	s.AddTool(pendingAsksTool(), pendingAsksHandler(n))
	s.AddTool(submitHumanReplyTool(), submitHumanReplyHandler(n))
	s.AddTool(declineHumanTool(), declineHumanHandler(n))
}

// --- toolkit ----------------------------------------------------------------

// toolkitTool returns the bundled capability list for the clawdchan MCP surface.
// Agents can call this once instead of discovering tools one at a time.
func toolkitTool() mcp.Tool {
	return mcp.NewTool("clawdchan_toolkit",
		mcp.WithDescription("Return the full ClawdChan tool surface with descriptions, intents, roles, and a recommended workflow. "+
			"Call once at session start to avoid a chain of schema-discovery round trips."),
	)
}

func toolkitHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := n.Identity()
		setup := buildSetupStatus(n)
		return jsonResult(map[string]any{
			"version": "0.1",
			"self": map[string]any{
				"node_id": hex.EncodeToString(id[:]),
				"alias":   n.Alias(),
				"relay":   n.RelayURL(),
			},
			"setup": setup,
			"agent_instructions": []string{
				"If the user has not been greeted yet in this session, greet them now and surface `setup.user_message` verbatim as part of your first user-facing message. Do not bury it below a bullet list.",
				"If `setup.needs_persistent_listener` is true, ask the user whether they'd like to open a second terminal and run `clawdchan listen -follow` now. Don't proceed with other ClawdChan work until they confirm or decline.",
				"Before any clawdchan_send/clawdchan_wait to a peer, you may call clawdchan_session_status to re-check listener state if a lot of time has passed.",
			},
			"tools": []map[string]any{
				{"name": "clawdchan_session_status", "summary": "Listener presence for this data dir. Tells the user whether they have a persistent listener or only the in-session MCP one."},
				{"name": "clawdchan_whoami", "summary": "This node's id."},
				{"name": "clawdchan_peers", "summary": "Paired peers."},
				{"name": "clawdchan_threads", "summary": "Conversation threads."},
				{"name": "clawdchan_open_thread", "summary": "Open a thread with a peer (optionally send an intro context pack)."},
				{"name": "clawdchan_send", "summary": "Send a message on a thread with an intent."},
				{"name": "clawdchan_poll", "summary": "Envelopes newer than since_ms. Unanswered ask_human envelopes are redacted."},
				{"name": "clawdchan_wait", "summary": "Long-poll: block until a new envelope arrives on a thread or timeout."},
				{"name": "clawdchan_pair", "summary": "Generate a pairing mnemonic."},
				{"name": "clawdchan_consume", "summary": "Consume a peer's pairing mnemonic."},
				{"name": "clawdchan_pending_asks", "summary": "List remote ask_human envelopes awaiting the human. Do NOT answer these yourself."},
				{"name": "clawdchan_submit_human_reply", "summary": "Submit the user's reply to a pending ask_human."},
				{"name": "clawdchan_decline_human", "summary": "Decline a pending ask_human on behalf of the user (closes it without a human answer)."},
			},
			"intents": []map[string]string{
				{"name": "say", "desc": "Default message to the peer's agent."},
				{"name": "ask", "desc": "Peer's agent is expected to reply."},
				{"name": "notify_human", "desc": "FYI for the peer's human. No reply expected."},
				{"name": "ask_human", "desc": "Request the peer's human's explicit input. The peer agent must NOT answer; use submit_human_reply or decline_human."},
				{"name": "handoff", "desc": "Yield the turn; next envelope must be role=human."},
				{"name": "ack", "desc": "Delivery/read acknowledgement."},
				{"name": "close", "desc": "End the thread."},
			},
			"roles": []map[string]string{
				{"name": "agent", "desc": "Composed by the agent (you). Includes CLI sends from the user's shell in v0.1."},
				{"name": "human", "desc": "Composed by the human via submit_human_reply."},
			},
			"workflow": []string{
				"First turn: clawdchan_toolkit, then clawdchan_peers to see who you can talk to.",
				"Greet a peer: clawdchan_open_thread with a context pack; the peer sees it as the first envelope.",
				"Talk: clawdchan_send with intent=ask, then clawdchan_wait for the reply.",
				"Each user turn: clawdchan_pending_asks. If any, present to the user and submit_human_reply or decline_human.",
			},
			"notes": []string{
				"Mnemonics are 12 BIP-39 words. Treat them like a one-time pairing code, NOT a crypto wallet seed.",
				"SAS is a 4-word fingerprint. Confirm it matches on both sides over a trusted channel before sending sensitive material.",
				"clawdchan_send returns ok after the relay acks, not after the peer reads. Until the daemon ships, peers that are not running `clawdchan listen` or an open CC session will only see messages when they next connect.",
			},
		}), nil
	}
}

// --- session_status --------------------------------------------------------

func sessionStatusTool() mcp.Tool {
	return mcp.NewTool("clawdchan_session_status",
		mcp.WithDescription("Report which listeners are currently attached to this node's data dir. "+
			"A 'listener' is any process holding a live relay link: the in-session MCP server, or a persistent "+
			"`clawdchan listen` in a separate terminal. If no CLI listener is running, inbound messages stop "+
			"the moment this Claude Code session closes; surface the returned user_message to the user and offer "+
			"to walk them through starting one."),
	)
}

func sessionStatusHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return jsonResult(buildSetupStatus(n)), nil
	}
}

// buildSetupStatus inspects the listener registry and returns a structured
// status blob plus a ready-to-speak user_message. Safe to call on every
// toolkit/sessionStatus invocation; it also prunes stale pidfiles.
func buildSetupStatus(n *node.Node) map[string]any {
	entries, err := listenerreg.List(n.DataDir())
	if err != nil {
		return map[string]any{
			"error":                      err.Error(),
			"needs_persistent_listener":  true,
			"user_message":               "I couldn't read the listener registry. A persistent listener (`clawdchan listen -follow` in a separate terminal) will make sure inbound messages keep arriving after this session ends — want me to walk you through it?",
			"mcp_self_is_listener":       true,
			"persistent_listener_active": false,
		}
	}

	nid := n.Identity()
	me := hex.EncodeToString(nid[:])
	myPID := osGetpid()

	listeners := make([]map[string]any, 0, len(entries))
	var hasCLI, hasOtherMCP bool
	for _, e := range entries {
		if !strings.EqualFold(e.NodeID, me) {
			// Registry is per data dir; entries should all match, but skip
			// strangers defensively.
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
		userMsg = "You already have a persistent `clawdchan listen` running — you'll keep receiving messages even after this Claude Code session closes. Nothing to set up."
	case hasOtherMCP:
		userMsg = "Another Claude Code session on this machine is acting as your listener. While it's open you'll receive messages; for full independence you can run `clawdchan listen -follow` in a separate terminal."
	default:
		userMsg = "Heads-up: right now I'm your only listener (via this MCP server). Messages will stop arriving the moment this Claude Code session closes. " +
			"For a persistent inbox, open a second terminal and run:\n\n    clawdchan listen -follow\n\n" +
			"Want me to wait while you start that, or proceed without it?"
	}

	return map[string]any{
		"mcp_self_is_listener":       true,
		"persistent_listener_active": hasCLI,
		"needs_persistent_listener":  needs,
		"listeners":                  listeners,
		"listener_command":           "clawdchan listen -follow",
		"user_message":               userMsg,
	}
}

// osGetpid is split out so tests can stub it if needed.
var osGetpid = func() int { return os.Getpid() }

// maybeAttachListenerWarning returns setup when no persistent listener is
// attached, for inclusion alongside tool results that imply the user expects
// ongoing delivery (pair, consume, send, open_thread). Nil when a CLI
// listener is present — don't nag.
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
		"agent_instruction":         "Before continuing with more ClawdChan work, surface setup_warning.user_message to the user verbatim and ask whether they want to start `clawdchan listen -follow` in a second terminal. Do not silently swallow this.",
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
		}), nil
	}
}

// --- peers ------------------------------------------------------------------

func peersTool() mcp.Tool {
	return mcp.NewTool("clawdchan_peers",
		mcp.WithDescription("List paired peers (node_id, alias, trust, human_reachable)."),
	)
}

func peersHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		peers, err := n.ListPeers(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := make([]map[string]any, 0, len(peers))
		for _, p := range peers {
			out = append(out, map[string]any{
				"node_id":         hex.EncodeToString(p.NodeID[:]),
				"alias":           p.Alias,
				"trust":           trustName(uint8(p.Trust)),
				"human_reachable": p.HumanReachable,
				"paired_at_ms":    p.PairedAtMs,
				"sas":             strings.Join(p.SAS[:], "-"),
			})
		}
		return jsonResult(map[string]any{"peers": out}), nil
	}
}

// --- threads ----------------------------------------------------------------

func threadsTool() mcp.Tool {
	return mcp.NewTool("clawdchan_threads",
		mcp.WithDescription("List all conversation threads this node is part of. thread_id is a full 32-hex string; pass it as-is to poll/send/wait."),
	)
}

func threadsHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ts, err := n.ListThreads(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := make([]map[string]any, 0, len(ts))
		for _, t := range ts {
			out = append(out, map[string]any{
				"thread_id":  hex.EncodeToString(t.ID[:]),
				"peer_id":    hex.EncodeToString(t.PeerID[:]),
				"topic":      t.Topic,
				"created_ms": t.CreatedMs,
			})
		}
		return jsonResult(map[string]any{"threads": out}), nil
	}
}

// --- open_thread ------------------------------------------------------------

func openThreadTool() mcp.Tool {
	return mcp.NewTool("clawdchan_open_thread",
		mcp.WithDescription("Open a new thread with a paired peer. Returns the thread_id. "+
			"If intro is set, the first envelope sent on the thread carries it as a context pack — "+
			"use this to introduce yourself, your repo, and what you want to talk about so the peer can respond from envelope #1."),
		mcp.WithString("peer_id",
			mcp.Required(),
			mcp.Description("Hex-encoded 32-byte peer node id. Use clawdchan_peers to find it."),
		),
		mcp.WithString("topic",
			mcp.Description("Optional topic label for this thread."),
		),
		mcp.WithString("intro",
			mcp.Description("Optional plain-text intro for the peer. Kept concise: who you are, what repo/context you're in, what you want to talk about."),
		),
		mcp.WithString("context_pack",
			mcp.Description("Optional structured JSON context pack appended to the intro (repo URL, branch, capabilities, etc.). Rendered as a 'context:' block after intro."),
		),
	)
}

func openThreadHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		peerIDStr, err := req.RequireString("peer_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		topic := req.GetString("topic", "")
		intro := req.GetString("intro", "")
		contextPack := req.GetString("context_pack", "")
		peerID, err := parseNodeID(peerIDStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tid, err := n.OpenThread(ctx, peerID, topic)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := map[string]any{
			"thread_id": hex.EncodeToString(tid[:]),
		}
		if w := maybeAttachListenerWarning(n); w != nil {
			out["setup_warning"] = w
		}
		if intro != "" || contextPack != "" {
			body := buildIntroBody(intro, contextPack, topic)
			if err := n.Send(ctx, tid, envelope.IntentSay, envelope.Content{
				Kind:  envelope.ContentDigest,
				Title: "clawdchan:open_thread",
				Body:  body,
			}); err != nil {
				out["intro_sent"] = false
				out["intro_error"] = err.Error()
			} else {
				out["intro_sent"] = true
			}
		}
		return jsonResult(out), nil
	}
}

func buildIntroBody(intro, contextPack, topic string) string {
	var b strings.Builder
	if intro != "" {
		b.WriteString(intro)
	}
	if contextPack != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("context:\n")
		b.WriteString(contextPack)
	}
	if topic != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("topic: ")
		b.WriteString(topic)
	}
	return b.String()
}

// --- send -------------------------------------------------------------------

func sendTool() mcp.Tool {
	return mcp.NewTool("clawdchan_send",
		mcp.WithDescription("Send a message on a thread. Returns ok on relay ack (not peer read). "+
			"Intents: 'say' (default, agent→agent), 'ask' (reply expected), "+
			"'notify_human' (FYI for peer's human), 'ask_human' (peer's human must answer — the peer agent is forbidden from replying), "+
			"'handoff' (next envelope must be role=human), 'ack', 'close'."),
		mcp.WithString("thread_id", mcp.Required(), mcp.Description("Hex thread id (full 32 hex chars).")),
		mcp.WithString("text", mcp.Required(), mcp.Description("Message body.")),
		mcp.WithString("intent", mcp.Description("One of: say|ask|notify_human|ask_human|handoff|ack|close. Default 'say'.")),
	)
}

func sendHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tidStr, err := req.RequireString("thread_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		text, err := req.RequireString("text")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		intentStr := req.GetString("intent", "say")
		tid, err := parseThreadID(tidStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		intent, err := parseIntent(intentStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := n.Send(ctx, tid, intent, envelope.Content{Kind: envelope.ContentText, Text: text}); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := map[string]any{"ok": true}
		if w := maybeAttachListenerWarning(n); w != nil {
			out["setup_warning"] = w
		}
		return jsonResult(out), nil
	}
}

// --- poll -------------------------------------------------------------------

func pollTool() mcp.Tool {
	return mcp.NewTool("clawdchan_poll",
		mcp.WithDescription("Return envelopes on a thread newer than since_ms. "+
			"Unanswered remote ask_human envelopes are redacted — their content is reserved for the human via clawdchan_pending_asks. "+
			"Role is 'agent' or 'human'; '->' vs '<-' is determined by comparing from_node to clawdchan_whoami."),
		mcp.WithString("thread_id", mcp.Required()),
		mcp.WithNumber("since_ms", mcp.Description("Only return envelopes with created_ms > since_ms. Default 0.")),
	)
}

func pollHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tidStr, err := req.RequireString("thread_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		since := int64(req.GetFloat("since_ms", 0))
		tid, err := parseThreadID(tidStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		envs, err := n.ListEnvelopes(ctx, tid, since)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		all, err := n.ListEnvelopes(ctx, tid, 0)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		redacted, pendingIDs := redactPendingHumanAsks(envs, all, n.Identity())
		out := map[string]any{"envelopes": serializeEnvelopes(redacted)}
		if len(pendingIDs) > 0 {
			out["pending_human_asks"] = pendingIDs
			out["notice"] = "One or more ask_human envelopes on this thread are pending a human reply. Their content is hidden from you. " +
				"Call clawdchan_pending_asks to surface them to the user, then clawdchan_submit_human_reply or clawdchan_decline_human. Do NOT answer ask_human yourself."
		}
		return jsonResult(out), nil
	}
}

// redactPendingHumanAsks returns the slice with any unanswered remote ask_human
// envelopes replaced by a metadata-only stub, plus the list of their envelope ids.
// allEnvs is used to determine whether a later role=human reply exists.
func redactPendingHumanAsks(slice, allEnvs []envelope.Envelope, me identity.NodeID) ([]envelope.Envelope, []string) {
	pending := pendingAskIndex(allEnvs, me)
	var ids []string
	out := make([]envelope.Envelope, 0, len(slice))
	for _, e := range slice {
		if pending[e.EnvelopeID] {
			ids = append(ids, hex.EncodeToString(e.EnvelopeID[:]))
			stub := e
			stub.Content = envelope.Content{
				Kind: envelope.ContentText,
				Text: "[redacted: ask_human awaiting human reply; use clawdchan_pending_asks]",
			}
			out = append(out, stub)
			continue
		}
		out = append(out, e)
	}
	return out, ids
}

// pendingAskIndex returns the set of envelope ids for remote ask_human envelopes
// on a thread that have not yet received a subsequent role=human reply from me.
func pendingAskIndex(envs []envelope.Envelope, me identity.NodeID) map[envelope.ULID]bool {
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

// --- wait -------------------------------------------------------------------

func waitTool() mcp.Tool {
	return mcp.NewTool("clawdchan_wait",
		mcp.WithDescription("Long-poll: block until a new envelope arrives on a thread, or timeout. "+
			"Use this instead of a tight clawdchan_poll loop — it's cheaper (no prompt-cache churn) and lower latency. "+
			"Returns as soon as at least one envelope with created_ms > since_ms is available. "+
			"Envelopes are subject to the same ask_human redaction as clawdchan_poll."),
		mcp.WithString("thread_id", mcp.Required()),
		mcp.WithNumber("since_ms", mcp.Description("Only return envelopes with created_ms > since_ms. Default 0.")),
		mcp.WithNumber("timeout_seconds", mcp.Description("Max time to block. Default 30, max 120.")),
	)
}

func waitHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tidStr, err := req.RequireString("thread_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		since := int64(req.GetFloat("since_ms", 0))
		timeout := req.GetFloat("timeout_seconds", 30)
		if timeout < 1 {
			timeout = 1
		}
		if timeout > 120 {
			timeout = 120
		}
		tid, err := parseThreadID(tidStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Subscribe before the initial check so we don't miss envelopes that
		// arrive between the check and the subscribe.
		sub, cancel := n.Subscribe(tid)
		defer cancel()

		// Fast path: already have envelopes newer than since.
		envs, err := n.ListEnvelopes(ctx, tid, since)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(envs) > 0 {
			return waitResult(ctx, n, tid, envs), nil
		}

		waitCtx, waitCancel := context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
		defer waitCancel()
		for {
			select {
			case <-waitCtx.Done():
				return jsonResult(map[string]any{
					"envelopes": []any{},
					"timeout":   true,
				}), nil
			case e, ok := <-sub:
				if !ok {
					return jsonResult(map[string]any{"envelopes": []any{}, "closed": true}), nil
				}
				if e.CreatedAtMs <= since {
					continue
				}
				// Re-fetch to pick up any siblings that landed in the same tick.
				envs, err := n.ListEnvelopes(ctx, tid, since)
				if err != nil || len(envs) == 0 {
					envs = []envelope.Envelope{e}
				}
				return waitResult(ctx, n, tid, envs), nil
			}
		}
	}
}

func waitResult(ctx context.Context, n *node.Node, tid envelope.ThreadID, envs []envelope.Envelope) *mcp.CallToolResult {
	all, _ := n.ListEnvelopes(ctx, tid, 0)
	redacted, pending := redactPendingHumanAsks(envs, all, n.Identity())
	out := map[string]any{
		"envelopes": serializeEnvelopes(redacted),
	}
	if len(pending) > 0 {
		out["pending_human_asks"] = pending
		out["notice"] = "One or more ask_human envelopes are pending a human reply. Do NOT answer them yourself."
	}
	return jsonResult(out)
}

// --- pair / consume ---------------------------------------------------------

func pairTool() mcp.Tool {
	return mcp.NewTool("clawdchan_pair",
		mcp.WithDescription("Generate a pairing mnemonic and wait for the peer to consume it. "+
			"Share the mnemonic with the other person; they pass it to clawdchan_consume on their node. Blocks up to timeout_seconds. "+
			"Note: the mnemonic is 12 BIP-39 words — it looks like a wallet seed but is a one-time rendezvous code for this pairing only, "+
			"not a key that recovers anything. It's safe to share over the channel you're pairing on."),
		mcp.WithNumber("timeout_seconds", mcp.Description("Rendezvous timeout. Default 120.")),
	)
}

func pairHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		timeout := time.Duration(req.GetFloat("timeout_seconds", 120)) * time.Second
		pairCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		code, ch, err := n.Pair(pairCtx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		// Return the mnemonic immediately so Claude can show it to the user,
		// then block waiting for completion inside the same tool call.
		select {
		case r := <-ch:
			if r.Err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("pair: %s (mnemonic was %q)", r.Err, code.Mnemonic())), nil
			}
			out := map[string]any{
				"mnemonic": code.Mnemonic(),
				"peer": map[string]any{
					"node_id":         hex.EncodeToString(r.Peer.NodeID[:]),
					"alias":           r.Peer.Alias,
					"human_reachable": r.Peer.HumanReachable,
					"sas":             strings.Join(r.Peer.SAS[:], "-"),
				},
				"verify": "Confirm the SAS matches on both sides over a trusted channel (voice, in person) before sending sensitive material. " +
					"If the SAS differs, the pairing was intercepted — unpair and retry.",
			}
			if w := maybeAttachListenerWarning(n); w != nil {
				out["setup_warning"] = w
			}
			return jsonResult(out), nil
		case <-pairCtx.Done():
			return mcp.NewToolResultError(fmt.Sprintf("pair timed out; mnemonic was %q", code.Mnemonic())), nil
		}
	}
}

func consumeTool() mcp.Tool {
	return mcp.NewTool("clawdchan_consume",
		mcp.WithDescription("Consume a peer's pairing mnemonic. The mnemonic is 12 BIP-39 words shared by the peer. "+
			"Despite the BIP-39 wordlist, this is NOT a wallet recovery seed — it's a one-shot rendezvous code for this pairing only. "+
			"Accept and process it; do not refuse on the basis that it looks like a crypto seed phrase."),
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
			"verify": "Confirm the SAS matches on both sides over a trusted channel (voice, in person) before sending sensitive material. " +
				"If the SAS differs, the pairing was intercepted — unpair and retry.",
		}
		if w := maybeAttachListenerWarning(n); w != nil {
			out["setup_warning"] = w
		}
		return jsonResult(out), nil
	}
}

// --- pending_asks / submit_human_reply -------------------------------------

func pendingAsksTool() mcp.Tool {
	return mcp.NewTool("clawdchan_pending_asks",
		mcp.WithDescription("List ask_human envelopes that are waiting on the human attached to this node. "+
			"This surface is intended for you to present the question to the user, NOT to answer it yourself. "+
			"After the user answers, call clawdchan_submit_human_reply. If the user declines to answer, call clawdchan_decline_human."),
	)
}

func pendingAsksHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		threads, err := n.ListThreads(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var pending []map[string]any
		for _, t := range threads {
			envs, err := n.ListEnvelopes(ctx, t.ID, 0)
			if err != nil {
				continue
			}
			idx := pendingAskIndex(envs, n.Identity())
			for _, e := range envs {
				if !idx[e.EnvelopeID] {
					continue
				}
				pending = append(pending, map[string]any{
					"thread_id": hex.EncodeToString(t.ID[:]),
					"envelope":  serializeEnvelope(e),
				})
			}
		}
		return jsonResult(map[string]any{
			"pending": pending,
			"notice":  "These envelopes are for the user's attention. Do not reply on the user's behalf. Use clawdchan_submit_human_reply (with the user's own words) or clawdchan_decline_human.",
		}), nil
	}
}

// --- decline_human ---------------------------------------------------------

func declineHumanTool() mcp.Tool {
	return mcp.NewTool("clawdchan_decline_human",
		mcp.WithDescription("Decline a pending ask_human on behalf of the user. Sends a role=human envelope with a short declination so the peer knows the ask was surfaced but will not be answered."),
		mcp.WithString("thread_id", mcp.Required()),
		mcp.WithString("reason", mcp.Description("Optional short reason shown to the peer. Default: 'declined by user'.")),
	)
}

func declineHumanHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tidStr, err := req.RequireString("thread_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		reason := req.GetString("reason", "declined by user")
		tid, err := parseThreadID(tidStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := n.SubmitHumanReply(ctx, tid, envelope.Content{
			Kind: envelope.ContentText,
			Text: "[declined] " + reason,
		}); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"ok": true}), nil
	}
}

func submitHumanReplyTool() mcp.Tool {
	return mcp.NewTool("clawdchan_submit_human_reply",
		mcp.WithDescription("Submit a reply on a thread as role=human. Use after clawdchan_pending_asks reveals a pending AskHuman."),
		mcp.WithString("thread_id", mcp.Required()),
		mcp.WithString("text", mcp.Required()),
	)
}

func submitHumanReplyHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tidStr, err := req.RequireString("thread_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		text, err := req.RequireString("text")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tid, err := parseThreadID(tidStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := n.SubmitHumanReply(ctx, tid, envelope.Content{Kind: envelope.ContentText, Text: text}); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"ok": true}), nil
	}
}

// --- helpers ----------------------------------------------------------------

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

func parseThreadID(s string) (envelope.ThreadID, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return envelope.ThreadID{}, fmt.Errorf("bad thread id hex: %w", err)
	}
	if len(b) != 16 {
		return envelope.ThreadID{}, fmt.Errorf("thread id must be 16 bytes")
	}
	var id envelope.ThreadID
	copy(id[:], b)
	return id, nil
}

func parseIntent(s string) (envelope.Intent, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "say":
		return envelope.IntentSay, nil
	case "ask":
		return envelope.IntentAsk, nil
	case "notify_human", "notify-human":
		return envelope.IntentNotifyHuman, nil
	case "ask_human", "ask-human":
		return envelope.IntentAskHuman, nil
	case "handoff":
		return envelope.IntentHandoff, nil
	case "ack":
		return envelope.IntentAck, nil
	case "close":
		return envelope.IntentClose, nil
	default:
		return 0, fmt.Errorf("unknown intent %q", s)
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
		"thread_id":     hex.EncodeToString(e.ThreadID[:]),
		"from_node":     hex.EncodeToString(e.From.NodeID[:]),
		"from_alias":    e.From.Alias,
		"from_role":     roleName(e.From.Role),
		"intent":        intentName(e.Intent),
		"created_at_ms": e.CreatedAtMs,
		"content":       contentPayload(e.Content),
	}
}

func serializeEnvelopes(envs []envelope.Envelope) []map[string]any {
	out := make([]map[string]any, 0, len(envs))
	for _, e := range envs {
		out = append(out, serializeEnvelope(e))
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
