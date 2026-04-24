package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// geminiAgent wires the google-gemini/gemini-cli host.
//
// Config file (both scopes): settings.json with a top-level
// "mcpServers" object. Trust / auto-approve is baked into the server
// entry itself — `"trust": true` bypasses confirmations for every tool
// on that server, which is the closest analog to Claude Code's
// mcp__clawdchan allow-rule. There is no separate permissions file,
// so Gemini has one scope prompt (MCP), not two.
//
// Paths:
//
//   - user:    ~/.gemini/settings.json
//   - project: .gemini/settings.json in cwd
//
// Reference: https://github.com/google-gemini/gemini-cli/blob/main/docs/tools/mcp-server.md
func geminiAgent() *agentWiring {
	return &agentWiring{
		key:            "gemini",
		flagName:       "gemini",
		displayName:    "Gemini CLI",
		defaultOn:      false,
		scopeFlags:     []string{"mcp"},
		setup:          setupGemini,
		doctorReport:   doctorGemini,
		uninstallHints: uninstallHintsGemini,
	}
}

func setupGemini(yes bool, scopes map[string]string) error {
	scope := strings.ToLower(strings.TrimSpace(scopes["mcp"]))
	if scope == "" && (yes || !stdinIsTTY()) {
		scope = "user"
	}
	if scope == "" {
		fmt.Println("    MCP scope:")
		fmt.Printf("      %s user %s\n", cyan("[1]"), green("(recommended)"))
		fmt.Printf("      %s project\n", cyan("[2]"))
		fmt.Printf("      %s skip\n", cyan("[3]"))
		switch promptChoice("    Choice [1]: ", 1, 3) {
		case 2:
			scope = "project"
		case 3:
			scope = "skip"
		default:
			scope = "user"
		}
	}

	mcpBin, err := requireMCPBinary()
	if err != nil {
		return err
	}

	var path string
	switch scope {
	case "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, ".gemini", "settings.json")
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		path = filepath.Join(cwd, ".gemini", "settings.json")
	case "skip":
		fmt.Printf("    %s MCP %s %s\n", okTag(), dim("→"), dim("skipped"))
		return nil
	default:
		return fmt.Errorf("unknown -gemini-mcp-scope %q (use user|project|skip)", scope)
	}

	return mergeGeminiMCP(path, mcpBin)
}

// mergeGeminiMCP merges a clawdchan server entry into the Gemini
// settings file, preserving every sibling field. Sets trust:true so
// tool calls don't prompt — equivalent to the mcp__clawdchan CC
// allow-rule.
func mergeGeminiMCP(path, mcpBin string) error {
	_, err := mergeJSONMCPServer(path, map[string]any{
		"command": mcpBin,
		"trust":   true,
	}, "(trust=true)")
	return err
}

func doctorGemini() []string {
	var lines []string
	home, err := os.UserHomeDir()
	if err != nil {
		return lines
	}
	candidates := []string{filepath.Join(home, ".gemini", "settings.json")}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, ".gemini", "settings.json"))
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			continue
		}
		servers, _ := obj["mcpServers"].(map[string]any)
		if entry, ok := servers["clawdchan"].(map[string]any); ok {
			trust, _ := entry["trust"].(bool)
			lines = append(lines, fmt.Sprintf("Gemini CLI: MCP registered in %s (trust=%t)", p, trust))
		}
	}
	return lines
}

func uninstallHintsGemini() []string {
	return []string{
		"If you wired Gemini CLI, drop the clawdchan entry from:",
		"  ~/.gemini/settings.json",
		"  .gemini/settings.json (any project you ran setup in)",
		"Remove the \"clawdchan\" key from the \"mcpServers\" object.",
	}
}
