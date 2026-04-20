package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/pairing"
)

const webUI = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>ClawdChan Secure Pairing</title>
<style>
  :root {
    --bg-color: #0f172a;
    --card-bg: rgba(30, 41, 59, 0.7);
    --text-main: #f8fafc;
    --text-muted: #94a3b8;
    --accent: #3b82f6;
    --accent-hover: #2563eb;
    --success: #10b981;
    --danger: #ef4444;
    --danger-hover: #dc2626;
  }
  body {
    background-color: var(--bg-color);
    color: var(--text-main);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    margin: 0;
  }
  .card {
    background: var(--card-bg);
    backdrop-filter: blur(12px);
    -webkit-backdrop-filter: blur(12px);
    border: 1px solid rgba(255, 255, 255, 0.1);
    border-radius: 16px;
    padding: 40px;
    width: 380px;
    text-align: center;
    box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.5);
    transition: all 0.4s ease;
  }
  h2 { margin-top: 0; font-weight: 600; }
  p { color: var(--text-muted); line-height: 1.5; margin-bottom: 30px; }
  .btn-container { display: flex; gap: 12px; justify-content: center; }
  button {
    border: none;
    border-radius: 8px;
    padding: 12px 24px;
    font-size: 16px;
    font-weight: 600;
    cursor: pointer;
    transition: background 0.2s;
    color: #fff;
    flex: 1;
  }
  .btn-approve { background: var(--accent); }
  .btn-approve:hover { background: var(--accent-hover); }
  .btn-decline { background: var(--danger); }
  .btn-decline:hover { background: var(--danger-hover); }
  .spinner {
    border: 3px solid rgba(255,255,255,0.1);
    border-top: 3px solid var(--accent);
    border-radius: 50%;
    width: 40px;
    height: 40px;
    animation: spin 1s linear infinite;
    margin: 0 auto 20px;
  }
  @keyframes spin { 0% { transform: rotate(0deg); } 100% { transform: rotate(360deg); } }
  .sas-box {
    background: rgba(0,0,0,0.2);
    padding: 15px;
    border-radius: 8px;
    font-family: monospace;
    font-size: 18px;
    letter-spacing: 1px;
    color: var(--success);
    margin-top: 20px;
  }
  .hidden { display: none !important; }
</style>
</head>
<body>
<div class="card" id="main-card">
  <div id="state-connecting">
    <div class="spinner"></div>
    <h2>Authenticating...</h2>
    <p>Establishing secure rendezvous with peer.</p>
  </div>
  
  <div id="state-approval" class="hidden">
    <h2>Pairing Request</h2>
    <p><strong id="peer-alias" style="color:#fff;font-size:1.1em;">Unknown</strong> wants to pair with you.</p>
    <div class="btn-container">
      <button class="btn-decline" onclick="sendApproval(false)">Decline</button>
      <button class="btn-approve" onclick="sendApproval(true)">Approve</button>
    </div>
  </div>

  <div id="state-success" class="hidden">
    <svg width="64" height="64" viewBox="0 0 24 24" fill="none" stroke="var(--success)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="margin-bottom:20px;">
      <path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"></path>
      <polyline points="22 4 12 14.01 9 11.01"></polyline>
    </svg>
    <h2>Successfully Paired!</h2>
    <p>Your secure confirmation code (SAS) is:</p>
    <div class="sas-box" id="sas-display"></div>
    <p style="margin-top:20px; margin-bottom:0; font-size:14px;">You can safely close this window.</p>
  </div>

  <div id="state-error" class="hidden">
    <svg width="64" height="64" viewBox="0 0 24 24" fill="none" stroke="var(--danger)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="margin-bottom:20px;">
      <circle cx="12" cy="12" r="10"></circle>
      <line x1="15" y1="9" x2="9" y2="15"></line>
      <line x1="9" y1="9" x2="15" y2="15"></line>
    </svg>
    <h2>Pairing Failed</h2>
    <p>The secure handshake could not be completed.</p>
  </div>
</div>

