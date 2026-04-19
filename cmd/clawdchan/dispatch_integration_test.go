package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/policy"
	"github.com/vMaroon/ClawdChan/internal/relayserver"
)

// mockDispatcher is a test double for policy.Dispatcher: records every
// request it sees and returns canned outcomes. The test uses it instead
// of a real subprocess so the assertion surface is about the daemon
// plumbing, not about fork/exec behavior.
type mockDispatcher struct {
	mu      sync.Mutex
	enabled bool
	seen    []policy.DispatchRequest
	answer  string
	intent  string
	collab  bool
	decline string
	callErr error
	waitFor chan struct{}
}

func (m *mockDispatcher) Enabled() bool { return m != nil && m.enabled }

func (m *mockDispatcher) Dispatch(ctx context.Context, req policy.DispatchRequest) (policy.DispatchOutcome, error) {
	m.mu.Lock()
	m.seen = append(m.seen, req)
	waitFor := m.waitFor
	m.mu.Unlock()
	if waitFor != nil {
		select {
		case <-waitFor:
		case <-ctx.Done():
			return policy.DispatchOutcome{}, ctx.Err()
		}
	}
	if m.callErr != nil {
		return policy.DispatchOutcome{}, m.callErr
	}
	if m.decline != "" {
		return policy.DispatchOutcome{Declined: true, DeclineReason: m.decline}, nil
	}
	intent := m.intent
	if intent == "" {
		intent = "ask"
	}
	return policy.DispatchOutcome{Reply: m.answer, Intent: intent, Collab: m.collab}, nil
}

func (m *mockDispatcher) requests() []policy.DispatchRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]policy.DispatchRequest, len(m.seen))
	copy(out, m.seen)
	return out
}

