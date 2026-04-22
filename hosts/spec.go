package hosts

import (
	"context"
	"fmt"

	"github.com/agents-first/clawdchan/core/node"
)

// SurfaceVersion is reported by clawdchan_toolkit so agents (and
// debuggers tailing the wire) can tell which revision of the tool
// surface they're looking at. Bump when the surface changes shape.
const SurfaceVersion = "0.5"

// ParamType enumerates the scalar shapes we describe to a host's
// native schema format. ClawdChan tool args are either a peer ref
// (string), a body (string), or a bounded numeric / boolean flag.
type ParamType string

const (
	ParamString  ParamType = "string"
	ParamNumber  ParamType = "number"
	ParamBoolean ParamType = "boolean"
)

// ParamSpec describes one tool argument.
type ParamSpec struct {
	Name        string
	Type        ParamType
	Description string
	Required    bool
}

// ToolSpec is the schema for one tool. A host adapter turns this into
// whatever native description format it needs; OpenClaw's bridge is
// schema-less and uses only the Name.
type ToolSpec struct {
	Name        string
	Description string
	Params      []ParamSpec
}

// Handler is the host-agnostic tool handler. Hosts decode their
// native tool call into a map[string]any, invoke the handler, then
// re-encode the returned map (typically as JSON). A non-nil error
// is translated to the host's native error shape.
type Handler func(ctx context.Context, params map[string]any) (map[string]any, error)

// SetupBuilder lets each host inject its own setup-status block into
// clawdchan_toolkit's response. Claude Code's version nudges the user
// to start `clawdchan daemon`; OpenClaw's reports that the gateway
// owns the relay link.
type SetupBuilder func(n *node.Node) map[string]any

// Registration ties a spec to the handler that implements it.
type Registration struct {
	Spec    ToolSpec
	Handler Handler
}

// All returns the canonical four-tool surface: clawdchan_toolkit,
// clawdchan_pair, clawdchan_message, clawdchan_inbox.
func All(n *node.Node, sb SetupBuilder) []Registration {
	return []Registration{
		{Spec: toolkitSpec(), Handler: toolkitHandler(n, sb)},
		{Spec: pairSpec(), Handler: pairHandler(n)},
		{Spec: messageSpec(), Handler: messageHandler(n)},
		{Spec: inboxSpec(), Handler: inboxHandler(n)},
	}
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
