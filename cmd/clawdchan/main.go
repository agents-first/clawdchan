// Command clawdchan is the reference CLI for a ClawdChan node.
//
// Subcommands:
//
//	clawdchan init      [-data DIR] [-relay URL] [-alias NAME]
//	clawdchan whoami
//	clawdchan pair      [-alias NAME]            print code; wait for peer
//	clawdchan consume   <mnemonic...>             consume a code
//	clawdchan peers
//	clawdchan threads
//	clawdchan open      <peer-hex> [-topic T]     open a new thread
//	clawdchan send      <thread-hex> <text>
//	clawdchan listen    [-follow]                 run node; print inbound
//	clawdchan inspect   <thread-hex>              print envelopes on thread
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
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/pairing"
)

type config struct {
	DataDir  string `json:"data_dir"`
	RelayURL string `json:"relay_url"`
	Alias    string `json:"alias"`
}

const configFileName = "config.json"

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
	case "threads":
		err = cmdThreads(args)
	case "open":
		err = cmdOpen(args)
	case "send":
		err = cmdSend(args)
	case "listen":
		err = cmdListen(args)
	case "inspect":
		err = cmdInspect(args)
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
  init      Create config and identity
  whoami    Print this node's id and alias
  pair      Generate a pairing code and wait for the peer
  consume   Enter a peer's pairing code
  peers     List paired peers
  threads   List conversation threads
  open      Open a new thread with a peer
  send      Send a message on a thread
  listen    Stay connected to receive traffic
  inspect   Print envelopes on a thread

Config lives at $CLAWDCHAN_HOME or ~/.clawdchan.`)
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
	relay := fs.String("relay", "ws://localhost:8787", "relay URL (ws:// or wss://)")
	alias := fs.String("alias", "", "display alias sent during pairing")
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
	return nil
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
	fmt.Printf("Verify the 4-word code out-of-band: %s\n", strings.Join(res.Peer.SAS[:], "-"))
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
	fmt.Printf("Verify the 4-word code out-of-band: %s\n", strings.Join(peer.SAS[:], "-"))
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
		return errors.New("usage: clawdchan send <thread-hex> <text...>")
	}
	threadID, err := parseThreadID(args[0])
	if err != nil {
		return err
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
	fmt.Printf("clawdchan listening (relay=%s, node=%s)\n", c.RelayURL, hex.EncodeToString(nid[:])[:16])

	if *follow {
		go followAll(ctx, n)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	return nil
}

func followAll(ctx context.Context, n *node.Node) {
	seen := make(map[envelope.ULID]bool)
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
		return errors.New("usage: clawdchan inspect <thread-hex>")
	}
	threadID, err := parseThreadID(args[0])
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
	envs, err := n.ListEnvelopes(context.Background(), threadID, 0)
	if err != nil {
		return err
	}
	for _, env := range envs {
		printEnvelope(env, n.Identity())
	}
	return nil
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
	intent := intentName(env.Intent)
	fmt.Printf("[%s] %s %s/%s  thread=%s  %s\n",
		time.UnixMilli(env.CreatedAtMs).Format(time.RFC3339),
		dir, env.From.Alias, role,
		hex.EncodeToString(env.ThreadID[:])[:16],
		renderContent(env.Intent, env.Content))
	_ = intent
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
