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
// mcpServers (Gemini CLI, GitHub Copilot CLI). The logSuffix is the
// parenthetical shown at the end of the success line, e.g.
// "(trust=true)" for Gemini or `(tools=["*"])` for Copilot.
func mergeJSONMCPServer(path string, entry map[string]any, logSuffix string) error {
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
	servers["clawdchan"] = entry
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
	fmt.Printf("    %s MCP %s %s %s\n", okTag(), dim("→"), dim(path), dim(logSuffix))
	return nil
}
