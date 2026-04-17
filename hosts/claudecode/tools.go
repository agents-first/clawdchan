package claudecode

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
)

// RegisterTools registers the full ClawdChan MCP tool surface on s, bound to n.
func RegisterTools(s *server.MCPServer, n *node.Node) {
	s.AddTool(whoamiTool(), whoamiHandler(n))
	s.AddTool(peersTool(), peersHandler(n))
	s.AddTool(threadsTool(), threadsHandler(n))
	s.AddTool(openThreadTool(), openThreadHandler(n))
	s.AddTool(sendTool(), sendHandler(n))
	s.AddTool(pollTool(), pollHandler(n))
	s.AddTool(pairTool(), pairHandler(n))
	s.AddTool(consumeTool(), consumeHandler(n))
	s.AddTool(pendingAsksTool(), pendingAsksHandler(n))
	s.AddTool(submitHumanReplyTool(), submitHumanReplyHandler(n))
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
		mcp.WithDescription("List all conversation threads this node is part of."),
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
		mcp.WithDescription("Open a new thread with a paired peer. Returns the thread_id."),
		mcp.WithString("peer_id",
			mcp.Required(),
			mcp.Description("Hex-encoded 32-byte peer node id. Use clawdchan_peers to find it."),
		),
		mcp.WithString("topic",
			mcp.Description("Optional topic label for this thread."),
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
		peerID, err := parseNodeID(peerIDStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tid, err := n.OpenThread(ctx, peerID, topic)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"thread_id": hex.EncodeToString(tid[:]),
		}), nil
	}
}

// --- send -------------------------------------------------------------------

func sendTool() mcp.Tool {
	return mcp.NewTool("clawdchan_send",
		mcp.WithDescription("Send a message on a thread. Intent defaults to 'say'. "+
			"Use 'notify_human' to drop an FYI on the peer's human; use 'ask_human' to request their human's explicit input."),
		mcp.WithString("thread_id", mcp.Required(), mcp.Description("Hex thread id.")),
		mcp.WithString("text", mcp.Required(), mcp.Description("Message body.")),
		mcp.WithString("intent", mcp.Description("One of: say|ask|notify_human|ask_human|handoff. Default 'say'.")),
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
		return jsonResult(map[string]any{"ok": true}), nil
	}
}

// --- poll -------------------------------------------------------------------

func pollTool() mcp.Tool {
	return mcp.NewTool("clawdchan_poll",
		mcp.WithDescription("Return envelopes on a thread newer than since_ms."),
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
		return jsonResult(map[string]any{"envelopes": serializeEnvelopes(envs)}), nil
	}
}

// --- pair / consume ---------------------------------------------------------

func pairTool() mcp.Tool {
	return mcp.NewTool("clawdchan_pair",
		mcp.WithDescription("Generate a pairing mnemonic and wait for the peer to consume it. "+
			"Share the mnemonic with the other person; they pass it to clawdchan_consume on their node. Blocks up to timeout_seconds."),
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
			return jsonResult(map[string]any{
				"mnemonic": code.Mnemonic(),
				"peer": map[string]any{
					"node_id":         hex.EncodeToString(r.Peer.NodeID[:]),
					"alias":           r.Peer.Alias,
					"human_reachable": r.Peer.HumanReachable,
					"sas":             strings.Join(r.Peer.SAS[:], "-"),
				},
			}), nil
		case <-pairCtx.Done():
			return mcp.NewToolResultError(fmt.Sprintf("pair timed out; mnemonic was %q", code.Mnemonic())), nil
		}
	}
}

func consumeTool() mcp.Tool {
	return mcp.NewTool("clawdchan_consume",
		mcp.WithDescription("Consume a peer's pairing mnemonic (12 words)."),
		mcp.WithString("mnemonic", mcp.Required()),
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

// --- pending_asks / submit_human_reply -------------------------------------

func pendingAsksTool() mcp.Tool {
	return mcp.NewTool("clawdchan_pending_asks",
		mcp.WithDescription("List AskHuman envelopes that have not yet received a role=human reply on their thread."),
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
			// Find the latest AskHuman from a remote peer with no subsequent
			// human reply.
			for i := len(envs) - 1; i >= 0; i-- {
				e := envs[i]
				if e.Intent != envelope.IntentAskHuman {
					continue
				}
				if e.From.NodeID == n.Identity() {
					break // we asked, they're pending on the other side
				}
				answered := false
				for j := i + 1; j < len(envs); j++ {
					if envs[j].From.Role == envelope.RoleHuman && envs[j].From.NodeID == n.Identity() {
						answered = true
						break
					}
				}
				if !answered {
					pending = append(pending, map[string]any{
						"thread_id": hex.EncodeToString(t.ID[:]),
						"envelope":  serializeEnvelope(e),
					})
				}
				break
			}
		}
		return jsonResult(map[string]any{"pending": pending}), nil
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
