package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/node"
)

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
