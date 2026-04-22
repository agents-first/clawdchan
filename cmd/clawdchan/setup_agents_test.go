package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMergeGeminiMCP_NewFile verifies we create a well-formed
// settings.json when none exists, with the trust flag set.
func TestMergeGeminiMCP_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := mergeGeminiMCP(path, "/usr/local/bin/clawdchan-mcp"); err != nil {
		t.Fatalf("mergeGeminiMCP: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	servers, _ := obj["mcpServers"].(map[string]any)
	entry, ok := servers["clawdchan"].(map[string]any)
	if !ok {
		t.Fatalf("no clawdchan entry: %s", string(data))
	}
	if cmd, _ := entry["command"].(string); cmd != "/usr/local/bin/clawdchan-mcp" {
		t.Errorf("command = %q, want /usr/local/bin/clawdchan-mcp", cmd)
	}
	if trust, _ := entry["trust"].(bool); !trust {
		t.Errorf("trust = false, want true")
	}
}

// TestMergeGeminiMCP_PreservesSiblings ensures we don't stomp other
// keys the user has in their settings.json.
func TestMergeGeminiMCP_PreservesSiblings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	existing := `{
  "theme": "Atom One",
  "mcpServers": {
    "other": { "command": "other-mcp", "trust": false }
  },
  "selectedAuthType": "oauth-personal"
}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeGeminiMCP(path, "clawdchan-mcp"); err != nil {
		t.Fatalf("mergeGeminiMCP: %v", err)
	}
	data, _ := os.ReadFile(path)
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if obj["theme"] != "Atom One" {
		t.Errorf("theme dropped: %v", obj["theme"])
	}
	if obj["selectedAuthType"] != "oauth-personal" {
		t.Errorf("selectedAuthType dropped: %v", obj["selectedAuthType"])
	}
	servers, _ := obj["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Errorf("existing 'other' server dropped")
	}
	if _, ok := servers["clawdchan"]; !ok {
		t.Errorf("clawdchan not added")
	}
}

// TestMergeCopilotMCP_ShapeAndSiblings exercises both new-file and
// merge-with-siblings paths in one.
func TestMergeCopilotMCP_ShapeAndSiblings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-config.json")
	existing := `{"mcpServers":{"other":{"type":"local","command":"x","tools":["*"]}}}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeCopilotMCP(path, "/bin/clawdchan-mcp"); err != nil {
		t.Fatalf("mergeCopilotMCP: %v", err)
	}
	data, _ := os.ReadFile(path)
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	servers, _ := obj["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Errorf("existing server dropped")
	}
	entry, ok := servers["clawdchan"].(map[string]any)
	if !ok {
		t.Fatalf("no clawdchan entry: %s", string(data))
	}
	if entry["type"] != "local" {
		t.Errorf("type=%v, want local", entry["type"])
	}
	if entry["command"] != "/bin/clawdchan-mcp" {
		t.Errorf("command=%v", entry["command"])
	}
	tools, _ := entry["tools"].([]any)
	if len(tools) != 1 || tools[0] != "*" {
		t.Errorf("tools=%v, want [\"*\"]", tools)
	}
}

// TestMergeCodexMCP_NewFile verifies we create a minimal TOML block
// with approval mode set.
func TestMergeCodexMCP_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := mergeCodexMCP(path, "/bin/clawdchan-mcp"); err != nil {
		t.Fatalf("mergeCodexMCP: %v", err)
	}
	data, _ := os.ReadFile(path)
	got := string(data)
	if !strings.Contains(got, "[mcp_servers.clawdchan]") {
		t.Errorf("missing section header:\n%s", got)
	}
	if !strings.Contains(got, `command = "/bin/clawdchan-mcp"`) {
		t.Errorf("missing command:\n%s", got)
	}
	if !strings.Contains(got, `default_tool_approval_mode = "approve"`) {
		t.Errorf("missing approval mode:\n%s", got)
	}
}

// TestMergeCodexMCP_PreservesOtherSections ensures we don't touch
// unrelated TOML blocks on re-runs — regressions here would corrupt
// user config. Also covers the "update" path (existing clawdchan
// section is replaced, not duplicated).
func TestMergeCodexMCP_PreservesOtherSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	existing := `model = "gpt-5-codex"
approval_policy = "on-request"

[mcp_servers.other]
command = "other-mcp"

[mcp_servers.clawdchan]
command = "/old/path/clawdchan-mcp"
default_tool_approval_mode = "prompt"

[profile.work]
model = "o3"
`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeCodexMCP(path, "/new/path/clawdchan-mcp"); err != nil {
		t.Fatalf("mergeCodexMCP: %v", err)
	}
	data, _ := os.ReadFile(path)
	got := string(data)
	if !strings.Contains(got, `model = "gpt-5-codex"`) {
		t.Errorf("top-level key lost")
	}
	if !strings.Contains(got, `[mcp_servers.other]`) {
		t.Errorf("unrelated mcp section lost")
	}
	if !strings.Contains(got, `[profile.work]`) {
		t.Errorf("unrelated profile section lost")
	}
	if strings.Contains(got, `/old/path/clawdchan-mcp`) {
		t.Errorf("old path still present (should be replaced):\n%s", got)
	}
	if !strings.Contains(got, `/new/path/clawdchan-mcp`) {
		t.Errorf("new path not written:\n%s", got)
	}
	// Exactly one clawdchan section.
	if n := strings.Count(got, "[mcp_servers.clawdchan]"); n != 1 {
		t.Errorf("expected 1 [mcp_servers.clawdchan] block, got %d:\n%s", n, got)
	}
}

// TestRemoveTOMLSection_NoSubstringMatch guards against the obvious
// bug where the stripper eats a similarly-named section (e.g.
// "clawdchan_other").
func TestRemoveTOMLSection_NoSubstringMatch(t *testing.T) {
	src := `[mcp_servers.clawdchan_other]
command = "x"

[other]
key = "y"
`
	out, removed := removeTOMLSection(src, "mcp_servers.clawdchan")
	if removed {
		t.Errorf("should not have removed anything")
	}
	if !strings.Contains(out, "[mcp_servers.clawdchan_other]") {
		t.Errorf("near-miss section was dropped")
	}
}

// TestAgentRegistryUnique guards against copy-paste mistakes where two
// agents share a flag name — which would silently break CLI parsing.
func TestAgentRegistryUnique(t *testing.T) {
	seenKey := map[string]bool{}
	seenFlag := map[string]bool{}
	for _, a := range allAgents() {
		if seenKey[a.key] {
			t.Errorf("duplicate agent key %q", a.key)
		}
		if seenFlag[a.flagName] {
			t.Errorf("duplicate flag name %q", a.flagName)
		}
		seenKey[a.key] = true
		seenFlag[a.flagName] = true
		if a.setup == nil || a.doctorReport == nil || a.uninstallHints == nil {
			t.Errorf("agent %q missing required funcs", a.key)
		}
	}
}
