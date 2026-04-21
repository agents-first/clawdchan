package node_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agents-first/ClawdChan/core/envelope"
	"github.com/agents-first/ClawdChan/core/node"
	"github.com/agents-first/ClawdChan/core/surface"
	"github.com/agents-first/ClawdChan/internal/relayserver"
)

type captureHuman struct {
	mu       sync.Mutex
	notified []envelope.Envelope
	reply    envelope.Content
	askErr   error
}

func (c *captureHuman) Notify(_ context.Context, _ envelope.ThreadID, env envelope.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notified = append(c.notified, env)
	return nil
}
func (c *captureHuman) Ask(context.Context, envelope.ThreadID, envelope.Envelope) (envelope.Content, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reply, c.askErr
}
func (c *captureHuman) Reachability() surface.Reachability                     { return surface.ReachableSync }
func (c *captureHuman) PresentThread(context.Context, envelope.ThreadID) error { return nil }

func spinRelay(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(relayserver.New(relayserver.Config{PairRendezvousTTL: 5 * time.Second}).Handler())
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func mkNode(t *testing.T, relay, alias string, h surface.HumanSurface) *node.Node {
	t.Helper()
	n, err := node.New(node.Config{
		DataDir:  t.TempDir(),
		RelayURL: relay,
		Alias:    alias,
		Human:    h,
	})
	if err != nil {
		t.Fatalf("new %s: %v", alias, err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func TestPairSendReceive(t *testing.T) {
	relay := spinRelay(t)
	alice := mkNode(t, relay, "alice", nil)
	bobHuman := &captureHuman{}
	bob := mkNode(t, relay, "bob", bobHuman)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if err := alice.Start(ctx); err != nil {
		t.Fatalf("alice start: %v", err)
	}
	if err := bob.Start(ctx); err != nil {
		t.Fatalf("bob start: %v", err)
	}

	code, pairCh, err := alice.Pair(ctx)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	bobPeer, err := bob.Consume(ctx, code.Mnemonic())
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if bobPeer.NodeID != alice.Identity() {
		t.Fatal("bob sees wrong peer id for alice")
	}
	res := <-pairCh
	if res.Err != nil {
		t.Fatalf("alice pair result: %v", res.Err)
	}
	if res.Peer.NodeID != bob.Identity() {
		t.Fatal("alice sees wrong peer id for bob")
	}

	thread, err := alice.OpenThread(ctx, bob.Identity(), "test-thread")
	if err != nil {
		t.Fatalf("open thread: %v", err)
	}

	// Plain agent-to-agent message.
	if err := alice.Send(ctx, thread, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: "hi bob"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		envs, err := bob.ListEnvelopes(ctx, thread, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(envs) >= 1 {
			if envs[0].Content.Text != "hi bob" {
				t.Fatalf("wrong content: %q", envs[0].Content.Text)
			}
			if err := envelope.Verify(envs[0]); err != nil {
				t.Fatalf("verify: %v", err)
			}
			goto saySuccess
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("bob never received the Say message")
saySuccess:

	// NotifyHuman path.
	if err := alice.Send(ctx, thread, envelope.IntentNotifyHuman, envelope.Content{Kind: envelope.ContentText, Text: "please be aware"}); err != nil {
		t.Fatalf("send notify: %v", err)
	}
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		bobHuman.mu.Lock()
		got := len(bobHuman.notified)
		bobHuman.mu.Unlock()
		if got >= 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("bob's human was never notified")
}

func TestAskHumanRoundTrip(t *testing.T) {
	relay := spinRelay(t)
	alice := mkNode(t, relay, "alice", nil)
	bobHuman := &captureHuman{reply: envelope.Content{Kind: envelope.ContentText, Text: "yes"}}
	bob := mkNode(t, relay, "bob", bobHuman)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	alice.Start(ctx)
	bob.Start(ctx)

	code, pairCh, _ := alice.Pair(ctx)
	if _, err := bob.Consume(ctx, code.Mnemonic()); err != nil {
		t.Fatal(err)
	}
	<-pairCh

	thread, err := alice.OpenThread(ctx, bob.Identity(), "ask")
	if err != nil {
		t.Fatal(err)
	}

	if err := alice.Send(ctx, thread, envelope.IntentAskHuman, envelope.Content{Kind: envelope.ContentText, Text: "should we proceed?"}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		envs, _ := alice.ListEnvelopes(ctx, thread, 0)
		for _, e := range envs {
			if e.From.Role == envelope.RoleHuman && e.Content.Text == "yes" {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("alice never received bob's human reply")
}

func TestAskHumanAsyncReplyDoesNotAutoReply(t *testing.T) {
	relay := spinRelay(t)
	alice := mkNode(t, relay, "alice", nil)
	bobHuman := &captureHuman{
		reply:  envelope.Content{Kind: envelope.ContentText, Text: "should-not-send"},
		askErr: surface.ErrAsyncReply,
	}
	bob := mkNode(t, relay, "bob", bobHuman)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if err := alice.Start(ctx); err != nil {
		t.Fatalf("alice start: %v", err)
	}
	if err := bob.Start(ctx); err != nil {
		t.Fatalf("bob start: %v", err)
	}

	code, pairCh, err := alice.Pair(ctx)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if _, err := bob.Consume(ctx, code.Mnemonic()); err != nil {
		t.Fatalf("consume: %v", err)
	}
	res := <-pairCh
	if res.Err != nil {
		t.Fatalf("alice pair result: %v", res.Err)
	}

	thread, err := alice.OpenThread(ctx, bob.Identity(), "ask-async")
	if err != nil {
		t.Fatalf("open thread: %v", err)
	}

	if err := alice.Send(ctx, thread, envelope.IntentAskHuman, envelope.Content{Kind: envelope.ContentText, Text: "async?"}); err != nil {
		t.Fatalf("send ask_human: %v", err)
	}

	noReplyDeadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(noReplyDeadline) {
		envs, err := alice.ListEnvelopes(ctx, thread, 0)
		if err != nil {
			t.Fatalf("list envelopes: %v", err)
		}
		for _, e := range envs {
			if e.From.Role == envelope.RoleHuman {
				t.Fatalf("unexpected auto human reply: %+v", e)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestHasPendingAsk(t *testing.T) {
	relay := spinRelay(t)
	alice := mkNode(t, relay, "alice", nil)
	bob := mkNode(t, relay, "bob", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if err := alice.Start(ctx); err != nil {
		t.Fatalf("alice start: %v", err)
	}
	if err := bob.Start(ctx); err != nil {
		t.Fatalf("bob start: %v", err)
	}

	code, pairCh, err := alice.Pair(ctx)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if _, err := bob.Consume(ctx, code.Mnemonic()); err != nil {
		t.Fatalf("consume: %v", err)
	}
	res := <-pairCh
	if res.Err != nil {
		t.Fatalf("alice pair result: %v", res.Err)
	}

	thread, err := alice.OpenThread(ctx, bob.Identity(), "pending-ask")
	if err != nil {
		t.Fatalf("open thread: %v", err)
	}

	if err := alice.Send(ctx, thread, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: "bootstrap"}); err != nil {
		t.Fatalf("send bootstrap: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		envs, err := bob.ListEnvelopes(ctx, thread, 0)
		if err == nil && len(envs) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if bob.HasPendingAsk(thread) {
		t.Fatal("expected no pending ask before ask_human")
	}

	if err := alice.Send(ctx, thread, envelope.IntentAskHuman, envelope.Content{Kind: envelope.ContentText, Text: "question?"}); err != nil {
		t.Fatalf("send ask_human: %v", err)
	}

	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if bob.HasPendingAsk(thread) {
			goto pendingDetected
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected pending ask after inbound ask_human")

pendingDetected:
	if err := bob.SubmitHumanReply(ctx, thread, envelope.Content{Kind: envelope.ContentText, Text: "reply"}); err != nil {
		t.Fatalf("submit human reply: %v", err)
	}

	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if !bob.HasPendingAsk(thread) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if bob.HasPendingAsk(thread) {
		t.Fatal("expected pending ask to clear after SubmitHumanReply")
	}

	if err := bob.Send(ctx, thread, envelope.IntentAskHuman, envelope.Content{Kind: envelope.ContentText, Text: "my own ask"}); err != nil {
		t.Fatalf("send local ask_human: %v", err)
	}
	if bob.HasPendingAsk(thread) {
		t.Fatal("expected local ask_human not to count as pending inbound ask")
	}
}
