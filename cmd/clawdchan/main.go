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
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/pairing"
	"github.com/vMaroon/ClawdChan/hosts/openclaw"
	"github.com/vMaroon/ClawdChan/internal/listenerreg"
)

type config struct {
	DataDir  string          `json:"data_dir"`
	RelayURL string          `json:"relay_url"`
	Alias    string          `json:"alias"`
	Dispatch *dispatchConfig `json:"agent_dispatch,omitempty"`

	// OpenClaw fields are optional. When OpenClawURL is empty, the daemon
	// runs in its default CC-only mode. When set, the daemon connects to the
	// gateway and routes inbound human-surface traffic into OpenClaw sessions
	// in addition to firing OS notifications for Claude Code.
	OpenClawURL      string `json:"openclaw_url,omitempty"`
	OpenClawToken    string `json:"openclaw_token,omitempty"`
	OpenClawDeviceID string `json:"openclaw_device_id,omitempty"`
}

// dispatchConfig controls the daemon's agent-cadence collab path. When
// enabled and a peer sends an envelope marked Content.Title="clawdchan:
// collab_sync", the daemon spawns Command with the ask on stdin and routes
// the subprocess's JSON answer back as a normal envelope instead of
// waiting for the human's next Claude Code turn. See
// core/policy/dispatch.go for the wire contract.
type dispatchConfig struct {
	Enabled         bool     `json:"enabled,omitempty"`
	Command         []string `json:"command,omitempty"`
	TimeoutSeconds  int      `json:"timeout_seconds,omitempty"`
	MaxContext      int      `json:"max_thread_context,omitempty"`
	MaxCollabRounds int      `json:"max_collab_rounds,omitempty"`
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

func openNode(ctx context.Context, c config) (*node.Node, error) {
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

// --- init -------------------------------------------------------------------

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dataDir := fs.String("data", defaultDataDir(), "data directory (holds config and sqlite store)")
	relay := fs.String("relay", defaultPublicRelay, "relay URL (ws:// or wss://)")
	alias := fs.String("alias", "", "display alias sent during pairing")
	writeMCP := fs.String("write-mcp", "", "also write a .mcp.json at this directory wired to this install's absolute clawdchan-mcp path")
	fs.Parse(args)

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	c := config{DataDir: *dataDir, RelayURL: *relay, Alias: *alias}
	if err := saveConfig(c); err != nil {
		return err
	}
	n, err := node.New(node.Config{DataDir: c.DataDir, RelayURL: c.RelayURL, Alias: c.Alias})
	if err != nil {
		return err
	}
	defer n.Close()
	fmt.Printf("initialized clawdchan node\n")
	fmt.Printf("  data dir: %s\n", c.DataDir)
	fmt.Printf("  relay:    %s\n", c.RelayURL)
	fmt.Printf("  alias:    %s\n", c.Alias)
	nid := n.Identity()
	fmt.Printf("  node id:  %s\n", hex.EncodeToString(nid[:]))

	mcpBin, mcpErr := resolveMCPBinary()
	if mcpErr != nil {
		fmt.Printf("\nclawdchan-mcp not on PATH: %v\n", mcpErr)
		fmt.Printf("Install the MCP server so Claude Code can launch it:\n")
		fmt.Printf("  make install    # then ensure $(go env GOPATH)/bin is on your PATH\n")
	} else {
		fmt.Printf("  mcp binary: %s\n", mcpBin)
	}

	if *writeMCP != "" {
		path, err := writeProjectMCP(*writeMCP, mcpBin)
		if err != nil {
			return fmt.Errorf("write .mcp.json: %w", err)
		}
		fmt.Printf("\nWrote %s\n", path)
		fmt.Printf("You must exit and restart your Claude Code session for the new MCP server to load.\n")
	} else {
		fmt.Printf("\nNext: add an .mcp.json to your project root, or rerun with -write-mcp <dir>.\n")
		fmt.Printf("After wiring MCP, exit and restart your Claude Code session.\n")
	}
	return nil
}

// resolveMCPBinary returns the absolute path to clawdchan-mcp, preferring PATH
// and falling back to $(go env GOPATH)/bin. Returns an error if neither works.
func resolveMCPBinary() (string, error) {
	if p, err := exec.LookPath("clawdchan-mcp"); err == nil {
		return p, nil
	}
	// Fallback: try $(go env GOPATH)/bin/clawdchan-mcp.
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			gopath = filepath.Join(home, "go")
		}
	}
	if gopath != "" {
		candidate := filepath.Join(gopath, "bin", "clawdchan-mcp")
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("clawdchan-mcp not found on PATH or in $(go env GOPATH)/bin; run `make install`")
}

