package openclaw

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/policy"
	"github.com/vMaroon/ClawdChan/hosts"
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
// has agent-dispatch configured.
func WithDispatchEnabled(enabled bool) Option {
	return func(o *regOpts) { o.dispatchEnabled = enabled }
}

// --- toolkit ----------------------------------------------------------------

func toolkitHandler(n *node.Node, opts *regOpts) ToolHandler {
	return func(ctx context.Context, _ map[string]any) (string, error) {
		setup := buildSetupStatus(n)
		dispatchEnabled := opts != nil && opts.dispatchEnabled
		return jsonResult(hosts.BuildToolkitBase(n, setup, dispatchEnabled))
	}
}

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
		out, err := hosts.BuildPeersList(ctx, n)
		if err != nil {
			return "", err
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
		peerID, err := hosts.ResolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return "", err
		}
		intent, err := hosts.ParseMessageIntent(getString(params, "intent", "say"))
		if err != nil {
			return "", err
		}
		collab := getBool(params, "collab", false)
		tid, err := hosts.ResolveOrOpenThread(ctx, n, peerID)
		if err != nil {
			return "", err
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
			return "", err
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
		return jsonResult(out)
	}
}

// --- inbox ------------------------------------------------------------------

func inboxHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, params map[string]any) (string, error) {
		since := int64(getFloat(params, "since_ms", 0))
		wait := getFloat(params, "wait_seconds", 0)
		if wait < 0 {
			wait = 0
		}
		if wait > hosts.MaxInboxWaitSeconds {
			wait = hosts.MaxInboxWaitSeconds
		}
		headersOnly := strings.EqualFold(strings.TrimSpace(getString(params, "include", "full")), "headers")
		notesSeen := getBool(params, "notes_seen", false)

		deadline := time.Now().Add(time.Duration(wait * float64(time.Second)))
		const pollInterval = 400 * time.Millisecond
		for {
			out, anyTraffic, hasPending, hasCollab, nowMs, err := hosts.CollectInbox(ctx, n, since, headersOnly)
			if err != nil {
				return "", err
			}
			if anyTraffic || wait == 0 || !time.Now().Before(deadline) {
				resp := map[string]any{
					"now_ms": nowMs,
					"peers":  out,
				}
				if !notesSeen {
					resp["notes"] = hosts.InboxNotes(hasPending, hasCollab)
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

// --- subagent_await (live collab primitive) ---------------------------------

func awaitHandler(n *node.Node) ToolHandler {
	return func(ctx context.Context, params map[string]any) (string, error) {
		peerStr, err := requireString(params, "peer_id")
		if err != nil {
			return "", err
		}
		peerID, err := hosts.ResolvePeerRef(ctx, n, peerStr)
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

		tid, err := hosts.ResolveOrOpenThread(ctx, n, peerID)
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
				serialized, pendingIDs := hosts.BuildAwaitPayload(ctx, n, tid, fresh)
				out := map[string]any{"envelopes": serialized, "now_ms": time.Now().UnixMilli()}
				if len(pendingIDs) > 0 {
					out["pending_human_asks"] = pendingIDs
					out["notice"] = "One or more ask_human envelopes are pending a human reply. Do not answer them yourself."
				}
				return jsonResult(out)
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
		peerID, err := hosts.ResolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return "", err
		}
		tid, err := hosts.FindThreadWithPendingAsk(ctx, n, peerID)
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
		peerID, err := hosts.ResolvePeerRef(ctx, n, peerStr)
		if err != nil {
			return "", err
		}
		tid, err := hosts.FindThreadWithPendingAsk(ctx, n, peerID)
		if err != nil {
			return "", err
		}
		if err := n.SubmitHumanReply(ctx, tid, envelope.Content{Kind: envelope.ContentText, Text: "[declined] " + reason}); err != nil {
			return "", err
		}
		return jsonResult(map[string]any{"ok": true})
	}
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
		go func() { defer cancel(); <-ch }()
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

// --- peer management (rename / revoke / remove) -----------------------------

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
		peerID, err := hosts.ResolvePeerRef(ctx, n, ref)
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
		peerID, err := hosts.ResolvePeerRef(ctx, n, ref)
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
		peerID, err := hosts.ResolvePeerRef(ctx, n, ref)
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

// --- helpers ----------------------------------------------------------------

func jsonResult(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

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
