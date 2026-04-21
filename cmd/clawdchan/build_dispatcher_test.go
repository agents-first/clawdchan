package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildDispatcherNilWhenUnconfigured: the unconfigured case must not
// error — the daemon falls through to the toast-only path.
func TestBuildDispatcherNilWhenUnconfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  config
	}{
		{"nil block", config{}},
		{"disabled", config{Dispatch: &dispatchConfig{Enabled: false, Command: []string{"/bin/true"}}}},
		{"empty command", config{Dispatch: &dispatchConfig{Enabled: true}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _, err := buildDispatcher(c.cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d != nil {
				t.Fatalf("expected nil dispatcher")
			}
		})
	}
}

// TestBuildDispatcherResolvesBinary: a configured dispatcher should
// resolve Command[0] via exec.LookPath. Write a tiny executable to a
// tempdir so the test is hermetic and doesn't depend on what's
// installed at /bin.
func TestBuildDispatcherResolvesBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-dispatcher")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	cfg := config{Dispatch: &dispatchConfig{
		Enabled:         true,
		Command:         []string{bin, "--flag"},
		TimeoutSeconds:  10,
		MaxCollabRounds: 5,
	}}
	d, _, err := buildDispatcher(cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if d == nil {
		t.Fatal("expected dispatcher, got nil")
	}
	if !d.Enabled() {
		t.Fatal("expected Enabled() = true")
	}
}

// TestBuildDispatcherRejectsMissingBinary: the whole point of the
// LookPath check. A configured-but-broken dispatcher must surface an
// error at daemon startup rather than silently declining every incoming
// collab-sync at runtime.
func TestBuildDispatcherRejectsMissingBinary(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope-does-not-exist")
	cfg := config{Dispatch: &dispatchConfig{
		Enabled:        true,
		Command:        []string{missing},
		TimeoutSeconds: 10,
	}}
	_, _, err := buildDispatcher(cfg)
	if err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}
	if !strings.Contains(err.Error(), "nope-does-not-exist") {
		t.Fatalf("error should name the bad binary: %v", err)
	}
}