func writeProjectMCP(dir, mcpBin string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ".mcp.json")
	bin := mcpBin
	if bin == "" {
		bin = "clawdchan-mcp"
	}
	payload := map[string]any{
		"mcpServers": map[string]any{
			"clawdchan": map[string]any{
				"command": bin,
			},
		},
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// --- whoami -----------------------------------------------------------------

func cmdWhoami(_ []string) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	n, err := openNode(context.Background(), c)
	if err != nil {
		return err
	}
	defer n.Close()
	nid := n.Identity()
	fmt.Printf("alias:   %s\n", c.Alias)
	fmt.Printf("node id: %s\n", hex.EncodeToString(nid[:]))
	fmt.Printf("relay:   %s\n", c.RelayURL)
	return nil
}

// --- pair -------------------------------------------------------------------

func cmdPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	alias := fs.String("alias", "", "override display alias for this pairing")
	timeout := fs.Duration("timeout", 5*time.Minute, "rendezvous timeout")
	fs.Parse(args)

	c, err := loadConfig()
	if err != nil {
		return err
	}
	if *alias != "" {
		c.Alias = *alias
	}
	n, err := openNode(context.Background(), c)
	if err != nil {
		return err
	}
	defer n.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	code, ch, err := n.Pair(ctx)
	if err != nil {
		return err
	}
	fmt.Println("Share this code with the other person, or run:")
	fmt.Println()
	fmt.Printf("    clawdchan consume %s\n", code.Mnemonic())
	fmt.Println()
	fmt.Println("Waiting for the peer…")

	res := <-ch
	if res.Err != nil {
		return res.Err
	}
	fmt.Printf("Paired with %q (%s)\n", res.Peer.Alias, hex.EncodeToString(res.Peer.NodeID[:]))
	fmt.Printf("SAS: %s\n", strings.Join(res.Peer.SAS[:], "-"))
	fmt.Println("Confirm this SAS matches on both sides (voice, in person, a trusted channel) before sending anything sensitive.")
	return nil
}

// --- consume ----------------------------------------------------------------

func cmdConsume(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: clawdchan consume <12 words>")
	}
	mnemonic := strings.Join(args, " ")

	c, err := loadConfig()
	if err != nil {
		return err
	}
	n, err := openNode(context.Background(), c)
	if err != nil {
		return err
	}
	defer n.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	peer, err := n.Consume(ctx, mnemonic)
	if err != nil {
		return err
	}
	fmt.Printf("Paired with %q (%s)\n", peer.Alias, hex.EncodeToString(peer.NodeID[:]))
	fmt.Printf("SAS: %s\n", strings.Join(peer.SAS[:], "-"))
	fmt.Println("Confirm this SAS matches on both sides (voice, in person, a trusted channel) before sending anything sensitive.")
	return nil
}

// --- peers ------------------------------------------------------------------

func cmdPeers(_ []string) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	n, err := openNode(context.Background(), c)
	if err != nil {
		return err
	}
	defer n.Close()
	peers, err := n.ListPeers(context.Background())
	if err != nil {
		return err
	}
	if len(peers) == 0 {
		fmt.Println("no peers")
		return nil
	}
	for _, p := range peers {
		trust := map[pairing.Trust]string{pairing.TrustPaired: "paired", pairing.TrustBridged: "bridged", pairing.TrustRevoked: "revoked"}[p.Trust]
		fmt.Printf("%s  %s  %-12s  paired=%s\n",
			hex.EncodeToString(p.NodeID[:])[:16],
			trust,
			p.Alias,
			time.UnixMilli(p.PairedAtMs).Format(time.RFC3339))
	}
	return nil
}

// --- threads ----------------------------------------------------------------

func cmdThreads(_ []string) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	n, err := openNode(context.Background(), c)
	if err != nil {
		return err
	}
	defer n.Close()
	threads, err := n.ListThreads(context.Background())
	if err != nil {
		return err
	}
	if len(threads) == 0 {
		fmt.Println("no threads")
		return nil
	}
	for _, t := range threads {
		fmt.Printf("%s  peer=%s  topic=%q\n",
			hex.EncodeToString(t.ID[:]),
			hex.EncodeToString(t.PeerID[:])[:16],
			t.Topic)
	}
	return nil
}

// --- open -------------------------------------------------------------------

