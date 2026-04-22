package openclaw

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/agents-first/clawdchan/core/node"
	"github.com/agents-first/clawdchan/hosts"
	"github.com/agents-first/clawdchan/internal/listenerreg"
)

// ToolHandler is the bridge's native tool handler signature. params
// is the decoded JSON object from the tool call; returns the JSON
// result string or an error surfaced as a tool error response.
type ToolHandler func(ctx context.Context, params map[string]any) (string, error)

// RegisterTools registers the ClawdChan tool surface on br, bound to n.
//
// The full tool surface lives in the hosts package — this file is a
// thin adapter that wraps each hosts.Handler as a bridge ToolHandler.
// The bridge is schema-less (it dispatches by name), so ToolSpec's
// param and description metadata is used only for the agent-facing
// guide (CLAWDCHAN_GUIDE.md) generated elsewhere.
func RegisterTools(br *Bridge, n *node.Node) {
	for _, reg := range hosts.All(n, buildSetupStatus) {
		br.RegisterTool(reg.Spec.Name, wrap(reg.Handler))
	}
}

// wrap adapts a hosts.Handler (params map → result map) into the
// bridge's ToolHandler signature (params map → JSON string).
func wrap(h hosts.Handler) ToolHandler {
	return func(ctx context.Context, params map[string]any) (string, error) {
		result, err := h(ctx, params)
		if err != nil {
			return "", err
		}
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}

// buildSetupStatus inspects the listener registry and returns a
// structured blob. In OpenClaw host mode the daemon always owns the
// relay link — the agent doesn't need to start anything.
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
