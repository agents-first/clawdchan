package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// antigravityAgent wires the Google Antigravity host. Antigravity is a
// VS Code-derived editor in Google's Gemini ecosystem; its MCP config
// lives under the parent ~/.gemini directory, not its own home, which
// is why the path is ~/.gemini/antigravity/mcp_config.json on every
// platform. The schema reuses the "mcpServers" convention shared with
// Cursor, Gemini CLI, and GitHub Copilot CLI.
//
// User-scope only — Antigravity's documented entry point ("Manage MCP
// Servers" → "View raw config") opens a single file, with no project
// scope mentioned in the upstream docs.
//
// Reference: https://antigravity.google (Editor → MCP Integration).
func antigravityAgent() *agentWiring {
	return &agentWiring{
		key:            "antigravity",
		flagName:       "antigravity",
		displayName:    "Antigravity",
		defaultOn:      false,
		scopeFlags:     []string{"mcp"},
		setup:          setupAntigravity,
		doctorReport:   doctorAntigravity,
		uninstallHints: uninstallHintsAntigravity,
	}
}

// antigravityUserMCPPath returns the user-scope mcp_config.json path
// for Antigravity. Layout matches the docs verbatim on every platform:
// ~/.gemini/antigravity/mcp_config.json.
func antigravityUserMCPPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "antigravity", "mcp_config.json"), nil
}

func setupAntigravity(yes bool, scopes map[string]string) error {
	scope := strings.ToLower(strings.TrimSpace(scopes["mcp"]))
	if scope == "" && (yes || !stdinIsTTY()) {
		scope = "user"
	}
	if scope == "" {
		fmt.Println("    MCP scope " + dim("(~/.gemini/antigravity/mcp_config.json — user only)") + ":")
		fmt.Printf("      %s user %s\n", cyan("[1]"), green("(recommended)"))
		fmt.Printf("      %s skip\n", cyan("[2]"))
		switch promptChoice("    Choice [1]: ", 1, 2) {
		case 2:
			scope = "skip"
		default:
			scope = "user"
		}
	}

	switch scope {
	case "user":
		mcpBin, err := requireMCPBinary()
		if err != nil {
			return err
		}
		path, err := antigravityUserMCPPath()
		if err != nil {
			return err
		}
		_, err = mergeJSONMCPServer(path, map[string]any{
			"command": mcpBin,
		}, "(user)")
		return err
	case "skip":
		fmt.Printf("    %s MCP %s %s\n", okTag(), dim("→"), dim("skipped"))
		return nil
	default:
		return fmt.Errorf("unknown -antigravity-mcp-scope %q (use user|skip)", scope)
	}
}

func doctorAntigravity() []string {
	path, err := antigravityUserMCPPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if strings.Contains(string(data), `"clawdchan"`) {
		return []string{"Antigravity: MCP registered (user scope, " + path + ")"}
	}
	return nil
}

func uninstallHintsAntigravity() []string {
	return []string{
		"If you wired Antigravity, remove the clawdchan entry from:",
		"  ~/.gemini/antigravity/mcp_config.json",
		"Delete the \"clawdchan\" key under the \"mcpServers\" object.",
	}
}
