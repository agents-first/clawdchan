package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// errMCPNotOnPath is the shared "clawdchan-mcp is missing" message the
// agent-setup entry points raise when resolveMCPBinary comes up empty.
// Centralized so all four agents produce the same hint.
const errMCPNotOnPath = "clawdchan-mcp not on PATH — run `make install` first, then re-run setup"

// requireMCPBinary wraps resolveMCPBinary with the friendly error above,
// so every agent setup reads as a clean "bin, err := …; if err != nil".
func requireMCPBinary() (string, error) {
	bin, err := resolveMCPBinary()
	if err != nil || bin == "" {
		return "", errors.New(errMCPNotOnPath)
	}
	return bin, nil
}

// mergeJSONMCPServer merges a named server entry named "clawdchan" into
// a JSON file with a top-level mcpServers object, preserving every
// sibling field. Creates the file (and its parent dir) if missing.
//
// Used by hosts whose config format is a plain JSON object keyed by
// mcpServers (Gemini CLI, GitHub Copilot CLI, Cursor, Antigravity).
// Returns whether the clawdchan entry was newly added (true) or
// updated in place (false). The logSuffix is the parenthetical shown
// at the end of the success line, e.g. "(trust=true)" for Gemini or
// `(tools=["*"])` for Copilot.
func mergeJSONMCPServer(path string, entry map[string]any, logSuffix string) (bool, error) {
	return mergeJSONMCPServerAt(path, "mcpServers", entry, logSuffix)
}

// mergeJSONMCPServerAt is the general form. VS Code is the one host
// that nests its servers under the top-level "servers" key (matching
// the MCP spec's native naming) instead of "mcpServers".
func mergeJSONMCPServerAt(path, topKey string, entry map[string]any, logSuffix string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	// Missing file and empty-stub file (some hosts — e.g. Antigravity —
	// create a zero-byte placeholder on first run) both decode as {}.
	if len(data) == 0 {
		data = []byte("{}")
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	servers, _ := obj[topKey].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	_, existed := servers["clawdchan"]
	servers["clawdchan"] = entry
	obj[topKey] = servers

	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return false, err
	}
	fmt.Printf("    %s MCP %s %s %s\n", okTag(), dim("→"), dim(path), dim(logSuffix))
	return !existed, nil
}
