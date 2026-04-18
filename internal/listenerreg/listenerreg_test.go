package listenerreg

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func TestRegisterAndList(t *testing.T) {
	dir := t.TempDir()

	ents, err := List(dir)
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(ents) != 0 {
		t.Fatalf("expected no entries, got %d", len(ents))
	}

	unregister, err := Register(dir, KindCLI, "nid", "ws://relay", "alice")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	ents, err = List(dir)
	if err != nil {
		t.Fatalf("List after register: %v", err)
	}
	if len(ents) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(ents))
	}
	if ents[0].Kind != KindCLI || ents[0].NodeID != "nid" || ents[0].Alias != "alice" {
		t.Fatalf("unexpected entry: %+v", ents[0])
	}

	unregister()
	ents, err = List(dir)
	if err != nil {
		t.Fatalf("List after unregister: %v", err)
	}
	if len(ents) != 0 {
		t.Fatalf("expected 0 entries after unregister, got %d", len(ents))
	}
}

func TestStaleEntriesArePruned(t *testing.T) {
	dir := t.TempDir()

	// Register then intentionally simulate a crash: keep the file but claim
	// a definitely-dead PID by rewriting. Easiest: create a fake entry with
	// PID = max int32 (very unlikely to exist) directly.
	_, err := Register(dir, KindMCP, "nid", "ws://relay", "a")
	if err != nil {
		t.Fatal(err)
	}

	// Drop in a second, stale pidfile.
	fakePath := dir + "/listeners/2147483646.json"
	if err := writeFile(fakePath, []byte(`{"pid":2147483646,"kind":"cli","node_id":"nid"}`)); err != nil {
		t.Fatal(err)
	}

	ents, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.PID == 2147483646 {
			t.Fatalf("stale entry not pruned: %+v", e)
		}
	}
}
