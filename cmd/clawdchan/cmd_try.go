package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agents-first/clawdchan/core/envelope"
	"github.com/agents-first/clawdchan/core/node"
)

// cmdTry runs a self-contained loopback demo: it spins up two
// ephemeral nodes in temporary data dirs, pairs them over the
// configured relay, and round-trips a single message. It's the
// "can I see it work without recruiting a second human" path.
//
// The two nodes are completely isolated from the user's real
// ~/.clawdchan — no identity, peers, or threads touched. Temp dirs
// are deleted on exit unless -keep is passed.
func cmdTry(args []string) error {
	fs := flag.NewFlagSet("try", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: clawdchan try [-relay URL] [-keep] [-timeout DURATION]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Solo loopback demo: spins up two ephemeral nodes (alice-demo and")
		fmt.Fprintln(os.Stderr, "bob-demo) against the configured relay, pairs them, and round-trips")
		fmt.Fprintln(os.Stderr, "a message. Useful for verifying install and relay reach without")
		fmt.Fprintln(os.Stderr, "recruiting a second human. Touches nothing in your real ~/.clawdchan.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	relay := fs.String("relay", "", "relay URL (default: the one in ~/.clawdchan/config.json, else the public relay)")
	keep := fs.Bool("keep", false, "keep the two ephemeral data dirs after exit")
	timeout := fs.Duration("timeout", 45*time.Second, "overall timeout")
	fs.Parse(args)

	relayURL := *relay
	if relayURL == "" {
		if c, err := loadConfig(); err == nil && c.RelayURL != "" {
			relayURL = c.RelayURL
		} else {
			relayURL = defaultPublicRelay
		}
	}

	tmp, err := os.MkdirTemp("", "clawdchan-try-*")
	if err != nil {
		return fmt.Errorf("mkdir tmp: %w", err)
	}
	cleanup := func() {
		if *keep {
			fmt.Printf("\n(kept temp dirs: %s)\n", tmp)
			return
		}
		_ = os.RemoveAll(tmp)
	}
	defer cleanup()

	dirA := filepath.Join(tmp, "alice")
	dirB := filepath.Join(tmp, "bob")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}

	fmt.Println("🐾 ClawdChan loopback demo")
	fmt.Printf("   relay:  %s\n", relayURL)
	fmt.Printf("   alice:  %s\n", dirA)
	fmt.Printf("   bob:    %s\n", dirB)
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	nA, err := node.New(node.Config{DataDir: dirA, RelayURL: relayURL, Alias: "alice-demo"})
	if err != nil {
		return fmt.Errorf("alice: %w", err)
	}
	defer nA.Close()
	nB, err := node.New(node.Config{DataDir: dirB, RelayURL: relayURL, Alias: "bob-demo"})
	if err != nil {
		return fmt.Errorf("bob: %w", err)
	}
	defer nB.Close()

	if err := nA.Start(ctx); err != nil {
		return fmt.Errorf("alice start (is the relay reachable?): %w", err)
	}
	defer nA.Stop()
	if err := nB.Start(ctx); err != nil {
		return fmt.Errorf("bob start: %w", err)
	}
	defer nB.Stop()
	fmt.Println("[1/5] both ephemeral nodes online")

	code, ch, err := nA.Pair(ctx)
	if err != nil {
		return fmt.Errorf("alice pair: %w", err)
	}
	first3 := firstNWords(code.Mnemonic(), 3)
	fmt.Printf("[2/5] alice generated a 12-word code (%s…)\n", first3)

	peerFromBob, err := nB.Consume(ctx, code.Mnemonic())
	if err != nil {
		return fmt.Errorf("bob consume: %w", err)
	}
	res := <-ch
	if res.Err != nil {
		return fmt.Errorf("alice pair result: %w", res.Err)
	}
	fmt.Printf("[3/5] paired — alice=%s bob=%s\n",
		hex.EncodeToString(res.Peer.NodeID[:])[:16],
		hex.EncodeToString(peerFromBob.NodeID[:])[:16])

	thread, err := nA.OpenThread(ctx, res.Peer.NodeID, "loopback")
	if err != nil {
		return fmt.Errorf("alice open thread: %w", err)
	}
	sub, unsub := nB.Subscribe(thread)
	defer unsub()

	msg := "hello from alice — if you're reading this, round-trip works."
	if err := nA.Send(ctx, thread, envelope.IntentSay, envelope.Content{
		Kind: envelope.ContentText,
		Text: msg,
	}); err != nil {
		return fmt.Errorf("alice send: %w", err)
	}
	fmt.Println("[4/5] alice sent: " + msg)

	select {
	case env := <-sub:
		fmt.Printf("[5/5] bob received: %s\n", env.Content.Text)
	case <-ctx.Done():
		return errors.New("timed out waiting for bob to receive — check relay reachability with `clawdchan doctor`")
	}

	fmt.Println()
	fmt.Println("✅ ClawdChan round-trip works. To pair with a real peer, run:")
	fmt.Println("    clawdchan pair              # then share the 12 words")
	fmt.Println("    clawdchan consume <words>   # when they share theirs")
	return nil
}

func firstNWords(s string, n int) string {
	fs := strings.Fields(s)
	if len(fs) <= n {
		return s
	}
	return strings.Join(fs[:n], " ")
}