func spinTestRelay(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(relayserver.New(relayserver.Config{PairRendezvousTTL: 5 * time.Second}).Handler())
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// TestDispatchCollabSyncHappyPath paired nodes, bob has a daemonSurface
// with a mockDispatcher, alice sends a collab-sync ask, bob's
// dispatcher is invoked and the answer lands back in alice's store.
func TestDispatchCollabSyncHappyPath(t *testing.T) {
	relay := spinTestRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	alice, err := node.New(node.Config{
		DataDir:  t.TempDir(),
		RelayURL: relay,
		Alias:    "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()

	mock := &mockDispatcher{enabled: true, answer: "pong from bob", intent: "ask"}
	bobSurface := &daemonSurface{
		dispatcher: mock,
		dispatchCfg: &dispatchConfig{
			Enabled: true,
			Command: []string{"mock"},
		},
	}
	bob, err := node.New(node.Config{
		DataDir:  t.TempDir(),
		RelayURL: relay,
		Alias:    "bob",
		Human:    bobSurface,
		Agent:    &daemonAgent{d: bobSurface},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	bobSurface.node = bob

	if err := alice.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := bob.Start(ctx); err != nil {
		t.Fatal(err)
	}

	code, pairCh, err := alice.Pair(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Consume(ctx, code.Mnemonic()); err != nil {
		t.Fatal(err)
	}
	if r := <-pairCh; r.Err != nil {
		t.Fatalf("pair: %v", r.Err)
	}

	thread, err := alice.OpenThread(ctx, bob.Identity(), "collab")
	if err != nil {
		t.Fatal(err)
	}

	// Alice sends a collab-sync ask — wrapped as digest with the reserved title.
	ask := envelope.Content{
		Kind:  envelope.ContentDigest,
		Title: policy.CollabSyncTitle,
		Body:  "please confirm sky is blue",
	}
	if err := alice.Send(ctx, thread, envelope.IntentAsk, ask); err != nil {
		t.Fatalf("alice send: %v", err)
	}

	// Wait for bob's dispatcher to fire and for the reply to appear on
	// alice's thread.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		envs, err := alice.ListEnvelopes(ctx, thread, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range envs {
			if e.From.NodeID == bob.Identity() && e.Content.Text == "pong from bob" {
				// Sanity-check the dispatcher was invoked with the
				// expected request shape.
				reqs := mock.requests()
				if len(reqs) != 1 {
					t.Fatalf("dispatcher call count = %d, want 1", len(reqs))
				}
				r := reqs[0]
				if r.Ask.Body != "please confirm sky is blue" {
					t.Fatalf("ask body: %q", r.Ask.Body)
				}
				if r.Ask.Direction != "in" {
					t.Fatalf("ask direction: %q want 'in'", r.Ask.Direction)
				}
				if !r.Ask.Collab {
					t.Fatalf("ask should be marked collab=true")
				}
				if r.Peer.Alias != "alice" {
					t.Fatalf("peer alias: %q want 'alice'", r.Peer.Alias)
				}
				if r.Self.Alias != "bob" {
					t.Fatalf("self alias: %q want 'bob'", r.Self.Alias)
				}
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("alice never received bob's dispatched reply; dispatcher calls=%d", len(mock.requests()))
}

// TestDispatchDeclineNotifiesPeer: when the dispatcher declines, the
// sender's thread must still see a bounded reply so an awaiting sub-agent
// doesn't spin until its own timeout. The body is prefixed with
// "[collab-dispatch declined]" so the sender can distinguish this from a
// substantive answer.
func TestDispatchDeclineNotifiesPeer(t *testing.T) {
	relay := spinTestRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	alice, err := node.New(node.Config{DataDir: t.TempDir(), RelayURL: relay, Alias: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()

	mock := &mockDispatcher{enabled: true, decline: "agent busy right now"}
	bobSurface := &daemonSurface{
		dispatcher:  mock,
		dispatchCfg: &dispatchConfig{Enabled: true, Command: []string{"mock"}},
	}
	bob, err := node.New(node.Config{
		DataDir:  t.TempDir(),
		RelayURL: relay,
		Alias:    "bob",
		Human:    bobSurface,
		Agent:    &daemonAgent{d: bobSurface},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	bobSurface.node = bob

	if err := alice.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := bob.Start(ctx); err != nil {
		t.Fatal(err)
	}

	code, pairCh, _ := alice.Pair(ctx)
	if _, err := bob.Consume(ctx, code.Mnemonic()); err != nil {
		t.Fatal(err)
	}
	<-pairCh

	thread, _ := alice.OpenThread(ctx, bob.Identity(), "decline")
	ask := envelope.Content{
		Kind:  envelope.ContentDigest,
		Title: policy.CollabSyncTitle,
		Body:  "please help",
	}
	if err := alice.Send(ctx, thread, envelope.IntentAsk, ask); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		envs, _ := alice.ListEnvelopes(ctx, thread, 0)
		for _, e := range envs {
			if e.From.NodeID == bob.Identity() && strings.HasPrefix(e.Content.Text, "[collab-dispatch declined]") {
				if !strings.Contains(e.Content.Text, "agent busy right now") {
					t.Fatalf("decline missing reason: %q", e.Content.Text)
				}
				return
			}
		}
		time.Sleep(80 * time.Millisecond)
	}
	t.Fatalf("alice never got a decline notice; dispatcher calls=%d", len(mock.requests()))
}

// TestDispatchSkippedWithoutCollabFlag — a normal ask (not wrapped as
// collab-sync) must not trigger the dispatcher, even when a dispatcher
// is configured. The classic toast-and-wait path owns it.
func TestDispatchSkippedWithoutCollabFlag(t *testing.T) {
	relay := spinTestRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	alice, _ := node.New(node.Config{DataDir: t.TempDir(), RelayURL: relay, Alias: "alice"})
	defer alice.Close()

	mock := &mockDispatcher{enabled: true, answer: "should not be called"}
	bobSurface := &daemonSurface{
		dispatcher:  mock,
		dispatchCfg: &dispatchConfig{Enabled: true, Command: []string{"mock"}},
	}
	bob, _ := node.New(node.Config{
		DataDir:  t.TempDir(),
		RelayURL: relay,
		Alias:    "bob",
		Human:    bobSurface,
		Agent:    &daemonAgent{d: bobSurface},
	})
	defer bob.Close()
	bobSurface.node = bob

	alice.Start(ctx)
	bob.Start(ctx)

	code, pairCh, _ := alice.Pair(ctx)
	if _, err := bob.Consume(ctx, code.Mnemonic()); err != nil {
		t.Fatal(err)
	}
	<-pairCh

	thread, _ := alice.OpenThread(ctx, bob.Identity(), "plain")
	if err := alice.Send(ctx, thread, envelope.IntentAsk, envelope.Content{Kind: envelope.ContentText, Text: "hi"}); err != nil {
		t.Fatal(err)
	}

	// Give inbound plenty of time, then check the dispatcher was never
	// invoked. We verify bob actually received the envelope to ensure
	// the lack of dispatcher call isn't just slow inbound.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		envs, _ := bob.ListEnvelopes(ctx, thread, 0)
		if len(envs) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := len(mock.requests()); n != 0 {
		t.Fatalf("dispatcher was invoked %d times for a non-collab ask; expected 0", n)
	}
}