func cmdOpen(args []string) error {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	topic := fs.String("topic", "", "thread topic")
	fs.Parse(args)

	rest := fs.Args()
	if len(rest) < 1 {
		return errors.New("usage: clawdchan open <peer-hex> [-topic T]")
	}
	peerID, err := parseNodeID(rest[0])
	if err != nil {
		return err
	}

	c, err := loadConfig()
	if err != nil {
		return err
	}
	n, err := openNode(context.Background(), c)
	if err != nil {
		return err
	}
	defer n.Close()

	tid, err := n.OpenThread(context.Background(), peerID, *topic)
	if err != nil {
		return err
	}
	fmt.Println(hex.EncodeToString(tid[:]))
	return nil
}

// --- send -------------------------------------------------------------------

func cmdSend(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: clawdchan send <thread-hex-or-prefix> <text...>")
	}
	text := strings.Join(args[1:], " ")

	c, err := loadConfig()
	if err != nil {
		return err
	}
	n, err := openNode(context.Background(), c)
	if err != nil {
		return err
	}
	defer n.Close()

	threadID, err := resolveThread(context.Background(), n, args[0])
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := n.Start(ctx); err != nil {
		return err
	}
	defer n.Stop()
	if err := n.Send(ctx, threadID, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: text}); err != nil {
		return err
	}
	fmt.Println("sent")
	return nil
}

// --- listen -----------------------------------------------------------------

func cmdListen(args []string) error {
	fs := flag.NewFlagSet("listen", flag.ExitOnError)
	follow := fs.Bool("follow", true, "follow all threads; print new envelopes to stdout")
	tail := fs.Int("tail", -1, "replay only the last N envelopes per thread before live traffic. -1 = all history, 0 = no replay")
	fs.Parse(args)

	c, err := loadConfig()
	if err != nil {
		return err
	}
	n, err := openNode(context.Background(), c)
	if err != nil {
		return err
	}
	defer n.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := n.Start(ctx); err != nil {
		return err
	}
	defer n.Stop()
	nid := n.Identity()
	fmt.Printf("clawdchan listening (relay=%s, node=%s)\n", c.RelayURL, hex.EncodeToString(nid[:]))
	fmt.Println("legend: '->' = sent by this node, '<-' = received. role is 'agent' or 'human'. thread is full 32-hex id.")

	unregister, regErr := listenerreg.Register(
		c.DataDir, listenerreg.KindCLI,
		hex.EncodeToString(nid[:]), c.RelayURL, c.Alias,
	)
	if regErr != nil {
		fmt.Fprintf(os.Stderr, "warn: could not register listener: %v\n", regErr)
	}
	defer unregister()

	if *follow {
		go followAll(ctx, n, *tail)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	return nil
}

func followAll(ctx context.Context, n *node.Node, tail int) {
	seen := make(map[envelope.ULID]bool)

	// Initial replay pass.
	threads, err := n.ListThreads(ctx)
	if err == nil {
		for _, th := range threads {
			envs, err := n.ListEnvelopes(ctx, th.ID, 0)
			if err != nil {
				continue
			}
			start := 0
			if tail == 0 {
				start = len(envs)
			} else if tail > 0 && len(envs) > tail {
				start = len(envs) - tail
			}
			for i := start; i < len(envs); i++ {
				e := envs[i]
				seen[e.EnvelopeID] = true
				printEnvelope(e, n.Identity())
			}
			// Mark envelopes we skipped during tail trimming as seen so they
			// don't re-appear in the live phase.
			for i := 0; i < start; i++ {
				seen[envs[i].EnvelopeID] = true
			}
		}
	}
	fmt.Println("--- live ---")

	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		threads, err := n.ListThreads(ctx)
		if err != nil {
			continue
		}
		for _, th := range threads {
			envs, err := n.ListEnvelopes(ctx, th.ID, 0)
			if err != nil {
				continue
			}
			for _, env := range envs {
				if seen[env.EnvelopeID] {
					continue
				}
				seen[env.EnvelopeID] = true
				printEnvelope(env, n.Identity())
			}
		}
	}
}

// --- inspect ----------------------------------------------------------------

func cmdInspect(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: clawdchan inspect <thread-hex-or-prefix>")
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	n, err := openNode(context.Background(), c)
	if err != nil {
		return err
	}
	defer n.Close()
	threadID, err := resolveThread(context.Background(), n, args[0])
	if err != nil {
		return err
	}
	envs, err := n.ListEnvelopes(context.Background(), threadID, 0)
	if err != nil {
		return err
	}
	for _, env := range envs {
		printEnvelope(env, n.Identity())
	}
	return nil
}

