package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
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
	return nil
}

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
