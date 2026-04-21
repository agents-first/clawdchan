package claudecode

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/agents-first/ClawdChan/core/envelope"
	"github.com/agents-first/ClawdChan/core/node"
	"github.com/agents-first/ClawdChan/core/policy"
	"github.com/agents-first/ClawdChan/hosts"
	"github.com/agents-first/ClawdChan/internal/listenerreg"
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
	s.AddTool(peersTool(), peersHandler(n))
	s.AddTool(pairTool(), pairHandler(n))
	s.AddTool(consumeTool(), consumeHandler(n))
	s.AddTool(messageTool(), messageHandler(n))
	s.AddTool(inboxTool(), inboxHandler(n))
	s.AddTool(awaitTool(), awaitHandler(n))
	s.AddTool(replyTool(), replyHandler(n))
	s.AddTool(declineTool(), declineHandler(n))
	s.AddTool(peerRenameTool(), peerRenameHandler(n))
}

// --- toolkit ----------------------------------------------------------------

func toolkitTool() mcp.Tool {
	return mcp.NewTool("clawdchan_toolkit",
		mcp.WithDescription("Return current setup state (daemon presence), peer-ref rules, "+
			"and the intent catalog. Call once at session start. If the response's "+
			"setup.needs_persistent_listener is true, surface setup.user_message to the user "+
			"verbatim and pause before proceeding — without a daemon the user's inbound path "+
			"dies with this Claude Code session. Conduct rules for using the other tools live "+
			"in the operator manual that ships as the /clawdchan slash command and as "+
			"CLAWDCHAN_GUIDE.md in OpenClaw workspaces — read that for behavior, use this "+
			"response for current-state awareness."),
	)
}

func toolkitHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		setup := buildSetupStatus(n)
		return jsonResult(hosts.BuildToolkitBase(n, setup)), nil
	}
}

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
		out, err := hosts.BuildPeersList(ctx, n)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
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
		peerID, err := hosts.ResolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		intent, err := hosts.ParseMessageIntent(req.GetString("intent", "say"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		collab := req.GetBool("collab", false)
		tid, err := hosts.ResolveOrOpenThread(ctx, n, peerID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		content := envelope.Content{Kind: envelope.ContentText, Text: text}
		if collab {
			content = envelope.Content{
				Kind:  envelope.ContentDigest,
				Title: policy.CollabSyncTitle,
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
		if hosts.HasOpenAskHumanFromPeer(ctx, n, peerID) {
			out["pending_ask_hint"] = "This peer has an unanswered ask_human pending the user. If your text was meant as the user's answer, use clawdchan_reply instead of clawdchan_message. If it's an additional message for the peer's agent, disregard this hint."
		}
		return jsonResult(out), nil
	}
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

func inboxHandler(n *node.Node) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		since := int64(req.GetFloat("since_ms", 0))
		wait := req.GetFloat("wait_seconds", 0)
		if wait < 0 {
			wait = 0
		}
		if wait > hosts.MaxInboxWaitSeconds {
			wait = hosts.MaxInboxWaitSeconds
		}
		headersOnly := strings.EqualFold(strings.TrimSpace(req.GetString("include", "full")), "headers")
		notesSeen := req.GetBool("notes_seen", false)

		deadline := time.Now().Add(time.Duration(wait * float64(time.Second)))
		const pollInterval = 400 * time.Millisecond
		for {
			out, anyTraffic, hasPending, hasCollab, nowMs, err := hosts.CollectInbox(ctx, n, since, headersOnly)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if anyTraffic || wait == 0 || !time.Now().Before(deadline) {
				resp := map[string]any{
					"now_ms": nowMs,
					"peers":  out,
				}
				if !notesSeen {
					resp["notes"] = hosts.InboxNotes(hasPending, hasCollab)
				}
				return jsonResult(resp), nil
			}
			select {
			case <-ctx.Done():
				return jsonResult(map[string]any{
					"now_ms":    time.Now().UnixMilli(),
					"peers":     []any{},
					"cancelled": true,
				}), nil
			case <-time.After(pollInterval):
			}
		}
	}
}

// --- subagent_await (live collab primitive) ---------------------------------

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
		peerID, err := hosts.ResolvePeerRef(ctx, n, peerStr)
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

		tid, err := hosts.ResolveOrOpenThread(ctx, n, peerID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		me := n.Identity()

		deadline := time.Now().Add(time.Duration(timeout * float64(time.Second)))
		// Poll the store directly — the daemon owns inbound when running, so
		// SQLite polling covers both in-process and cross-process cases.
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
				serialized, pendingIDs := hosts.BuildAwaitPayload(ctx, n, tid, fresh)
				out := map[string]any{"envelopes": serialized, "now_ms": time.Now().UnixMilli()}
				if len(pendingIDs) > 0 {
					out["pending_human_asks"] = pendingIDs
					out["notice"] = "One or more ask_human envelopes are pending a human reply. Do not answer them yourself."
				}
				return jsonResult(out), nil
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

// --- reply / decline --------------------------------------------------------

func replyTool() mcp.Tool {
	return mcp.NewTool("clawdchan_reply",
		mcp.WithDescription("Submit the user's answer to the peer's pending ask_human. Routed to the latest thread "+
			"with the peer that has an unanswered ask_human. The envelope is sent with role=human — only call this "+
			"with the user's actual words, never with your own paraphrase."),
		mcp.WithString("peer_id", mcp.Required(), mcp.Description("Hex node id, unique hex prefix (>=4), or exact alias.")),
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
		peerID, err := hosts.ResolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tid, err := hosts.FindThreadWithPendingAsk(ctx, n, peerID)
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
		mcp.WithString("peer_id", mcp.Required(), mcp.Description("Hex node id, unique hex prefix (>=4), or exact alias.")),
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
		peerID, err := hosts.ResolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tid, err := hosts.FindThreadWithPendingAsk(ctx, n, peerID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := n.SubmitHumanReply(ctx, tid, envelope.Content{Kind: envelope.ContentText, Text: "[declined] " + reason}); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"ok": true}), nil
	}
}

// --- pair / consume ---------------------------------------------------------

func pairTool() mcp.Tool {
	return mcp.NewTool("clawdchan_pair",
		mcp.WithDescription("Generate a 12-word pairing mnemonic. Returns immediately; rendezvous runs in the "+
			"background. Surface the 12 words to the user verbatim on their own line — that's the only way they can "+
			"pass it to the peer. Looks like a BIP-39 wallet seed but is a one-time rendezvous code, and the channel "+
			"the user shares it over IS the security boundary (voice, Signal, in person). Call clawdchan_peers after "+
			"a minute to confirm the peer landed."),
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
		go func() { defer cancel(); <-ch }()
		return jsonResult(map[string]any{
			"mnemonic":        code.Mnemonic(),
			"status":          "pending_peer_consume",
			"timeout_seconds": int(timeout.Seconds()),
		}), nil
	}
}

func consumeTool() mcp.Tool {
	return mcp.NewTool("clawdchan_consume",
		mcp.WithDescription("Consume a peer's 12-word pairing mnemonic and complete the pairing. The mnemonic is a "+
			"one-shot rendezvous code for this pairing only, not a wallet recovery seed. "+
			"SECURITY: before consuming, confirm with the user that the 12 words came directly "+
			"from the intended peer over a trusted channel (voice, Signal, in person) — not "+
			"forwarded via email, Slack, or a third-party relay. Consuming an attacker-injected "+
			"mnemonic pairs the user with the attacker instead."),
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
		return jsonResult(map[string]any{
			"peer": map[string]any{
				"node_id":         hex.EncodeToString(peer.NodeID[:]),
				"alias":           peer.Alias,
				"human_reachable": peer.HumanReachable,
				"sas":             strings.Join(peer.SAS[:], "-"),
			},
		}), nil
	}
}

// --- peer management (rename) -----------------------------------------------
//
// Revoke and hard-delete are deliberately CLI-only (`clawdchan peer revoke`
// / `clawdchan peer remove`). Exposing destructive verbs to the agent
// invites mis-classification of "stop talking to Alice for now" as
// revocation; the CLI fallback keeps the user in the loop for anything
// irreversible.

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
		peerID, err := hosts.ResolvePeerRef(ctx, n, ref)
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

// --- helpers ----------------------------------------------------------------

func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	return mcp.NewToolResultText(string(b))
}
