package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/agents-first/ClawdChan/core/pairing"
)

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

func cmdPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: clawdchan pair [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Generate a 12-word pairing code and block until the peer consumes it.")
		fmt.Fprintln(os.Stderr, "Share the code over a trusted channel (voice, Signal, in person) — that")
		fmt.Fprintln(os.Stderr, "channel is the security boundary, not the relay.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Terminal fallback only — the primary flow is from inside Claude Code:")
		fmt.Fprintln(os.Stderr, "    \"pair me with <name> via clawdchan\"")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
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
	fmt.Printf("Waiting for the peer (timeout %s)…\n", *timeout)

	progressCtx, stopProgress := context.WithCancel(ctx)
	defer stopProgress()
	go pairProgressTicker(progressCtx)

	res := <-ch
	stopProgress()
	if res.Err != nil {
		if errors.Is(res.Err, context.DeadlineExceeded) {
			return fmt.Errorf("timed out after %s; peer never consumed the code", *timeout)
		}
		return res.Err
	}
	fmt.Printf("Paired with %q (%s)\n", res.Peer.Alias, hex.EncodeToString(res.Peer.NodeID[:]))
	return nil
}

// pairProgressTicker prints a "still waiting" line every 30s so a user who
// invoked `clawdchan pair` can tell the command hasn't hung while the peer
// decides whether to consume.
func pairProgressTicker(ctx context.Context) {
	start := time.Now()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fmt.Printf("  …still waiting (%s elapsed)\n", time.Since(start).Truncate(time.Second))
		}
	}
}

func cmdConsume(args []string) error {
	fs := flag.NewFlagSet("consume", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: clawdchan consume <12 words>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Consume a peer's 12-word pairing mnemonic and complete the pairing.")
		fmt.Fprintln(os.Stderr, "Before consuming, confirm the words came directly from the intended peer")
		fmt.Fprintln(os.Stderr, "over a trusted channel — not forwarded via email or third-party chat.")
		fmt.Fprintln(os.Stderr, "Consuming an attacker's mnemonic pairs you with the attacker.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Terminal fallback only — the primary flow is from inside Claude Code:")
		fmt.Fprintln(os.Stderr, "    \"consume this clawdchan code: <12 words>\"")
	}
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 1 {
		fs.Usage()
		return errors.New("missing mnemonic")
	}
	mnemonic := strings.Join(rest, " ")

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
	return nil
}

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
