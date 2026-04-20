package node_test

import (
	"context"
	"crypto/rand"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/session"
	"github.com/vMaroon/ClawdChan/core/store"
	"github.com/vMaroon/ClawdChan/core/surface"
	"github.com/vMaroon/ClawdChan/core/transport"
	"github.com/vMaroon/ClawdChan/internal/relayserver"
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

type captureAgent struct {
	mu       sync.Mutex
	messages []envelope.Envelope
}

func (c *captureAgent) OnMessage(_ context.Context, env envelope.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, env)
	return nil
}

func spinRelay(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(relayserver.New(relayserver.Config{PairRendezvousTTL: 5 * time.Second}).Handler())
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func mkNode(t *testing.T, relay, alias string, h surface.HumanSurface) *node.Node {
	t.Helper()
	return mkNodeWithSurfaces(t, relay, alias, h, nil)
}

func mkNodeWithSurfaces(t *testing.T, relay, alias string, h surface.HumanSurface, a surface.AgentSurface) *node.Node {
	t.Helper()
	n, err := node.New(node.Config{
		DataDir:  t.TempDir(),
		RelayURL: relay,
		Alias:    alias,
		Human:    h,
		Agent:    a,
	})
	if err != nil {
		t.Fatalf("new %s: %v", alias, err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func pairNodes(t *testing.T, ctx context.Context, initiator, responder *node.Node) {
	t.Helper()
	code, pairCh, err := initiator.Pair(ctx)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	peer, err := responder.Consume(ctx, code.Mnemonic())
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if peer.NodeID != initiator.Identity() {
		t.Fatalf("wrong peer id: got %x want %x", peer.NodeID, initiator.Identity())
	}
	res := <-pairCh
	if res.Err != nil {
		t.Fatalf("pair result: %v", res.Err)
	}
	if res.Peer.NodeID != responder.Identity() {
		t.Fatalf("wrong pair result id: got %x want %x", res.Peer.NodeID, responder.Identity())
	}
}

func waitForStatus(t *testing.T, n *node.Node, thread envelope.ThreadID, envID envelope.ULID, want store.DeliveryStatus, timeout time.Duration) store.EnvelopeRecord {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		recs, err := n.ListEnvelopeRecords(context.Background(), thread, 0)
		if err == nil {
			for _, rec := range recs {
				if rec.Envelope.EnvelopeID == envID && rec.Status == want {
					return rec
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("status %v not reached for envelope %x", want, envID)
	return store.EnvelopeRecord{}
}

func getRecordByID(t *testing.T, n *node.Node, thread envelope.ThreadID, envID envelope.ULID) store.EnvelopeRecord {
	t.Helper()
	recs, err := n.ListEnvelopeRecords(context.Background(), thread, 0)
	if err != nil {
		t.Fatalf("list envelope records: %v", err)
	}
	for _, rec := range recs {
		if rec.Envelope.EnvelopeID == envID {
			return rec
		}
	}
	t.Fatalf("missing envelope record %x", envID)
	return store.EnvelopeRecord{}
}

func randomULID(t *testing.T) envelope.ULID {
	t.Helper()
	var id envelope.ULID
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatalf("random ULID: %v", err)
	}
	return id
}

func sendForgedAck(t *testing.T, relay string, from *node.Node, to identity.NodeID, thread envelope.ThreadID, parent envelope.ULID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	senderID, err := from.Store().LoadIdentity(ctx)
	if err != nil {
		t.Fatalf("load sender identity: %v", err)
	}
	peer, err := from.GetPeer(ctx, to)
	if err != nil {
		t.Fatalf("load sender peer: %v", err)
	}
	sess, err := session.New(senderID, peer.KexPub)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	ack := envelope.Envelope{
		Version:    envelope.Version,
		EnvelopeID: randomULID(t),
		ThreadID:   thread,
		ParentID:   parent,
		From: envelope.Principal{
			NodeID: from.Identity(),
			Role:   envelope.RoleAgent,
			Alias:  from.Alias(),
		},
		Intent:      envelope.IntentAck,
		CreatedAtMs: time.Now().UnixMilli(),
		Content: envelope.Content{
			Kind: envelope.ContentText,
		},
	}
	if err := envelope.Sign(&ack, senderID); err != nil {
		t.Fatalf("sign ack: %v", err)
	}
	blob, err := envelope.Marshal(ack)
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}
	frame, err := sess.Seal(blob)
	if err != nil {
		t.Fatalf("seal ack frame: %v", err)
	}
	link, err := transport.NewWS(relay).Connect(ctx, senderID)
	if err != nil {
		t.Fatalf("connect forged sender: %v", err)
	}
	defer link.Close()
	if err := link.Send(ctx, to, frame); err != nil {
		t.Fatalf("send forged ack: %v", err)
	}
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

func TestAckRoundtrip(t *testing.T) {
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
	pairNodes(t, ctx, alice, bob)

	thread, err := alice.OpenThread(ctx, bob.Identity(), "ack-roundtrip")
	if err != nil {
		t.Fatalf("open thread: %v", err)
	}
	if err := alice.Send(ctx, thread, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: "ack me"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	aliceRecs, err := alice.ListEnvelopeRecords(ctx, thread, 0)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(aliceRecs) != 1 {
		t.Fatalf("expected one outbound record, got %d", len(aliceRecs))
	}
	envID := aliceRecs[0].Envelope.EnvelopeID

	delivered := waitForStatus(t, alice, thread, envID, store.StatusDelivered, 4*time.Second)
	if delivered.SentAtMs == 0 || delivered.DeliveredAtMs == 0 {
		t.Fatalf("expected sent/delivered timestamps, got sent=%d delivered=%d", delivered.SentAtMs, delivered.DeliveredAtMs)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		bobRecs, err := bob.ListEnvelopeRecords(ctx, thread, 0)
		if err == nil && len(bobRecs) >= 1 {
			if len(bobRecs) != 1 {
				t.Fatalf("expected one inbound record on bob, got %d", len(bobRecs))
			}
			if bobRecs[0].Envelope.Intent == envelope.IntentAck {
				t.Fatal("ack should not be stored as a thread envelope")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("bob never received inbound envelope")
}

func TestAckDoesNotRoute(t *testing.T) {
	relay := spinRelay(t)
	aliceHuman := &captureHuman{}
	aliceAgent := &captureAgent{}
	alice := mkNodeWithSurfaces(t, relay, "alice", aliceHuman, aliceAgent)
	bob := mkNode(t, relay, "bob", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if err := alice.Start(ctx); err != nil {
		t.Fatalf("alice start: %v", err)
	}
	if err := bob.Start(ctx); err != nil {
		t.Fatalf("bob start: %v", err)
	}
	pairNodes(t, ctx, alice, bob)

	thread, err := alice.OpenThread(ctx, bob.Identity(), "ack-not-route")
	if err != nil {
		t.Fatalf("open thread: %v", err)
	}
	if err := alice.Send(ctx, thread, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: "route?"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	recs, err := alice.ListEnvelopeRecords(ctx, thread, 0)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected one outbound record, got %d", len(recs))
	}
	waitForStatus(t, alice, thread, recs[0].Envelope.EnvelopeID, store.StatusDelivered, 4*time.Second)

	aliceHuman.mu.Lock()
	notified := len(aliceHuman.notified)
	aliceHuman.mu.Unlock()
	if notified != 0 {
		t.Fatalf("ack should not notify human, got %d notifications", notified)
	}
	aliceAgent.mu.Lock()
	msgs := len(aliceAgent.messages)
	aliceAgent.mu.Unlock()
	if msgs != 0 {
		t.Fatalf("ack should not route to agent, got %d messages", msgs)
	}
}

func TestAckRejectedFromWrongPeer(t *testing.T) {
	relay := spinRelay(t)
	alice := mkNode(t, relay, "alice", nil)
	bob := mkNode(t, relay, "bob", nil)
	charlie := mkNode(t, relay, "charlie", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if err := alice.Start(ctx); err != nil {
		t.Fatalf("alice start: %v", err)
	}
	pairNodes(t, ctx, alice, bob)
	pairNodes(t, ctx, alice, charlie)

	thread, err := alice.OpenThread(ctx, bob.Identity(), "ack-wrong-peer")
	if err != nil {
		t.Fatalf("open thread: %v", err)
	}
	if err := alice.Send(ctx, thread, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: "bob-only"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	recs, err := alice.ListEnvelopeRecords(ctx, thread, 0)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected one outbound record, got %d", len(recs))
	}
	envID := recs[0].Envelope.EnvelopeID
	waitForStatus(t, alice, thread, envID, store.StatusSent, 2*time.Second)

	sendForgedAck(t, relay, charlie, alice.Identity(), thread, envID)

	time.Sleep(300 * time.Millisecond)
	rec := getRecordByID(t, alice, thread, envID)
	if rec.Status != store.StatusSent {
		t.Fatalf("status changed after spoofed ack: got %v want %v", rec.Status, store.StatusSent)
	}
	if rec.DeliveredAtMs != 0 {
		t.Fatalf("expected no delivered timestamp, got %d", rec.DeliveredAtMs)
	}
}

func TestAckRejectedForUnknownEnvelope(t *testing.T) {
	relay := spinRelay(t)
	alice := mkNode(t, relay, "alice", nil)
	bob := mkNode(t, relay, "bob", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if err := alice.Start(ctx); err != nil {
		t.Fatalf("alice start: %v", err)
	}
	pairNodes(t, ctx, alice, bob)

	thread, err := alice.OpenThread(ctx, bob.Identity(), "ack-unknown")
	if err != nil {
		t.Fatalf("open thread: %v", err)
	}
	if err := alice.Send(ctx, thread, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: "known message"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	recs, err := alice.ListEnvelopeRecords(ctx, thread, 0)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected one outbound record, got %d", len(recs))
	}
	envID := recs[0].Envelope.EnvelopeID
	waitForStatus(t, alice, thread, envID, store.StatusSent, 2*time.Second)

	sendForgedAck(t, relay, bob, alice.Identity(), thread, randomULID(t))

	time.Sleep(300 * time.Millisecond)
	rec := getRecordByID(t, alice, thread, envID)
	if rec.Status != store.StatusSent {
		t.Fatalf("status changed after unknown-envelope ack: got %v want %v", rec.Status, store.StatusSent)
	}
	if rec.DeliveredAtMs != 0 {
		t.Fatalf("expected no delivered timestamp, got %d", rec.DeliveredAtMs)
	}
}
