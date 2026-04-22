package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ccAgent is the Claude Code wiring. It is the v0 default host and the
// only one with a distinct permissions step (mcp__clawdchan allow-rule
// in a separate settings.json). MCP registration and permissions are
// prompted for independently so the user can pick different scopes for
// each.
func ccAgent() *agentWiring {
	return &agentWiring{
		key:            "cc",
		flagName:       "cc",
		displayName:    "Claude Code",
		defaultOn:      true,
		scopeFlags:     []string{"mcp", "perm"},
		setup:          setupClaudeCode,
		doctorReport:   doctorCC,
		uninstallHints: uninstallHintsCC,
	}
}

func setupClaudeCode(yes bool, scopes map[string]string) error {
	if err := setupClaudeCodeMCP(yes, scopes["mcp"]); err != nil {
		return fmt.Errorf("MCP wiring: %w", err)
	}
	if err := setupClaudeCodePermissions(yes, scopes["perm"]); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	return nil
}

// setupClaudeCodeMCP asks where to register the clawdchan MCP server
// for Claude Code, then performs that write. The three scopes are:
//
//   - user:    ~/.claude.json via `claude mcp add -s user` — one-time
//     registration visible in every CC session on this machine
//   - project: .mcp.json in the current directory — visible only in CC
//     sessions opened from here; checked in with the project
//   - skip:    do nothing; user can register manually later
//
// In -y / non-TTY mode, absent an explicit -cc-mcp-scope flag, we
// default to "user" — the same choice the interactive flow recommends
// and the only one that produces a working install without further
// action. Pass -cc-mcp-scope=skip to opt out.
func setupClaudeCodeMCP(yes bool, flagScope string) error {
	scope := strings.ToLower(strings.TrimSpace(flagScope))
	if scope == "" && (yes || !stdinIsTTY()) {
		scope = "user"
	}
	if scope == "" {
		fmt.Println("    MCP: [1] user (recommended)  [2] project  [3] skip")
		switch promptChoice("    Choice [1]: ", 1, 3) {
		case 2:
			scope = "project"
		case 3:
			scope = "skip"
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
		return installCCMCPUser(mcpBin)
	case "project":
		return installCCMCPProject(mcpBin)
	case "skip":
		fmt.Println("    [ok] MCP → skipped")
		return nil
	default:
		return fmt.Errorf("unknown -cc-mcp-scope %q (use user|project|skip)", scope)
	}
}

// installCCMCPUser registers clawdchan-mcp user-wide via the `claude`
// CLI. We prefer shelling out to the CLI over editing ~/.claude.json
// directly — that file holds arbitrary user state and a bad merge could
// corrupt settings unrelated to ClawdChan.
func installCCMCPUser(mcpBin string) error {
	claudeCLI, err := exec.LookPath("claude")
	if err != nil {
		fmt.Println("  User-wide MCP registration needs the `claude` CLI. Run this when you have it available:")
		fmt.Printf("      claude mcp add clawdchan %s -s user\n", mcpBin)
		return nil
	}
	// `claude mcp add` returns non-zero if an entry with that name already
	// exists. Try add first, then retry with remove+add to update the path.
	cmd := exec.Command(claudeCLI, "mcp", "add", "clawdchan", mcpBin, "-s", "user")
	out, err := cmd.CombinedOutput()
	if err == nil {
		fmt.Printf("    [ok] MCP → user (%s)\n", mcpBin)
		return nil
	}
	if strings.Contains(string(out), "already exists") {
		// Re-register to update the path in case the user reinstalled.
		_ = exec.Command(claudeCLI, "mcp", "remove", "clawdchan", "-s", "user").Run()
		if out2, err2 := exec.Command(claudeCLI, "mcp", "add", "clawdchan", mcpBin, "-s", "user").CombinedOutput(); err2 != nil {
			return fmt.Errorf("claude mcp add (retry): %w: %s", err2, string(out2))
		}
		fmt.Printf("    [ok] MCP → user (updated, %s)\n", mcpBin)
		return nil
	}
	return fmt.Errorf("claude mcp add: %w: %s", err, string(out))
}

// installCCMCPProject writes .mcp.json in the current directory. If a
// .mcp.json exists and doesn't mention clawdchan we leave it alone
// rather than stomping unrelated config.
func installCCMCPProject(mcpBin string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	dotMCP := filepath.Join(cwd, ".mcp.json")
	if data, err := os.ReadFile(dotMCP); err == nil && !strings.Contains(string(data), "clawdchan") {
		fmt.Printf("  note: %s exists but doesn't include clawdchan. Skipping to avoid overwriting your config.\n", dotMCP)
		return nil
	}
	path, err := writeProjectMCP(cwd, mcpBin)
	if err != nil {
		return err
	}
	fmt.Printf("    [ok] MCP → %s\n", path)
	return nil
}

// setupClaudeCodePermissions adds an allow-rule for the clawdchan MCP
// server (`mcp__clawdchan`, a server-level prefix that covers every
// tool) to a Claude Code settings file. Without this every clawdchan_*
// call triggers a per-call permission prompt; sub-agents driving live
// collab can't answer those prompts and silently fail.
//
// Scopes:
//
//   - user:          ~/.claude/settings.json — applies to every CC session
//   - project:       .claude/settings.json in cwd — checked in, team-wide
//   - project-local: .claude/settings.local.json — personal, gitignored
//   - skip:          leave per-call prompts in place
//
// In -y / non-TTY mode, absent an explicit -cc-perm-scope flag, we
// default to "user" so the clawdchan_* allow-rule is in place for
// every CC session. Without it, per-call prompts block live-collab
// sub-agents. Pass -cc-perm-scope=skip to opt out.
func setupClaudeCodePermissions(yes bool, flagScope string) error {
	scope := strings.ToLower(strings.TrimSpace(flagScope))
	if scope == "" && (yes || !stdinIsTTY()) {
		scope = "user"
	}
	if scope == "" {
		fmt.Println("    Permissions (without this, clawdchan_* prompts block sub-agents):")
		fmt.Println("      [1] user (recommended)  [2] project  [3] project-local  [4] skip")
		switch promptChoice("    Choice [1]: ", 1, 4) {
		case 2:
			scope = "project"
		case 3:
			scope = "project-local"
		case 4:
			scope = "skip"
		default:
			scope = "user"
		}
	}

	const rule = "mcp__clawdchan"
	var settingsPath string
	switch scope {
	case "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		settingsPath = filepath.Join(home, ".claude", "settings.json")
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		settingsPath = filepath.Join(cwd, ".claude", "settings.json")
	case "project-local":
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		settingsPath = filepath.Join(cwd, ".claude", "settings.local.json")
	case "skip":
		fmt.Println("    [ok] perm → skipped")
		return nil
	default:
		return fmt.Errorf("unknown -cc-perm-scope %q (use user|project|project-local|skip)", scope)
	}

	if err := mergeAllowRule(settingsPath, rule); err != nil {
		return err
	}
	if scope == "project-local" {
		if err := ensureGitignoreEntry(".claude/settings.local.json"); err != nil {
			fmt.Printf("    note: update .gitignore manually: %v\n", err)
		}
	}
	return nil
}

// mergeAllowRule adds rule to settings.permissions.allow if absent.
// Reads the existing JSON (if any), preserves every sibling field, and
// writes the result back. Missing parent dirs are created.
func mergeAllowRule(settingsPath, rule string) error {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		data = []byte("{}")
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("parse %s: %w", settingsPath, err)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	perms, _ := obj["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	for _, e := range allow {
		if s, _ := e.(string); s == rule {
			fmt.Printf("    [ok] perm → %s (already present)\n", settingsPath)
			return nil
		}
	}
	allow = append(allow, rule)
	perms["allow"] = allow
	obj["permissions"] = perms

	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(settingsPath, append(out, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("    [ok] perm → %s\n", settingsPath)
	return nil
}

func doctorCC() []string {
	var lines []string
	home, err := os.UserHomeDir()
	if err != nil {
		return lines
	}
	// User-scope MCP lives in ~/.claude.json (managed by `claude mcp add`).
	if data, err := os.ReadFile(filepath.Join(home, ".claude.json")); err == nil && strings.Contains(string(data), `"clawdchan"`) {
		lines = append(lines, "Claude Code: MCP registered (user scope)")
	}
	// User-scope permission allow-rule.
	if data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); err == nil && strings.Contains(string(data), `"mcp__clawdchan"`) {
		lines = append(lines, "Claude Code: mcp__clawdchan allow-rule present (user scope)")
	}
	return lines
}

func uninstallHintsCC() []string {
	return []string{
		"If you wired Claude Code, remove its references:",
		"  claude mcp remove clawdchan -s user",
		"  # then drop \"mcp__clawdchan\" from permissions.allow in:",
		"  #   ~/.claude/settings.json",
		"  #   .claude/settings.json (any project you ran setup in)",
		"  #   .claude/settings.local.json (any project you ran setup in)",
	}
}