<script>
  let pollInterval;
  
  function showState(id) {
    document.getElementById('state-connecting').classList.add('hidden');
    document.getElementById('state-approval').classList.add('hidden');
    document.getElementById('state-success').classList.add('hidden');
    document.getElementById('state-error').classList.add('hidden');
    document.getElementById(id).classList.remove('hidden');
  }

  async function pollState() {
    try {
      const res = await fetch('/api/state');
      const data = await res.json();
      
      if (data.status === 'needs_approval') {
        document.getElementById('peer-alias').textContent = data.peer_alias;
        showState('state-approval');
      } else if (data.status === 'paired') {
        document.getElementById('sas-display').textContent = data.sas;
        showState('state-success');
        clearInterval(pollInterval);
      } else if (data.status === 'error') {
        showState('state-error');
        clearInterval(pollInterval);
      }
    } catch (e) {
      console.error(e);
    }
  }

  async function sendApproval(approved) {
    showState('state-connecting'); // show loading while submitting
    try {
      await fetch('/api/approve', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ approved })
      });
    } catch (e) {
      showState('state-error');
    }
  }

  pollInterval = setInterval(pollState, 1000);
  pollState(); // Initial poll
</script>
</body>
</html>`

type webState struct {
	Status    string `json:"status"`
	PeerAlias string `json:"peer_alias"`
	SAS       string `json:"sas"`
}

type consumeResult struct {
	peer pairing.Peer
	err  error
}

func RunWebConfirmation(ctx context.Context, n *node.Node, mnemonic string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := webState{Status: "connecting"}
	var stateMu sync.RWMutex
	setState := func(status, peerAlias, sas string) {
		stateMu.Lock()
		state.Status = status
		if peerAlias != "" {
			state.PeerAlias = peerAlias
		}
		if sas != "" {
			state.SAS = sas
		}
		stateMu.Unlock()
	}
	readState := func() webState {
		stateMu.RLock()
		defer stateMu.RUnlock()
		return state
	}

	approvalCh := make(chan bool, 1)
	resultCh := make(chan consumeResult, 1)

	go func() {
		peer, err := n.ConsumeInteractive(ctx, mnemonic, func(confirmCtx context.Context, peerCard pairing.Card) (bool, error) {
			setState("needs_approval", peerCard.Alias, "")
			select {
			case approved := <-approvalCh:
				return approved, nil
			case <-confirmCtx.Done():
				return false, confirmCtx.Err()
			}
		})
		if err != nil {
			setState("error", "", "")
			resultCh <- consumeResult{err: err}
			return
		}
		setState("paired", peer.Alias, strings.Join(peer.SAS[:], "-"))
		resultCh <- consumeResult{peer: peer}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(webUI))
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(readState()); err != nil {
			http.Error(w, fmt.Sprintf("encode state: %v", err), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("POST /api/approve", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Approved *bool `json:"approved"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
			return
		}
		if req.Approved == nil {
			http.Error(w, "missing required field: approved", http.StatusBadRequest)
			return
		}
		if readState().Status != "needs_approval" {
			http.Error(w, "approval not requested", http.StatusConflict)
			return
		}
		select {
		case approvalCh <- *req.Approved:
		default:
			http.Error(w, "approval already submitted", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	server := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen web confirmation server: %w", err)
	}

	serveErrCh := make(chan error, 1)
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
		}
	}()

	baseURL := fmt.Sprintf("http://%s", ln.Addr().String())
	if err := openBrowser(baseURL); err != nil {
		_ = server.Close()
		return fmt.Errorf("open browser: %w", err)
	}

	var res consumeResult
	select {
	case res = <-resultCh:
	case err := <-serveErrCh:
		cancel()
		_ = server.Close()
		return fmt.Errorf("web confirmation server: %w", err)
	case <-ctx.Done():
		cancel()
		_ = server.Close()
		return ctx.Err()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)

	if res.err != nil {
		return res.err
	}
	return nil
}

func openBrowser(targetURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL)
	case "darwin":
		cmd = exec.Command("open", targetURL)
	default:
		cmd = exec.Command("xdg-open", targetURL)
	}
	return cmd.Start()
}
