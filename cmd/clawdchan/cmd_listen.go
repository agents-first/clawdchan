package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agents-first/ClawdChan/core/envelope"
	"github.com/agents-first/ClawdChan/core/node"
	"github.com/agents-first/ClawdChan/internal/listenerreg"
)

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
