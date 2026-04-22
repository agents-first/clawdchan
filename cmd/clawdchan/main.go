// Command clawdchan is the reference CLI for a ClawdChan node.
//
// Subcommands:
//
//	clawdchan setup     [-y] [-cc-perm-scope …]        one-command onboarding: init + PATH + daemon
//	clawdchan init      [-data DIR] [-relay URL] [-alias NAME] [-write-mcp DIR]
//	clawdchan whoami                                    print node id and alias
//	clawdchan pair      [-alias NAME]                   print code; wait for peer
//	clawdchan consume   <mnemonic...>                   consume a peer's code
//	clawdchan peers                                     list paired peers
//	clawdchan peer      show|rename|revoke|remove …     manage one peer (destructive ops are CLI-only)
//	clawdchan threads                                   list threads
//	clawdchan open      <peer-hex> [-topic T]           open a new thread (scripting aid)
//	clawdchan send      <thread-hex-or-prefix> <text>   send text on a thread (scripting aid)
//	clawdchan listen    [-follow] [-tail N]             run node; print inbound
//	clawdchan daemon    run|install|uninstall|status    background listener with OS notifications
//	clawdchan path-setup                                put $GOPATH/bin on your shell PATH
//	clawdchan inspect   <thread-hex-or-prefix>          print envelopes on thread (debugging)
//	clawdchan doctor                                    diagnose install and link
//
// On first run, `clawdchan setup` (or `init`) creates ~/.clawdchan/config.json
// and the SQLite store. Most subcommands start a node against the configured
// relay; `listen` stays attached to receive traffic, while one-shot commands
// exit as soon as their work is done (messages to offline peers are queued
// at the relay).
//
// The MCP tool surface — four tools, clawdchan_toolkit / _pair / _message /
// _inbox — is a separate concern; see docs/mcp.md. Destructive peer ops
// (rename/revoke/remove) live only here on the CLI by design.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/agents-first/clawdchan/core/node"
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
const defaultPublicRelay = "wss://clawdchan-relay.fly.dev"

// defaultDataDir returns the per-user data directory for the node.
// Unix-y platforms keep the dotfile-style ~/.clawdchan that's been the
// convention since v0. Windows uses %AppData%\clawdchan via
// os.UserConfigDir — a dot-prefixed directory under the user's home is
// hidden by default in Explorer, which made identities hard to find.
// $CLAWDCHAN_HOME still wins on every platform.
func defaultDataDir() string {
	if v := os.Getenv("CLAWDCHAN_HOME"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if cfg, err := os.UserConfigDir(); err == nil && cfg != "" {
			return filepath.Join(cfg, "clawdchan")
		}
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
	case "try":
		err = cmdTry(args)
	case "uninstall":
		err = cmdUninstall(args)
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
  setup       One-command onboarding: init + PATH + daemon (interactive)
  try         Solo loopback demo — pairs two ephemeral nodes and round-trips a message
  init        Create config and identity (non-interactive)
  whoami      Print this node's id and alias
  pair        Generate a pairing code and wait for the peer (terminal fallback; CC is primary)
  consume     Enter a peer's pairing code (terminal fallback; CC is primary)
  peers       List paired peers
  peer        Manage one peer: show | rename | revoke | remove
  threads     List conversation threads
  open        Open a new thread with a peer (scripting aid)
  send        Send a plain-text message on a thread (scripting aid)
  listen      Foreground inspector — prints traffic to stdout (use 'daemon install' for persistence)
  daemon      Persistent background listener with OS notifications
              Subcommands: install | setup | uninstall | status | run
  path-setup  Put $GOPATH/bin on your shell PATH (zsh/bash/fish)
  inspect     Print envelopes on a thread (debugging)
  doctor      Diagnose install, config, and relay connectivity
  uninstall   Reverse setup — daemon service, data dir, MCP/permission hints

Run 'clawdchan <command> -h' for per-command help.

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
		return config{}, fmt.Errorf("parse config: %w (run `clawdchan doctor` for diagnostics)", err)
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
