package main

import (
	"flag"
	"strings"
)

// agentWiring describes one MCP-capable agent host (Claude Code, Gemini,
// Codex, Copilot). The setup / doctor / uninstall commands iterate over
// this registry so adding a new host is a single new entry.
//
// Each agent owns its own config format and scope model — CC and Gemini
// have both user and project scopes, Codex and Copilot only expose a
// user-scope config file that's officially documented. Trust / allow
// mechanics are per-agent too:
//
//   - Claude Code — separate settings.json with permissions.allow; the
//     only agent with two distinct scope prompts (MCP + permissions).
//   - Gemini      — `trust: true` on the MCP server entry itself.
//   - Codex       — `default_tool_approval_mode = "approve"` on the
//     `[mcp_servers.<name>]` TOML block.
//   - Copilot     — `tools: ["*"]` per-server allowlist; no separate trust.
type agentWiring struct {
	key         string // "cc" | "gemini" | "codex" | "copilot"
	flagName    string // CLI flag stem: -cc, -gemini, -codex, -copilot
	displayName string // Human label for prompts / headers
	defaultOn   bool   // True for Claude Code only — the v0 default.

	// scopeFlags names the per-agent sub-scope flags registered by the
	// orchestrator. For CC this is ["mcp", "perm"]; for the others it's
	// just ["mcp"]. Each entry turns into a CLI flag of the form
	// -<flagName>-<scope>-scope.
	scopeFlags []string

	// setup wires the agent for the current user. scopes is keyed by
	// the entries in scopeFlags. An empty scope means "prompt the user
	// (TTY) or default to 'user' (-y / non-TTY)".
	setup func(yes bool, scopes map[string]string) error

	// doctorReport returns zero or more status lines for `clawdchan
	// doctor`. Empty slice means "this agent isn't configured / nothing
	// to report" — doctor stays quiet rather than warning about every
	// host the user doesn't use.
	doctorReport func() []string

	// uninstallHints returns the manual-cleanup commands to print in
	// `clawdchan uninstall`. Empty slice if the hint is not
	// conditionally relevant (e.g. the user never wired this agent).
	uninstallHints func() []string
}

// allAgents is the registry of MCP-capable hosts the setup flow knows
// about. Order is preserved in prompts and headers.
func allAgents() []*agentWiring {
	return []*agentWiring{
		ccAgent(),
		geminiAgent(),
		codexAgent(),
		copilotAgent(),
		cursorAgent(),
		vscodeAgent(),
		antigravityAgent(),
	}
}

// agentFlags registers -<flagName> and -<flagName>-<scope>-scope flags
// for every agent on the provided FlagSet. Returns maps the caller
// reads after fs.Parse: selection[key] → raw flag value (yes|no|""),
// scopeFlags[key][scope] → raw flag value.
func agentFlags(fs *flag.FlagSet, agents []*agentWiring) (selection map[string]*string, scopes map[string]map[string]*string) {
	selection = map[string]*string{}
	scopes = map[string]map[string]*string{}
	for _, a := range agents {
		selection[a.key] = fs.String(a.flagName, "", describeSelectFlag(a))
		scopes[a.key] = map[string]*string{}
		for _, scope := range a.scopeFlags {
			name := a.flagName + "-" + scope + "-scope"
			scopes[a.key][scope] = fs.String(name, "", describeScopeFlag(a, scope))
		}
	}
	return selection, scopes
}

func describeSelectFlag(a *agentWiring) string {
	def := "no"
	if a.defaultOn {
		def = "yes"
	}
	return "configure " + a.displayName + " integration (yes|no). Default: interactive; " + def + " in -y mode."
}

func describeScopeFlag(a *agentWiring, scope string) string {
	switch scope {
	case "mcp":
		return "where to register clawdchan-mcp for " + a.displayName + ": " + strings.Join(mcpScopeChoices(a), " | ") + " | skip"
	case "perm":
		return "where to write clawdchan permissions for " + a.displayName + ": user | project | project-local | skip"
	default:
		return "where to apply " + scope + " config for " + a.displayName
	}
}

// mcpScopeChoices returns the MCP-registration scope names the agent
// supports. Gemini and CC support user+project; the rest are user-only
// (per their upstream docs).
func mcpScopeChoices(a *agentWiring) []string {
	switch a.key {
	case "cc", "gemini":
		return []string{"user", "project"}
	default:
		// Codex, Copilot, Cursor, VS Code, and Antigravity are user-scope only.
		// Cursor specifically ignores project-level .cursor/mcp.json — only
		// ~/.cursor/mcp.json loads. VS Code and Antigravity also support a
		// workspace file (.vscode/mcp.json, etc.) but registering at user
		// scope means a single setup run covers every project the user opens.
		return []string{"user"}
	}
}

// parseBoolFlag normalizes a user-supplied yes|no-ish flag value.
// Unknown / empty strings fall back to dflt.
func parseBoolFlag(s string, dflt bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "y", "true", "1", "on":
		return true
	case "no", "n", "false", "0", "off":
		return false
	default:
		return dflt
	}
}