// resolveThread accepts either a full 32-hex thread id or a unique prefix
// matching one of the node's existing threads.
func resolveThread(ctx context.Context, n *node.Node, s string) (envelope.ThreadID, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if len(s) == 32 {
		return parseThreadID(s)
	}
	if s == "" {
		return envelope.ThreadID{}, errors.New("empty thread id")
	}
	threads, err := n.ListThreads(ctx)
	if err != nil {
		return envelope.ThreadID{}, err
	}
	var matches []envelope.ThreadID
	for _, t := range threads {
		h := hex.EncodeToString(t.ID[:])
		if strings.HasPrefix(h, s) {
			matches = append(matches, t.ID)
		}
	}
	if len(matches) == 0 {
		return envelope.ThreadID{}, fmt.Errorf("no thread matches prefix %q", s)
	}
	if len(matches) > 1 {
		return envelope.ThreadID{}, fmt.Errorf("prefix %q is ambiguous (%d matches)", s, len(matches))
	}
	return matches[0], nil
}

// --- doctor -----------------------------------------------------------------

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	timeout := fs.Duration("timeout", 5*time.Second, "relay connect timeout")
	fs.Parse(args)

	fmt.Println("clawdchan doctor")

	// 1. config
	cfgPath := filepath.Join(defaultDataDir(), configFileName)
	c, cfgErr := loadConfig()
	if cfgErr != nil {
		fmt.Printf("  [FAIL] config: %v\n", cfgErr)
		fmt.Printf("         run: clawdchan init -relay <url> -alias <name>\n")
		return cfgErr
	}
	fmt.Printf("  [ok]  config: %s\n", cfgPath)
	fmt.Printf("         data dir: %s\n", c.DataDir)
	fmt.Printf("         relay:    %s\n", c.RelayURL)
	fmt.Printf("         alias:    %s\n", c.Alias)

	// 2. relay URL shape
	if err := checkRelayURL(c.RelayURL); err != nil {
		fmt.Printf("  [WARN] relay url: %v\n", err)
	}

	// 3. clawdchan CLI on PATH
	if p, err := exec.LookPath("clawdchan"); err == nil {
		fmt.Printf("  [ok]  clawdchan on PATH: %s\n", p)
	} else {
		fmt.Printf("  [WARN] clawdchan not on PATH. You are running: %s\n", firstNonEmpty(os.Args[0], "?"))
	}

	// 4. clawdchan-mcp discoverable
	mcpBin, mcpErr := resolveMCPBinary()
	if mcpErr != nil {
		fmt.Printf("  [FAIL] clawdchan-mcp: %v\n", mcpErr)
		fmt.Printf("         Claude Code's .mcp.json needs this binary on PATH, or an absolute\n")
		fmt.Printf("         path. Fix with `make install` then add $(go env GOPATH)/bin to PATH,\n")
		fmt.Printf("         or rerun `clawdchan init -write-mcp <project-dir>` to hardcode the path.\n")
	} else {
		fmt.Printf("  [ok]  clawdchan-mcp: %s\n", mcpBin)
	}

	// 5. node / identity / store
	n, err := openNode(context.Background(), c)
	if err != nil {
		fmt.Printf("  [FAIL] open node: %v\n", err)
		return err
	}
	defer n.Close()
	id := n.Identity()
	fmt.Printf("  [ok]  identity loaded: node id %s\n", hex.EncodeToString(id[:]))

	// 6. relay reachability
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := n.Start(ctx); err != nil {
		fmt.Printf("  [FAIL] relay connect: %v\n", err)
		return err
	}
	defer n.Stop()
	fmt.Printf("  [ok]  relay reachable\n")

	// Minimum-change OpenClaw config surface: detect active daemon OpenClaw mode
	// from the listener registry entry the daemon writes at startup.
	if openClawCfg, ok := activeOpenClawConfig(c.DataDir, hex.EncodeToString(id[:])); ok {
		fmt.Printf("  [ok]  openclaw mode active: %s\n", openClawCfg.OpenClawURL)
		openClawCtx, openClawCancel := context.WithTimeout(context.Background(), *timeout)
		defer openClawCancel()
		if err := checkOpenClawGateway(openClawCtx, openClawCfg); err != nil {
			fmt.Printf("  [FAIL] openclaw gateway connect: %v\n", err)
			fmt.Printf("         %s\n", openClawRemediation(err, openClawCfg.OpenClawURL))
			return err
		}
		fmt.Printf("  [ok]  openclaw gateway reachable\n")
	}

	// 7. peers / threads summary
	peers, _ := n.ListPeers(context.Background())
	threads, _ := n.ListThreads(context.Background())
	fmt.Printf("  [ok]  peers: %d, threads: %d\n", len(peers), len(threads))

	if mcpErr != nil {
		return mcpErr
	}
	fmt.Println("all checks passed")
	return nil
}

