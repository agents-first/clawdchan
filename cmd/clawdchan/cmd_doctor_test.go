package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agents-first/clawdchan/internal/listenerreg"
)

func TestCheckRelayURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "ws", raw: "ws://relay.example"},
		{name: "wss", raw: "wss://relay.example"},
		{name: "http", raw: "http://relay.example"},
		{name: "https", raw: "https://relay.example"},
		{name: "empty", raw: "", wantErr: "relay url is empty"},
		{name: "bad parse", raw: "://bad", wantErr: "parse:"},
		{name: "unsupported scheme", raw: "ftp://relay.example", wantErr: "unexpected scheme"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRelayURL(tc.raw)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkRelayURL(%q): %v", tc.raw, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkRelayURL(%q): expected error containing %q", tc.raw, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("checkRelayURL(%q): got %q, want substring %q", tc.raw, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestActiveOpenClawConfig(t *testing.T) {
	dataDir := t.TempDir()
	writeListenerEntry(t, dataDir, "101.json", listenerreg.Entry{
		PID:                os.Getpid(),
		Kind:               listenerreg.KindCLI,
		StartedMs:          100,
		NodeID:             "abc123",
		OpenClawHostActive: true,
		OpenClawURL:        "ws://openclaw-old",
		OpenClawDeviceID:   "old-device",
	})
	writeListenerEntry(t, dataDir, "102.json", listenerreg.Entry{
		PID:                os.Getpid(),
		Kind:               listenerreg.KindMCP,
		StartedMs:          500,
		NodeID:             "abc123",
		OpenClawHostActive: true,
		OpenClawURL:        "ws://ignore-kind",
	})
	writeListenerEntry(t, dataDir, "103.json", listenerreg.Entry{
		PID:                os.Getpid(),
		Kind:               listenerreg.KindCLI,
		StartedMs:          600,
		NodeID:             "someone-else",
		OpenClawHostActive: true,
		OpenClawURL:        "ws://ignore-node",
	})
	writeListenerEntry(t, dataDir, "104.json", listenerreg.Entry{
		PID:                os.Getpid(),
		Kind:               listenerreg.KindCLI,
		StartedMs:          700,
		NodeID:             "abc123",
		OpenClawHostActive: false,
		OpenClawURL:        "ws://ignore-inactive",
	})
	writeListenerEntry(t, dataDir, "105.json", listenerreg.Entry{
		PID:                os.Getpid(),
		Kind:               listenerreg.KindCLI,
		StartedMs:          800,
		NodeID:             "ABC123",
		OpenClawHostActive: true,
		OpenClawURL:        "ws://openclaw-new",
		OpenClawDeviceID:   "new-device",
	})

	got, ok := activeOpenClawConfig(dataDir, "abc123")
	if !ok {
		t.Fatal("activeOpenClawConfig: expected match")
	}
	if got.OpenClawURL != "ws://openclaw-new" {
		t.Fatalf("OpenClawURL = %q, want latest matching entry", got.OpenClawURL)
	}
	if got.OpenClawDeviceID != "new-device" {
		t.Fatalf("OpenClawDeviceID = %q, want %q", got.OpenClawDeviceID, "new-device")
	}

	if _, ok := activeOpenClawConfig(dataDir, "missing"); ok {
		t.Fatal("activeOpenClawConfig: unexpected match for missing node")
	}
}

func TestOpenClawRemediation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "auth guidance",
			err:  errors.New("401 unauthorized"),
			want: "gateway rejected the token; update daemon -openclaw-token and restart the daemon service.",
		},
		{
			name: "reachability guidance",
			err:  errors.New("dial tcp 127.0.0.1:1234: connection refused"),
			want: "gateway unreachable; ensure OpenClaw is listening at ws://openclaw.test and daemon -openclaw matches that URL.",
		},
		{
			name: "generic guidance",
			err:  errors.New("unexpected protocol mismatch"),
			want: "check OpenClaw gateway logs and daemon -openclaw/-openclaw-token flags for mismatches.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := openClawRemediation(tc.err, "ws://openclaw.test")
			if got != tc.want {
				t.Fatalf("openClawRemediation() = %q, want %q", got, tc.want)
			}
		})
	}
}

func writeListenerEntry(t *testing.T, dataDir, name string, entry listenerreg.Entry) {
	t.Helper()
	path := filepath.Join(dataDir, "listeners", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir listeners: %v", err)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal listener entry: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write listener entry: %v", err)
	}
}
