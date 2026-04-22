package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// codexAgent wires the openai/codex (Rust) host.
//
// Config file: ~/.codex/config.toml. Codex is user-scope only — the
// upstream docs describe no project-level config file for MCP servers,
// so we don't invent one. MCP entries live under `[mcp_servers.<name>]`
// tables. Approval mode is expressed as
// `default_tool_approval_mode = "approve"` on the same table, which is
// Codex's closest analog to a "trusted server" flag.
//
// Reference: https://github.com/openai/codex/blob/main/docs/config.md
// Schema: https://raw.githubusercontent.com/openai/codex/main/codex-rs/core/config.schema.json
//
// We edit the TOML file as text rather than pulling in a parser
// dependency — the contract is small: find a `[mcp_servers.clawdchan]`
// section, delete it if present, append a fresh block. Every other
// section in the file is left byte-identical.
func codexAgent() *agentWiring {
	return &agentWiring{
		key:            "codex",
		flagName:       "codex",
		displayName:    "Codex CLI",
		defaultOn:      false,
		scopeFlags:     []string{"mcp"},
		setup:          setupCodex,
		doctorReport:   doctorCodex,
		uninstallHints: uninstallHintsCodex,
	}
}

func setupCodex(yes bool, scopes map[string]string) error {
	scope := strings.ToLower(strings.TrimSpace(scopes["mcp"]))
	if scope == "" {
		if yes || !stdinIsTTY() {
			fmt.Println("  MCP server registration: defaulting to user scope (pass -codex-mcp-scope=skip to opt out)")
			scope = "user"
		}
	}
	if scope == "" {
		fmt.Println()
		fmt.Println("  Register clawdchan-mcp for Codex CLI?")
		fmt.Println("    Codex's documented MCP config is user-scope only: ~/.codex/config.toml")
		fmt.Println("    [1] User-wide (recommended)")
		fmt.Println("    [2] Skip")
		switch promptChoice("  Choice [1]: ", 1, 2) {
		case 2:
			return nil
		default:
			scope = "user"
		}
	}

	mcpBin, _ := resolveMCPBinary()
	if mcpBin == "" {
		return errors.New("clawdchan-mcp not on PATH — run `make install` first, then re-run setup")
	}

	switch scope {
	case "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path := filepath.Join(home, ".codex", "config.toml")
		return mergeCodexMCP(path, mcpBin)
	case "skip":
		fmt.Println("  Codex MCP registration: skipped")
		return nil
	default:
		return fmt.Errorf("unknown -codex-mcp-scope %q (use user|skip)", scope)
	}
}

// mergeCodexMCP replaces the [mcp_servers.clawdchan] section in the
// Codex config.toml with a fresh block, or appends one if absent. The
// rest of the file is preserved byte-for-byte. This deliberately does
// NOT use a TOML parser — round-tripping arbitrary user TOML through a
// parser and re-serializing would reorder keys and strip comments.
func mergeCodexMCP(path, mcpBin string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	stripped, removed := removeTOMLSection(string(existing), "mcp_servers.clawdchan")

	var buf strings.Builder
	buf.WriteString(stripped)
	if buf.Len() > 0 && !strings.HasSuffix(buf.String(), "\n") {
		buf.WriteString("\n")
	}
	if buf.Len() > 0 && !strings.HasSuffix(buf.String(), "\n\n") {
		buf.WriteString("\n")
	}
	buf.WriteString("[mcp_servers.clawdchan]\n")
	fmt.Fprintf(&buf, "command = %q\n", mcpBin)
	buf.WriteString("default_tool_approval_mode = \"approve\"\n")

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		return err
	}
	verb := "added"
	if removed {
		verb = "updated"
	}
	fmt.Printf("  [ok] %s [mcp_servers.clawdchan] in %s (default_tool_approval_mode=approve)\n", verb, path)
	return nil
}

// removeTOMLSection strips a named `[section]` block (up to the next
// top-level `[...]` header or EOF) from a TOML document. Returns the
// stripped text and a bool indicating whether the section existed.
// Comparison ignores surrounding whitespace in the header line.
func removeTOMLSection(src, name string) (string, bool) {
	scanner := bufio.NewScanner(strings.NewReader(src))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out strings.Builder
	inTarget := false
	removed := false
	target := "[" + name + "]"
	for scanner.Scan() {
		line := scanner.Text()
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			if trim == target {
				inTarget = true
				removed = true
				continue
			}
			inTarget = false
		}
		if inTarget {
			continue
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String(), removed
}

func doctorCodex() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".codex", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if strings.Contains(string(data), "[mcp_servers.clawdchan]") {
		return []string{"Codex CLI: MCP registered (user scope, " + path + ")"}
	}
	return nil
}

func uninstallHintsCodex() []string {
	return []string{
		"If you wired Codex CLI, remove the clawdchan section from:",
		"  ~/.codex/config.toml",
		"Delete the `[mcp_servers.clawdchan]` block.",
	}
}