func checkRelayURL(raw string) error {
	if raw == "" {
		return errors.New("relay url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	switch u.Scheme {
	case "ws", "wss", "http", "https":
		return nil
	default:
		return fmt.Errorf("unexpected scheme %q (want ws/wss/http/https)", u.Scheme)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func activeOpenClawConfig(dataDir, nodeID string) (listenerreg.Entry, bool) {
	entries, err := listenerreg.List(dataDir)
	if err != nil {
		return listenerreg.Entry{}, false
	}
	var best listenerreg.Entry
	ok := false
	for _, e := range entries {
		if e.Kind != listenerreg.KindCLI || !strings.EqualFold(e.NodeID, nodeID) || !e.OpenClawHostActive {
			continue
		}
		if !ok || e.StartedMs > best.StartedMs {
			best = e
			ok = true
		}
	}
	return best, ok
}

func checkOpenClawGateway(ctx context.Context, cfg listenerreg.Entry) error {
	bridge := openclaw.NewBridge(cfg.OpenClawURL, cfg.OpenClawToken, firstNonEmpty(cfg.OpenClawDeviceID, "clawdchan-daemon"), nil)
	if err := bridge.Connect(ctx); err != nil {
		return err
	}
	return bridge.Close()
}

func openClawRemediation(err error, wsURL string) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "401"), strings.Contains(msg, "403"), strings.Contains(msg, "unauthorized"), strings.Contains(msg, "forbidden"), strings.Contains(msg, "auth"):
		return "gateway rejected the token; update daemon -openclaw-token and restart the daemon service."
	case strings.Contains(msg, "dial"), strings.Contains(msg, "connection refused"), strings.Contains(msg, "no such host"), strings.Contains(msg, "timeout"):
		return fmt.Sprintf("gateway unreachable; ensure OpenClaw is listening at %s and daemon -openclaw matches that URL.", wsURL)
	default:
		return "check OpenClaw gateway logs and daemon -openclaw/-openclaw-token flags for mismatches."
	}
}

// --- helpers ----------------------------------------------------------------

func parseNodeID(s string) (identity.NodeID, error) {
	s = strings.TrimSpace(s)
	b, err := hex.DecodeString(s)
	if err != nil {
		return identity.NodeID{}, fmt.Errorf("bad node id hex: %w", err)
	}
	if len(b) != len(identity.NodeID{}) {
		return identity.NodeID{}, fmt.Errorf("node id must be %d bytes hex", len(identity.NodeID{}))
	}
	var id identity.NodeID
	copy(id[:], b)
	return id, nil
}

func parseThreadID(s string) (envelope.ThreadID, error) {
	s = strings.TrimSpace(s)
	b, err := hex.DecodeString(s)
	if err != nil {
		return envelope.ThreadID{}, fmt.Errorf("bad thread id hex: %w", err)
	}
	if len(b) != 16 {
		return envelope.ThreadID{}, fmt.Errorf("thread id must be 16 bytes hex")
	}
	var id envelope.ThreadID
	copy(id[:], b)
	return id, nil
}

func printEnvelope(env envelope.Envelope, me identity.NodeID) {
	dir := "<-"
	if env.From.NodeID == me {
		dir = "->"
	}
	role := "agent"
	if env.From.Role == envelope.RoleHuman {
		role = "human"
	}
	fmt.Printf("[%s] %s %s/%s  thread=%s  %s\n",
		time.UnixMilli(env.CreatedAtMs).Format(time.RFC3339),
		dir, env.From.Alias, role,
		hex.EncodeToString(env.ThreadID[:]),
		renderContent(env.Intent, env.Content))
}

func renderContent(intent envelope.Intent, c envelope.Content) string {
	tag := intentName(intent)
	switch c.Kind {
	case envelope.ContentText:
		return fmt.Sprintf("%s: %s", tag, c.Text)
	case envelope.ContentDigest:
		return fmt.Sprintf("%s digest: %s — %s", tag, c.Title, c.Body)
	default:
		return tag
	}
}

func intentName(i envelope.Intent) string {
	switch i {
	case envelope.IntentSay:
		return "say"
	case envelope.IntentAsk:
		return "ask"
	case envelope.IntentNotifyHuman:
		return "notify-human"
	case envelope.IntentAskHuman:
		return "ask-human"
	case envelope.IntentHandoff:
		return "handoff"
	case envelope.IntentAck:
		return "ack"
	case envelope.IntentClose:
		return "close"
	default:
		return fmt.Sprintf("intent(%d)", i)
	}
}
