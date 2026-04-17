// Command clawdchan-mcp runs the ClawdChan MCP server over stdio. It is
// designed to be launched by Claude Code as a plugin MCP server; the CC
// process communicates with it via JSON-RPC on stdin/stdout.
//
// On startup the server loads its config from $CLAWDCHAN_HOME/config.json
// (defaulting to ~/.clawdchan), opens the SQLite store, connects to the
// configured relay, and exposes the ClawdChan tool surface.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/mark3labs/mcp-go/server"

	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/hosts/claudecode"
)

type config struct {
	DataDir  string `json:"data_dir"`
	RelayURL string `json:"relay_url"`
	Alias    string `json:"alias"`
}

const version = "0.1.0"

func main() {
	log.SetOutput(os.Stderr) // stdout is the MCP channel

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("clawdchan-mcp: %v", err)
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
	if err := n.Start(ctx); err != nil {
		log.Fatalf("clawdchan-mcp: start node: %v", err)
	}
	defer n.Stop()

	// Handle SIGINT/SIGTERM gracefully even though ServeStdio also installs
	// its own handlers; ours ensures the node shuts down cleanly.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
	}()

	s := server.NewMCPServer("clawdchan", version)
	claudecode.RegisterTools(s, n)

	id := n.Identity()
	log.Printf("clawdchan-mcp ready (alias=%q node=%x relay=%s)", cfg.Alias, id[:8], cfg.RelayURL)

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("clawdchan-mcp: serve: %v", err)
	}
}

func loadConfig() (config, error) {
	home := os.Getenv("CLAWDCHAN_HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = filepath.Join(h, ".clawdchan")
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
