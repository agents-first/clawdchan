package node

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agents-first/clawdchan/core/envelope"
	"github.com/agents-first/clawdchan/core/identity"
	"github.com/agents-first/clawdchan/core/pairing"
	"github.com/agents-first/clawdchan/core/transport"
)

func TestTryDrainOutboxKeepsFailedAndRemainingEntries(t *testing.T) {
	ctx := context.Background()
	n, err := New(Config{
		DataDir:  t.TempDir(),
		RelayURL: "ws://relay.invalid",
		Alias:    "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })

	peerID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	peer := pairing.Peer{
		NodeID:     peerID.SigningPublic,
		KexPub:     peerID.KexPublic,
		Alias:      "bob",
		Trust:      pairing.TrustPaired,
		PairedAtMs: time.Now().UnixMilli(),
	}
	if err := n.store.UpsertPeer(ctx, peer); err != nil {
		t.Fatal(err)
	}
	thread, err := n.OpenThread(ctx, peer.NodeID, "outbox")
	if err != nil {
		t.Fatal(err)
	}

	n.mu.Lock()
	n.link = &outboxTestLink{failAll: true}
	n.mu.Unlock()
	for _, text := range []string{"first", "second", "third"} {
		if err := n.Send(ctx, thread, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: text}); err != nil {
			t.Fatalf("queue %q: %v", text, err)
		}
	}

	queued, err := n.store.ListOutbox(ctx, peer.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 3 {
		t.Fatalf("expected 3 queued entries, got %d", len(queued))
	}

	link := &outboxTestLink{failAt: 2}
	n.mu.Lock()
	n.link = link
	n.mu.Unlock()
	n.tryDrainOutbox()

	queued, err = n.store.ListOutbox(ctx, peer.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 2 {
		t.Fatalf("expected failed entry plus untouched remainder, got %d", len(queued))
	}
	if queued[0].Envelope.Content.Text != "second" || queued[1].Envelope.Content.Text != "third" {
		t.Fatalf("wrong outbox contents after partial drain: %q, %q",
			queued[0].Envelope.Content.Text, queued[1].Envelope.Content.Text)
	}
	if link.sends != 2 {
		t.Fatalf("expected drain to stop after failed send, got %d sends", link.sends)
	}
}

type outboxTestLink struct {
	failAll bool
	failAt  int
	sends   int
}

func (l *outboxTestLink) Send(context.Context, identity.NodeID, []byte) error {
	l.sends++
	if l.failAll || l.sends == l.failAt {
		return errors.New("send failed")
	}
	return nil
}

func (*outboxTestLink) Recv(context.Context) (transport.Frame, error) {
	return transport.Frame{}, errors.New("unused")
}

func (*outboxTestLink) Events() <-chan transport.CtlEvent {
	return make(chan transport.CtlEvent)
}

func (*outboxTestLink) Close() error {
	return nil
}
