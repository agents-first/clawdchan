package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// vscodeAgent is the Visual Studio Code wiring. VS Code 1.102+ supports
// MCP servers natively. User-scope config lives at
// `<UserConfigDir>/Code/User/mcp.json` — the same User profile dir that
// holds settings.json/keybindings.json — and uses a top-level "servers"
// key rather than the "mcpServers" convention shared by the other hosts.
//
// We register at user scope only. Workspace `.vscode/mcp.json` is also
// supported by VS Code, but matching the Cursor/Codex/Copilot pattern
// keeps the setup flow consistent and means a single registration works
// across every project the user opens.
//
// Reference: https://code.visualstudio.com/docs/copilot/customization/mcp-servers
func vscodeAgent() *agentWiring {
	return &agentWiring{
		key:            "vscode",
		flagName:       "vscode",
		displayName:    "VS Code",
		defaultOn:      false,
		scopeFlags:     []string{"mcp"},
		setup:          setupVSCode,
		doctorReport:   doctorVSCode,
		uninstallHints: uninstallHintsVSCode,
	}
}

// vscodeUserMCPPath returns the user-scope mcp.json path for VS Code
// Stable. The User folder under the OS-specific config dir is where VS
// Code stores per-user settings.json and keybindings.json; mcp.json
// lives alongside them.
func vscodeUserMCPPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "Code", "User", "mcp.json"), nil
}

func setupVSCode(yes bool, scopes map[string]string) error {
	scope := strings.ToLower(strings.TrimSpace(scopes["mcp"]))
	if scope == "" && (yes || !stdinIsTTY()) {
		scope = "user"
	}
	if scope == "" {
		fmt.Println("    MCP scope " + dim("(VS Code user profile — Code/User/mcp.json)") + ":")
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
		path, err := vscodeUserMCPPath()
		if err != nil {
			return err
		}
		if _, err := mergeJSONMCPServerAt(path, "servers", map[string]any{
			"command": mcpBin,
		}, "(user)"); err != nil {
			return err
		}
		fmt.Printf("    %s Reload VS Code %s to pick up the new server.\n",
			warnTag(), dim("(Command Palette → \"Developer: Reload Window\")"))
		return nil
	case "skip":
		fmt.Printf("    %s MCP %s %s\n", okTag(), dim("→"), dim("skipped"))
		return nil
	default:
		return fmt.Errorf("unknown -vscode-mcp-scope %q (use user|skip)", scope)
	}
}

func doctorVSCode() []string {
	path, err := vscodeUserMCPPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if strings.Contains(string(data), `"clawdchan"`) {
		return []string{"VS Code: MCP registered (user scope, " + path + ")"}
	}
	return nil
}

func uninstallHintsVSCode() []string {
	return []string{
		"If you wired VS Code, remove the clawdchan entry from the user mcp.json:",
		"  Command Palette → \"MCP: Open User Configuration\"",
		"  (or edit the file at <UserConfigDir>/Code/User/mcp.json)",
		"Delete the \"clawdchan\" key under the \"servers\" object.",
	}
}
