// Command clawdchan is the reference CLI for a ClawdChan node.
//
// Subcommands:
//
//	clawdchan init      [-data DIR] [-relay URL] [-alias NAME] [-write-mcp DIR]
//	clawdchan whoami
//	clawdchan pair      [-alias NAME]                 print code; wait for peer
//	clawdchan consume   <mnemonic...>                  consume a code
//	clawdchan peers
//	clawdchan threads
//	clawdchan open      <peer-hex> [-topic T]          open a new thread
//	clawdchan send      <thread-hex-or-prefix> <text>
//	clawdchan listen    [-follow] [-tail N]            run node; print inbound
//	clawdchan daemon    run|install|uninstall|status   background listener with OS notifications
//	clawdchan inspect   <thread-hex-or-prefix>         print envelopes on thread
//	clawdchan doctor                                   diagnose install and link
//
// On first run, `clawdchan init` creates ~/.clawdchan/config.json and the
// SQLite store. Most subcommands start a node against the configured relay;
// `listen` stays attached to receive traffic, while one-shot commands exit as
// soon as their work is done (messages to offline peers are queued at the relay).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vMaroon/ClawdChan/core/node"
)

type config struct {
	DataDir  string `json:"data_dir"`
	RelayURL string `json:"relay_url"`
	Alias    string `json:"alias"`

	// OpenClaw fields are optional. When OpenClawURL is empty, the daemon
	// runs in its default CC-only mode. When set, the daemon connects to the
	// gateway and routes inbound human-surface traffic into OpenClaw sessions
	// in addition to firing OS notifications for Claude Code.
	OpenClawURL      string `json:"openclaw_url,omitempty"`
	OpenClawToken    string `json:"openclaw_token,omitempty"`
	OpenClawDeviceID string `json:"openclaw_device_id,omitempty"`
}

const configFileName = "config.json"

// defaultPublicRelay is the convenience relay vMaroon hosts on fly.io so
// users can get started without deploying their own. It's best-effort, has
// no SLA, and sees ciphertext only — but for first-time setup and casual
// use it saves the "run a server first" step. Deploy your own for stable
// or production use; see docs/deploy.md.
const defaultPublicRelay = "wss://clawdchan-test-relay.fly.dev"

func defaultDataDir() string {
	if v := os.Getenv("CLAWDCHAN_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawdchan")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "whoami":
		err = cmdWhoami(args)
	case "pair":
		err = cmdPair(args)
	case "consume":
		err = cmdConsume(args)
	case "peers":
		err = cmdPeers(args)
	case "peer":
		err = cmdPeer(args)
	case "threads":
		err = cmdThreads(args)
	case "open":
		err = cmdOpen(args)
	case "send":
		err = cmdSend(args)
	case "listen":
		err = cmdListen(args)
	case "daemon":
		err = cmdDaemon(args)
	case "path-setup":
		err = cmdPathSetup(args)
	case "setup":
		err = cmdSetup(args)
	case "inspect":
		err = cmdInspect(args)
	case "doctor":
		err = cmdDoctor(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "clawdchan %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `ClawdChan — let my Claude talk to yours

Usage:
  clawdchan <command> [args]

Commands:
  setup     One-command onboarding: init + PATH + daemon (interactive)
  init      Create config and identity (non-interactive)
  whoami    Print this node's id and alias
  pair      Generate a pairing code and wait for the peer
  consume   Enter a peer's pairing code
  peers     List paired peers
  peer      Manage one peer: show | rename | revoke | remove
  threads   List conversation threads
  open      Open a new thread with a peer
  send      Send a message on a thread
  listen      Stay connected and tail traffic to stdout (terminal UX)
  daemon      Background listener: fires OS notifications on inbound
              Subcommands: run | setup | install | uninstall | status
  path-setup  Put $GOPATH/bin on your shell PATH (zsh/bash/fish)
  inspect     Print envelopes on a thread
  doctor    Diagnose install, config, and relay connectivity

Config lives at $CLAWDCHAN_HOME or ~/.clawdchan.

Listen output legend:
  [time] -> me/role  thread=...  intent: text   (sent by this node)
  [time] <- peer/role thread=...  intent: text   (received from peer)`)
}

func loadConfig() (config, error) {
	dir := defaultDataDir()
	f := filepath.Join(dir, configFileName)
	data, err := os.ReadFile(f)
	if err != nil {
		return config{}, fmt.Errorf("read config: %w (run `clawdchan init` first)", err)
	}
	var c config
	if err := json.Unmarshal(data, &c); err != nil {
		return config{}, fmt.Errorf("parse config: %w", err)
	}
	if c.DataDir == "" {
		c.DataDir = dir
	}
	return c, nil
}

func saveConfig(c config) error {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.DataDir, configFileName), data, 0o600)
}

func openNode(_ context.Context, c config) (*node.Node, error) {
	n, err := node.New(node.Config{
		DataDir:  c.DataDir,
		RelayURL: c.RelayURL,
		Alias:    c.Alias,
	})
	if err != nil {
		return nil, err
	}
	return n, nil
}
