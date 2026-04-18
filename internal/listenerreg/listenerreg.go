// Package listenerreg is a small pidfile-based registry of "listener"
// processes for a ClawdChan data dir. A listener is any process that holds a
// live link to the relay on behalf of the local node: the long-running
// `clawdchan listen` CLI, or the `clawdchan-mcp` server spawned by Claude
// Code.
//
// The registry exists so Claude (via the MCP server) can tell the user
// whether they currently have persistent inbound-message capacity, and
// suggest starting a `clawdchan listen` if not. It is intentionally not a
// lock — multiple listeners are fine, and an MCP listener dying when the CC
// session closes is expected.
package listenerreg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Kind describes how the listener is attached.
type Kind string

const (
	KindCLI Kind = "cli" // `clawdchan listen` in a terminal
	KindMCP Kind = "mcp" // `clawdchan-mcp` spawned by Claude Code
)

// Entry is one registered listener.
type Entry struct {
	PID       int    `json:"pid"`
	Kind      Kind   `json:"kind"`
	StartedMs int64  `json:"started_ms"`
	NodeID    string `json:"node_id"`
	RelayURL  string `json:"relay_url"`
	Alias     string `json:"alias"`
}

func dirFor(dataDir string) string { return filepath.Join(dataDir, "listeners") }

// Register writes a pidfile for the current process under dataDir/listeners/
// and returns an unregister function. Callers must defer the returned
// function; the pidfile is automatically removed when it runs.
func Register(dataDir string, kind Kind, nodeID, relayURL, alias string) (func(), error) {
	if dataDir == "" {
		return func() {}, errors.New("listenerreg: empty data dir")
	}
	d := dirFor(dataDir)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return func() {}, fmt.Errorf("listenerreg: mkdir: %w", err)
	}
	pid := os.Getpid()
	entry := Entry{
		PID:       pid,
		Kind:      kind,
		StartedMs: time.Now().UnixMilli(),
		NodeID:    nodeID,
		RelayURL:  relayURL,
		Alias:     alias,
	}
	path := filepath.Join(d, fmt.Sprintf("%d.json", pid))
	blob, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return func() {}, err
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return func() {}, fmt.Errorf("listenerreg: write: %w", err)
	}
	return func() { _ = os.Remove(path) }, nil
}

// List returns every live listener registered under dataDir. Stale pidfiles
// (pointing to dead PIDs) are pruned as a side effect.
func List(dataDir string) ([]Entry, error) {
	d := dirFor(dataDir)
	ents, err := os.ReadDir(d)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Entry, 0, len(ents))
	for _, de := range ents {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		base := strings.TrimSuffix(de.Name(), ".json")
		if _, err := strconv.Atoi(base); err != nil {
			continue
		}
		full := filepath.Join(d, de.Name())
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			_ = os.Remove(full)
			continue
		}
		if !pidAlive(e.PID) {
			_ = os.Remove(full)
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// pidAlive returns true if pid names a running process on this host. On Unix
// we send signal 0, which performs permission checks but no delivery.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		// On Windows, FindProcess actually checks for existence and returns an
		// error if the PID is not found. Signal(0) is not supported.
		return true
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but we don't own it — still alive.
	return errors.Is(err, syscall.EPERM)
}
