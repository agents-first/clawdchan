package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cursorAgent is the Cursor IDE wiring. Cursor reads MCP servers only from
// the global ~/.cursor/mcp.json — project-level .cursor/mcp.json files are
// silently ignored. A full application restart (Cmd+Q / Alt+F4, then
// reopen) is required; a window reload is not sufficient because Cursor
// registers MCP servers at startup, not on reload.
func cursorAgent() *agentWiring {
	return &agentWiring{
		key:            "cursor",
		flagName:       "cursor",
		displayName:    "Cursor",
		defaultOn:      false,
		scopeFlags:     []string{"mcp"},
		setup:          setupCursor,
		doctorReport:   doctorCursor,
		uninstallHints: uninstallHintsCursor,
	}
}

func setupCursor(yes bool, scopes map[string]string) error {
	return setupCursorMCP(yes, scopes["mcp"])
}

// setupCursorMCP writes the clawdchan MCP entry into ~/.cursor/mcp.json.
//
// Cursor only supports a single scope for MCP — the global
// ~/.cursor/mcp.json. Project-level .cursor/mcp.json is not loaded.
// There is no separate permissions / trust step for Cursor; the MCP server
// entry is unconditionally trusted once present.
func setupCursorMCP(yes bool, flagScope string) error {
	scope := strings.ToLower(strings.TrimSpace(flagScope))
	if scope == "" && (yes || !stdinIsTTY()) {
		scope = "user"
	}
	if scope == "" {
		fmt.Println("    MCP scope:")
		fmt.Printf("      %s user — %s\n", cyan("[1]"), green("~/.cursor/mcp.json (global, only supported scope)"))
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
		return installCursorMCPGlobal(mcpBin)
	case "skip":
		fmt.Printf("    %s MCP %s %s\n", okTag(), dim("→"), dim("skipped"))
		return nil
	default:
		return fmt.Errorf("unknown -cursor-mcp-scope %q (use user|skip — project scope is not supported by Cursor)", scope)
	}
}

// installCursorMCPGlobal writes or merges the clawdchan entry into
// ~/.cursor/mcp.json. If the file already contains a "clawdchan" server
// entry we update its command path in-place; otherwise we add it. Sibling
// entries are preserved.
func installCursorMCPGlobal(mcpBin string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cursorDir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		return err
	}
	configPath := filepath.Join(cursorDir, "mcp.json")
	added, err := mergeJSONMCPServer(configPath, map[string]any{
		"command": mcpBin,
	}, "(global)")
	if err != nil {
		return err
	}
	action := "updated"
	if added {
		action = "added"
	}
	fmt.Printf("      %s %s\n", dim("status:"), dim(action))
	fmt.Printf("    %s Fully quit Cursor %s and reopen — window reload is not enough.\n",
		warnTag(), dim("(Cmd+Q on macOS / Alt+F4 on Windows/Linux)"))
	fmt.Printf("      Verify: Cursor Settings → MCP — clawdchan should show a green connected status.\n")
	return nil
}

func doctorCursor() []string {
	var lines []string
	home, err := os.UserHomeDir()
	if err != nil {
		return lines
	}
	configPath := filepath.Join(home, ".cursor", "mcp.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return lines
	}
	if strings.Contains(string(data), `"clawdchan"`) {
		lines = append(lines, "Cursor: MCP registered (~/.cursor/mcp.json)")
	}
	return lines
}

func uninstallHintsCursor() []string {
	return []string{
		"If you wired Cursor, remove the clawdchan entry from ~/.cursor/mcp.json",
		"  (edit the file and delete the \"clawdchan\" key under \"mcpServers\")",
		"  then fully quit and reopen Cursor.",
	}
}
