package claudecode

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/agents-first/clawdchan/core/node"
	"github.com/agents-first/clawdchan/hosts"
	"github.com/agents-first/clawdchan/internal/listenerreg"
)

// RegisterTools registers the ClawdChan MCP surface on s, bound to n.
//
// The full tool surface (schemas, handlers, helpers) lives in the
// hosts package — this file is a thin adapter that translates
// hosts.ToolSpec into mark3labs/mcp-go's mcp.Tool and wraps
// hosts.Handler into server.ToolHandlerFunc. Adding or removing a
// tool happens in hosts/; this adapter picks it up automatically.
func RegisterTools(s *server.MCPServer, n *node.Node) {
	for _, reg := range hosts.All(n, buildSetupStatus) {
		s.AddTool(toMCPTool(reg.Spec), wrap(reg.Handler))
	}
	for _, reg := range hosts.CollabSessionTools(n) {
		s.AddTool(toMCPTool(reg.Spec), wrap(reg.Handler))
	}
}

// toMCPTool converts a hosts.ToolSpec into the mcp-go native tool
// definition. Parameters preserve required-ness and their description;
// types map 1:1 to mcp.WithString / WithNumber / WithBoolean helpers.
func toMCPTool(spec hosts.ToolSpec) mcp.Tool {
	opts := []mcp.ToolOption{mcp.WithDescription(spec.Description)}
	for _, p := range spec.Params {
		opts = append(opts, paramOption(p))
	}
	return mcp.NewTool(spec.Name, opts...)
}

func paramOption(p hosts.ParamSpec) mcp.ToolOption {
	var propOpts []mcp.PropertyOption
	propOpts = append(propOpts, mcp.Description(p.Description))
	if p.Required {
		propOpts = append(propOpts, mcp.Required())
	}
	switch p.Type {
	case hosts.ParamString:
		return mcp.WithString(p.Name, propOpts...)
	case hosts.ParamNumber:
		return mcp.WithNumber(p.Name, propOpts...)
	case hosts.ParamBoolean:
		return mcp.WithBoolean(p.Name, propOpts...)
	default:
		return mcp.WithString(p.Name, propOpts...)
	}
}

// wrap translates a hosts.Handler into a server.ToolHandlerFunc.
// Errors go to mcp.NewToolResultError (the mcp-go convention); the
// success payload is JSON-encoded text.
func wrap(h hosts.Handler) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		params := req.GetArguments()
		if params == nil {
			params = map[string]any{}
		}
		result, err := h(ctx, params)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		b, mErr := json.MarshalIndent(result, "", "  ")
		if mErr != nil {
			return mcp.NewToolResultError(mErr.Error()), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

// buildSetupStatus inspects the listener registry and returns a
// structured blob plus a ready-to-speak user_message. The daemon is
// the recommended listener; the MCP server is a fallback that dies
// with the CC session.
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
