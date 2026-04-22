// Command clawdchan-mcp runs the ClawdChan MCP server over stdio. It is
// designed to be launched by Claude Code as a plugin MCP server; the CC
// process communicates with it via JSON-RPC on stdin/stdout.
//
// On startup the server loads its config from $CLAWDCHAN_HOME/config.json
// (defaulting to ~/.clawdchan), opens the SQLite store, and exposes the
// ClawdChan tool surface. It does NOT connect to the relay if a persistent
// daemon is already running for this node (detected via the listener
// registry): the daemon owns the relay link and the MCP server reads/writes
// the shared store. When no daemon is running, the MCP server falls back to
// owning the relay link for the lifetime of the Claude Code session.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/agents-first/clawdchan/core/node"
	"github.com/agents-first/clawdchan/core/surface"
	"github.com/agents-first/clawdchan/hosts/claudecode"
	"github.com/agents-first/clawdchan/internal/listenerreg"
)

type config struct {
	DataDir  string `json:"data_dir"`
	RelayURL string `json:"relay_url"`
	Alias    string `json:"alias"`
}

const version = "0.2.0"

func main() {
	log.SetOutput(os.Stderr) // stdout is the MCP channel

	cfg, err := loadConfig()
	if err != nil {
		// The MCP server is launched by Claude Code; its stderr is
		// typically swallowed. Dying here leaves the user with an opaque
		// "server failed to start" with no hint. Instead, serve a minimal
		// stub so the actionable message surfaces inside Claude Code.
		log.Printf("clawdchan-mcp: %v — serving setup-hint stub", err)
		serveUnconfigured(err)
		return
	}

	n, err := node.New(node.Config{
		DataDir:  cfg.DataDir,
		RelayURL: cfg.RelayURL,
		Alias:    cfg.Alias,
		Human:    claudecode.HumanSurface{},
		Agent:    claudecode.AgentSurface{},
	})
	if err != nil {
		log.Fatalf("clawdchan-mcp: new node: %v", err)
	}
	defer n.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id := n.Identity()
	daemon := daemonStateForNode(cfg.DataDir, hex.EncodeToString(id[:]))
	if daemon.openclawHost {
		n.SetHumanSurface(surface.NopHuman{})
		n.SetAgentSurface(surface.NopAgent{})
	}
	if daemon.running {
		if daemon.openclawHost {
			log.Printf("clawdchan-mcp: openclaw daemon mode detected, running outbox-writer-only (no Claude human surface)")
		} else {
			log.Printf("clawdchan-mcp: daemon detected, skipping relay connect (daemon owns inbound + outbox drain)")
		}
	} else {
		if err := n.Start(ctx); err != nil {
			log.Fatalf("clawdchan-mcp: start node: %v", err)
		}
		defer n.Stop()
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
	}()

	s := server.NewMCPServer("clawdchan", version)
	claudecode.RegisterTools(s, n)

	unregister, regErr := listenerreg.Register(
		cfg.DataDir, listenerreg.KindMCP,
		hex.EncodeToString(id[:]), cfg.RelayURL, cfg.Alias,
	)
	if regErr != nil {
		log.Printf("clawdchan-mcp: listener registry: %v", regErr)
	}
	defer unregister()

	log.Printf("clawdchan-mcp ready (alias=%q node=%x relay=%s daemon=%v openclaw_host=%v)", cfg.Alias, id[:8], cfg.RelayURL, daemon.running, daemon.openclawHost)

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("clawdchan-mcp: serve: %v", err)
	}
}

type daemonMode struct {
	running      bool
	openclawHost bool
}

// daemonStateForNode reports whether another process is already holding the
// relay link for this node — i.e. a running `clawdchan daemon` (or `clawdchan
// listen`). In OpenClaw host mode, MCP must remain outbox-writer-only and not
// register a Claude human surface.
func daemonStateForNode(dataDir, nodeID string) daemonMode {
	entries, err := listenerreg.List(dataDir)
	if err != nil {
		return daemonMode{}
	}
	for _, e := range entries {
		if !strings.EqualFold(e.NodeID, nodeID) {
			continue
		}
		if e.Kind == listenerreg.KindCLI {
			return daemonMode{running: true, openclawHost: e.OpenClawHostActive}
		}
	}
	return daemonMode{}
}

func loadConfig() (config, error) {
	home := os.Getenv("CLAWDCHAN_HOME")
	if home == "" {
		if runtime.GOOS == "windows" {
			if cfg, err := os.UserConfigDir(); err == nil && cfg != "" {
				home = filepath.Join(cfg, "clawdchan")
			}
		}
		if home == "" {
			h, _ := os.UserHomeDir()
			home = filepath.Join(h, ".clawdchan")
		}
	}
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		return config{}, fmt.Errorf("read config: %w (run `clawdchan init` first)", err)
	}
	var c config
	if err := json.Unmarshal(data, &c); err != nil {
		return config{}, fmt.Errorf("parse config: %w", err)
	}
	if c.DataDir == "" {
		c.DataDir = home
	}
	if c.RelayURL == "" {
		return config{}, fmt.Errorf("config: relay_url is empty")
	}
	return c, nil
}

// serveUnconfigured runs a stub MCP server when we can't load a real
// config. It registers a single clawdchan_toolkit tool whose response
// tells the agent exactly what to tell the user — open a terminal,
// run `clawdchan setup`, restart Claude Code. Any call to an unknown
// tool returns the same hint. This keeps the failure mode inside
// Claude Code's tool surface instead of a swallowed stderr line.
func serveUnconfigured(startupErr error) {
	msg := fmt.Sprintf(
		"ClawdChan is not configured on this machine.\n\n"+
			"Open a terminal and run:\n\n"+
			"    clawdchan setup\n\n"+
			"Then fully quit and reopen Claude Code so the MCP server "+
			"picks up the new config. Run `clawdchan doctor` to verify.\n\n"+
			"(Startup error: %v)", startupErr)

	hint := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		payload := map[string]any{
			"setup": map[string]any{
				"configured":                 false,
				"needs_persistent_listener":  true,
				"persistent_listener_active": false,
				"mcp_self_is_listener":       false,
				"user_message":               msg,
			},
			"self":  nil,
			"peers": []any{},
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	}

	s := server.NewMCPServer("clawdchan", version)
	s.AddTool(
		mcp.NewTool(
			"clawdchan_toolkit",
			mcp.WithDescription("ClawdChan is not configured; this tool returns setup instructions. Run `clawdchan setup` in a terminal, then restart Claude Code."),
		),
		hint,
	)
	if err := server.ServeStdio(s); err != nil {
		log.Printf("clawdchan-mcp: serve (unconfigured): %v", err)
	}
}
