package main

import (
	"encoding/json"
	"errors"
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
	if scope == "" {
		if yes || !stdinIsTTY() {
			fmt.Println("  MCP server registration: defaulting to user scope (pass -gemini-mcp-scope=skip to opt out)")
			scope = "user"
		}
	}
	if scope == "" {
		fmt.Println()
		fmt.Println("  Where should Gemini CLI find the clawdchan-mcp server?")
		fmt.Println("    [1] User-wide (recommended) — ~/.gemini/settings.json; available in every session")
		fmt.Println("    [2] This project only — .gemini/settings.json in the current directory")
		fmt.Println("    [3] Skip")
		switch promptChoice("  Choice [1]: ", 1, 3) {
		case 2:
			scope = "project"
		case 3:
			return nil
		default:
			scope = "user"
		}
	}

	mcpBin, _ := resolveMCPBinary()
	if mcpBin == "" {
		return errors.New("clawdchan-mcp not on PATH — run `make install` first, then re-run setup")
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
		fmt.Println("  Gemini MCP registration: skipped")
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
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		data = []byte("{}")
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	servers, _ := obj["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["clawdchan"] = map[string]any{
		"command": mcpBin,
		"trust":   true,
	}
	obj["mcpServers"] = servers

	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("  [ok] wrote clawdchan MCP entry (trust=true) to %s\n", path)
	return nil
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
