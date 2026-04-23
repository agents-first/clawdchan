package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// copilotAgent wires the github/copilot-cli (agentic) host.
//
// Config file: ~/.copilot/mcp-config.json. The official "Add MCP
// servers" page documents only the user-scope file — it does not
// describe a project-scope equivalent, so setup exposes user-only.
//
// Server entry is shaped:
//
//	{ "type": "local", "command": ..., "tools": ["*"] }
//
// The "tools" key is the per-server allowlist; "*" is the closest
// Copilot analog to a trust flag.
//
// Reference: https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/add-mcp-servers
func copilotAgent() *agentWiring {
	return &agentWiring{
		key:            "copilot",
		flagName:       "copilot",
		displayName:    "GitHub Copilot CLI",
		defaultOn:      false,
		scopeFlags:     []string{"mcp"},
		setup:          setupCopilot,
		doctorReport:   doctorCopilot,
		uninstallHints: uninstallHintsCopilot,
	}
}

func setupCopilot(yes bool, scopes map[string]string) error {
	scope := strings.ToLower(strings.TrimSpace(scopes["mcp"]))
	if scope == "" && (yes || !stdinIsTTY()) {
		scope = "user"
	}
	if scope == "" {
		fmt.Println("    MCP scope " + dim("(~/.copilot/mcp-config.json — user only)") + ":")
		fmt.Printf("      %s user %s\n", cyan("[1]"), green("(recommended)"))
		fmt.Printf("      %s skip\n", cyan("[2]"))
		switch promptChoice("    Choice [1]: ", 1, 2) {
		case 2:
			scope = "skip"
		default:
			scope = "user"
		}
	}

	mcpBin, err := requireMCPBinary()
	if err != nil {
		return err
	}

	switch scope {
	case "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path := filepath.Join(home, ".copilot", "mcp-config.json")
		return mergeCopilotMCP(path, mcpBin)
	case "skip":
		fmt.Printf("    %s MCP %s %s\n", okTag(), dim("→"), dim("skipped"))
		return nil
	default:
		return fmt.Errorf("unknown -copilot-mcp-scope %q (use user|skip)", scope)
	}
}

// mergeCopilotMCP merges a clawdchan server entry into Copilot's
// mcp-config.json, preserving every sibling field. The "tools": ["*"]
// entry is required — it's both Copilot's per-server allowlist and the
// "trust everything on this server" mechanism.
func mergeCopilotMCP(path, mcpBin string) error {
	return mergeJSONMCPServer(path, map[string]any{
		"type":    "local",
		"command": mcpBin,
		"tools":   []any{"*"},
	}, "(tools=[\"*\"])")
}

func doctorCopilot() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".copilot", "mcp-config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if strings.Contains(string(data), `"clawdchan"`) {
		return []string{"GitHub Copilot CLI: MCP registered (user scope, " + path + ")"}
	}
	return nil
}

func uninstallHintsCopilot() []string {
	return []string{
		"If you wired GitHub Copilot CLI, drop the clawdchan entry from:",
		"  ~/.copilot/mcp-config.json",
		"Remove the \"clawdchan\" key from the \"mcpServers\" object.",
	}
}
