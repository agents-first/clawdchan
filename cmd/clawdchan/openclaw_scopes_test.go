package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// Extra scopes (operator.pairing, operator.approvals) come from other
// OpenClaw flows — the merge must not drop them.
func TestMergeScopeList_PreservesExtraScopes(t *testing.T) {
	in := []any{"operator.read", "operator.pairing", "operator.approvals"}
	out, _ := mergeScopeList(in)
	got := make([]string, 0, len(out))
	for _, v := range out {
		got = append(got, v.(string))
	}
	want := []string{
		"operator.admin", "operator.approvals", "operator.pairing",
		"operator.read", "operator.write",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge dropped or reordered scopes\n got: %v\nwant: %v", got, want)
	}
}

func TestEnsureOpenClawOperatorScopes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows

	deviceID := "dev-abc"
	authPath := filepath.Join(home, ".openclaw", "identity", "device-auth.json")
	pairedPath := filepath.Join(home, ".openclaw", "devices", "paired.json")

	mustWriteJSON(t, authPath, map[string]any{
		"version":  1,
		"deviceId": deviceID,
		"tokens": map[string]any{
			"operator": map[string]any{
				"token":  "t",
				"role":   "operator",
				"scopes": []any{"operator.read"},
			},
		},
	})
	mustWriteJSON(t, pairedPath, map[string]any{
		deviceID: map[string]any{
			"deviceId":       deviceID,
			"scopes":         []any{"operator.read"},
			"approvedScopes": []any{"operator.read"},
			"tokens": map[string]any{
				"operator": map[string]any{"scopes": []any{"operator.read"}},
			},
		},
	})

	changed, err := ensureOpenClawOperatorScopes()
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !changed {
		t.Fatalf("first run should report changed=true")
	}

	// All three scope fields on the paired entry must carry write+admin.
	paired := readJSON(t, pairedPath)
	entry := paired[deviceID].(map[string]any)
	for _, path := range [][]string{
		{"scopes"},
		{"approvedScopes"},
		{"tokens", "operator", "scopes"},
	} {
		got := stringsAt(entry, path)
		if !slices.Contains(got, "operator.write") || !slices.Contains(got, "operator.admin") {
			t.Fatalf("%v: missing write/admin, got %v", path, got)
		}
	}

	// Idempotent: second run is a no-op (drives the restart-gate behaviour).
	changed2, err := ensureOpenClawOperatorScopes()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if changed2 {
		t.Fatalf("second run should be a no-op; got changed=true")
	}
}

func mustWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func stringsAt(m map[string]any, path []string) []string {
	var cur any = m
	for _, k := range path {
		cur = cur.(map[string]any)[k]
	}
	list := cur.([]any)
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = v.(string)
	}
	return out
}
