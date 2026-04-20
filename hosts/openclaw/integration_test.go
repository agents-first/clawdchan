package openclaw

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/store"
)

type capturedSend struct {
	sid  string
	text string
}

func TestIntegrationTwoOpenClawHostsRoundTripAndAskHuman(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	relay := spinRelay(t)
	alice := mkTestNode(t, relay, "alice")
	bob := mkTestNode(t, relay, "bob")
	if err := alice.Start(ctx); err != nil {
		t.Fatalf("alice start: %v", err)
	}
	if err := bob.Start(ctx); err != nil {
		t.Fatalf("bob start: %v", err)
	}
	pairNodes(t, ctx, alice, bob)

	gwA, sendsA := newCapturingGateway(t, "token-a")
	gwB, sendsB := newCapturingGateway(t, "token-b")

	bridgeA := NewBridge(gwA.wsURL, "token-a", "device-a", nil)
	bridgeB := NewBridge(gwB.wsURL, "token-b", "device-b", nil)
	t.Cleanup(func() { _ = bridgeA.Close() })
	t.Cleanup(func() { _ = bridgeB.Close() })
	if err := bridgeA.Connect(ctx); err != nil {
		t.Fatalf("bridgeA connect: %v", err)
	}
	if err := bridgeB.Connect(ctx); err != nil {
		t.Fatalf("bridgeB connect: %v", err)
	}

	storeA, err := store.Open(filepath.Join(alice.DataDir(), "clawdchan.db"))
	if err != nil {
		t.Fatalf("open alice store: %v", err)
	}
	storeB, err := store.Open(filepath.Join(bob.DataDir(), "clawdchan.db"))
	if err != nil {
		_ = storeA.Close()
		t.Fatalf("open bob store: %v", err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	t.Cleanup(func() { _ = storeB.Close() })

	smA := NewSessionMap(bridgeA, storeA)
	smB := NewSessionMap(bridgeB, storeB)
	alice.SetHumanSurface(NewHumanSurface(smA, bridgeA))
	alice.SetAgentSurface(NewAgentSurface(smA, bridgeA))
	bob.SetHumanSurface(NewHumanSurface(smB, bridgeB))
	bob.SetAgentSurface(NewAgentSurface(smB, bridgeB))

	sidAB, err := smA.EnsureSessionFor(ctx, bob.Identity())
	if err != nil {
		t.Fatalf("alice ensure session for bob: %v", err)
	}
	sidBA, err := smB.EnsureSessionFor(ctx, alice.Identity())
	if err != nil {
		t.Fatalf("bob ensure session for alice: %v", err)
	}

	thread, err := alice.OpenThread(ctx, bob.Identity(), "openclaw-integration")
	if err != nil {
		t.Fatalf("open thread: %v", err)
	}
	if err := alice.Send(ctx, thread, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: "bootstrap"}); err != nil {
		t.Fatalf("bootstrap send: %v", err)
	}
	waitUntil(t, 4*time.Second, func() bool {
		envs, err := bob.ListEnvelopes(ctx, thread, 0)
		return err == nil && len(envs) > 0
	}, "bob did not receive bootstrap message")

	subCtxA, cancelSubA := context.WithCancel(ctx)
	subCtxB, cancelSubB := context.WithCancel(ctx)
	t.Cleanup(cancelSubA)
	t.Cleanup(cancelSubB)
	go bridgeA.RunSubscriber(subCtxA, sidAB, alice, thread)
	go bridgeB.RunSubscriber(subCtxB, sidBA, bob, thread)
	waitUntil(t, 3*time.Second, func() bool {
		return gwA.hasSubscription(sidAB) && gwB.hasSubscription(sidBA)
	}, "subscribers did not subscribe to both sessions")

	step2Body := "assistant A -> B"
	gwA.emitMessage(sidAB, "msg-a-1", "assistant", step2Body)
	waitForSessionsSend(t, sendsB, 4*time.Second, func(s capturedSend) bool {
		return s.sid == sidBA &&
			strings.Contains(s.text, "[clawdchan · from alice · say]") &&
			strings.Contains(s.text, step2Body)
	}, "bob gateway did not receive alice->bob assistant relay payload")

	step4Body := "assistant B -> A"
	gwB.emitMessage(sidBA, "msg-b-1", "assistant", step4Body)
	waitForSessionsSend(t, sendsA, 4*time.Second, func(s capturedSend) bool {
		return s.sid == sidAB &&
			strings.Contains(s.text, "[clawdchan · from bob · say]") &&
			strings.Contains(s.text, step4Body)
	}, "alice gateway did not receive bob->alice assistant relay payload")

	askBody := "need human confirmation"
	if err := alice.Send(ctx, thread, envelope.IntentAskHuman, envelope.Content{Kind: envelope.ContentText, Text: askBody}); err != nil {
		t.Fatalf("alice send ask_human: %v", err)
	}
	waitUntil(t, 4*time.Second, func() bool {
		return bob.HasPendingAsk(thread)
	}, "bob did not record pending ask_human")
	waitForSessionsSend(t, sendsB, 4*time.Second, func(s capturedSend) bool {
		return s.sid == sidBA &&
			strings.Contains(s.text, "[clawdchan · from alice · ask_human]") &&
			strings.Contains(s.text, askBody)
	}, "bob gateway did not receive ask_human payload")

	replyBody := "approved by human"
	gwB.emitMessage(sidBA, "msg-b-ask-reply", "assistant", replyBody)
	waitUntil(t, 4*time.Second, func() bool {
		return !bob.HasPendingAsk(thread)
	}, "pending ask_human did not clear after assistant reply")
	waitUntil(t, 4*time.Second, func() bool {
		envs, err := alice.ListEnvelopes(ctx, thread, 0)
		if err != nil {
			return false
		}
		for _, env := range envs {
			if env.Content.Text == replyBody && env.From.Role == envelope.RoleHuman {
				return true
			}
		}
		return false
	}, "alice did not receive reply as role=human envelope")
	waitForSessionsSend(t, sendsA, 4*time.Second, func(s capturedSend) bool {
		return s.sid == sidAB &&
			strings.Contains(s.text, "[clawdchan · from bob · say]") &&
			strings.Contains(s.text, replyBody)
	}, "alice gateway did not receive rendered human reply")
}

func newCapturingGateway(t *testing.T, token string) (*fakeGateway, <-chan capturedSend) {
	t.Helper()
	sends := make(chan capturedSend, 128)
	gw := newFakeGateway(t, token, func(_ *fakeGatewayClient, req gatewayMessage) bool {
		if req.Method != "sessions.send" {
			return false
		}
		var params struct {
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
		}
		if err := json.Unmarshal(req.Params, &params); err == nil {
			sends <- capturedSend{sid: params.SessionID, text: params.Text}
		}
		return false
	})
	return gw, sends
}

func waitForSessionsSend(t *testing.T, sends <-chan capturedSend, timeout time.Duration, match func(capturedSend) bool, failMsg string) capturedSend {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case sent := <-sends:
			if match(sent) {
				return sent
			}
		case <-timer.C:
			t.Fatal(failMsg)
		}
	}
}

func pairNodes(t *testing.T, ctx context.Context, alice, bob *node.Node) {
	t.Helper()
	code, pairCh, err := alice.Pair(ctx)
	if err != nil {
		t.Fatalf("alice pair: %v", err)
	}
	peer, err := bob.Consume(ctx, code.Mnemonic())
	if err != nil {
		t.Fatalf("bob consume: %v", err)
	}
	if peer.NodeID != alice.Identity() {
		t.Fatalf("bob sees wrong peer id: got=%x want=%x", peer.NodeID, alice.Identity())
	}
	res := <-pairCh
	if res.Err != nil {
		t.Fatalf("alice pair result: %v", res.Err)
	}
	if res.Peer.NodeID != bob.Identity() {
		t.Fatalf("alice sees wrong peer id: got=%x want=%x", res.Peer.NodeID, bob.Identity())
	}
}
