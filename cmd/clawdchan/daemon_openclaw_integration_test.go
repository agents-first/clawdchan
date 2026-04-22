package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agents-first/clawdchan/internal/relayserver"
	"github.com/gorilla/websocket"
)

type testGatewayMessage struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Event   string          `json:"event,omitempty"`
	OK      bool            `json:"ok,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type fakeOpenClawGateway struct {
	token string
	wsURL string

	mu       sync.Mutex
	connects int
	seq      uint64
	server   *httptest.Server
}

func newFakeOpenClawGateway(t *testing.T, token string) *fakeOpenClawGateway {
	t.Helper()
	gw := &fakeOpenClawGateway{token: token}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gw.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+gw.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		gw.mu.Lock()
		gw.connects++
		gw.mu.Unlock()

		_ = conn.WriteJSON(testGatewayMessage{
			Type:  "event",
			Event: "connect.challenge",
			Payload: mustJSON(t, map[string]any{
				"nonce": "nonce-test",
				"ts":    0,
			}),
		})

		var connectReq testGatewayMessage
		if err := conn.ReadJSON(&connectReq); err != nil {
			return
		}
		if connectReq.Type != "req" || connectReq.Method != "connect" {
			return
		}
		_ = conn.WriteJSON(testGatewayMessage{
			Type:    "res",
			ID:      connectReq.ID,
			OK:      true,
			Payload: mustJSON(t, map[string]any{"type": "hello-ok"}),
		})

		for {
			var req testGatewayMessage
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			if req.Type != "req" {
				continue
			}
			switch req.Method {
			case "sessions.create":
				sid := fmt.Sprintf("sid-%d", atomic.AddUint64(&gw.seq, 1))
				_ = conn.WriteJSON(testGatewayMessage{
					Type: "res",
					ID:   req.ID,
					Payload: mustJSON(t, map[string]any{
						"session_id": sid,
					}),
				})
			case "sessions.send", "sessions.messages.subscribe":
				_ = conn.WriteJSON(testGatewayMessage{
					Type: "res",
					ID:   req.ID,
					Payload: mustJSON(t, map[string]any{
						"ok": true,
					}),
				})
			default:
				_ = conn.WriteJSON(testGatewayMessage{Type: "res", ID: req.ID})
			}
		}
	}))
	gw.wsURL = "ws" + strings.TrimPrefix(gw.server.URL, "http")
	t.Cleanup(func() { gw.server.Close() })
	return gw
}

func (g *fakeOpenClawGateway) connectCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.connects
}

func TestDaemonRunOpenClawGatewaySuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repoRoot, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	clawdchan := filepath.Join(binDir, binaryName("clawdchan"))
	if err := goBuild(repoRoot, clawdchan, "./cmd/clawdchan"); err != nil {
		t.Fatalf("build clawdchan: %v", err)
	}

	relay := httptest.NewServer(relayserver.New(relayserver.Config{PairRendezvousTTL: 5 * time.Second}).Handler())
	t.Cleanup(relay.Close)
	relayURL := "ws" + strings.TrimPrefix(relay.URL, "http")
	gateway := newFakeOpenClawGateway(t, "good-token")

	home := t.TempDir()
	if err := writeCLIConfig(home, relayURL); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, clawdchan, "daemon", "run",
		"-openclaw", gateway.wsURL,
		"-openclaw-token", "good-token",
		"-openclaw-device-id", "daemon-test")
	cmd.Env = append(os.Environ(), "CLAWDCHAN_HOME="+home)
	out := &bytes.Buffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	waitForOutputOrExit(t, done, out, "clawdchan daemon running", 8*time.Second)
	waitUntil(t, 5*time.Second, func() bool { return gateway.connectCount() > 0 }, "daemon never connected to openclaw gateway")

	_ = cmd.Process.Kill()
	<-done
}

func TestDaemonRunOpenClawBadTokenFailsClearly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repoRoot, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	clawdchan := filepath.Join(binDir, binaryName("clawdchan"))
	if err := goBuild(repoRoot, clawdchan, "./cmd/clawdchan"); err != nil {
		t.Fatalf("build clawdchan: %v", err)
	}

	relay := httptest.NewServer(relayserver.New(relayserver.Config{PairRendezvousTTL: 5 * time.Second}).Handler())
	t.Cleanup(relay.Close)
	relayURL := "ws" + strings.TrimPrefix(relay.URL, "http")
	gateway := newFakeOpenClawGateway(t, "good-token")

	home := t.TempDir()
	if err := writeCLIConfig(home, relayURL); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, clawdchan, "daemon", "run",
		"-openclaw", gateway.wsURL,
		"-openclaw-token", "bad-token",
		"-openclaw-device-id", "daemon-test")
	cmd.Env = append(os.Environ(), "CLAWDCHAN_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected daemon start to fail with bad token, output:\n%s", string(out))
	}
	if !strings.Contains(string(out), "openclaw connect") {
		t.Fatalf("expected clear openclaw connect failure, got:\n%s", string(out))
	}
	if !strings.Contains(strings.ToLower(string(out)), "401") && !strings.Contains(strings.ToLower(string(out)), "unauthorized") {
		t.Fatalf("expected auth-specific failure detail, got:\n%s", string(out))
	}
}

func waitForOutputOrExit(t *testing.T, done <-chan error, out *bytes.Buffer, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("process exited before startup banner: %v\n%s", err, out.String())
		default:
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q\n%s", want, out.String())
}

func writeCLIConfig(home, relayURL string) error {
	cfg := struct {
		DataDir  string `json:"data_dir"`
		RelayURL string `json:"relay_url"`
		Alias    string `json:"alias"`
	}{
		DataDir:  home,
		RelayURL: relayURL,
		Alias:    "daemon-test",
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "config.json"), raw, 0o600)
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, failMsg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(failMsg)
}
