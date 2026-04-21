//go:build integration

package openclaw

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agents-first/ClawdChan/core/envelope"
)

func TestHubIntegrationOpenClawNativePairingRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

	gwA, capA := newHubGateway(t, "token-a")
	gwB, capB := newHubGateway(t, "token-b")

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

	smA := NewSessionMap(bridgeA, alice.Store())
	smB := NewSessionMap(bridgeB, bob.Store())
	alice.SetHumanSurface(NewHumanSurface(smA, bridgeA))
	alice.SetAgentSurface(NewAgentSurface(smA, bridgeA))
	bob.SetHumanSurface(NewHumanSurface(smB, bridgeB))
	bob.SetAgentSurface(NewAgentSurface(smB, bridgeB))

	hubA := NewHub(alice, bridgeA, smA)
	hubB := NewHub(bob, bridgeB, smB)
	stopA, doneA, hubSidA := startHubForTest(t, ctx, hubA, gwA)
	stopB, doneB, hubSidB := startHubForTest(t, ctx, hubB, gwB)
	defer stopHubForTest(t, stopA, doneA)
	defer stopHubForTest(t, stopB, doneB)

	_ = waitForSessionsSend(t, capA.sends, 3*time.Second, func(s capturedSend) bool {
		return s.sid == hubSidA && strings.Contains(s.text, "ClawdChan agent")
	}, "alice hub context was not sent")
	_ = waitForSessionsSend(t, capB.sends, 3*time.Second, func(s capturedSend) bool {
		return s.sid == hubSidB && strings.Contains(s.text, "ClawdChan agent")
	}, "bob hub context was not sent")

	gwA.emitMessage(hubSidA, "hub-pair-action", "assistant", "I'll pair you now.\n{\"cc\":\"pair\"}")
	pairReply := waitForSessionsSend(t, capA.sends, 5*time.Second, func(s capturedSend) bool {
		return s.sid == hubSidA && strings.Contains(s.text, "Here's your pairing code")
	}, "alice hub did not respond to pair action")
	mnemonic := extractMnemonic(pairReply.text)
	if mnemonic == "" {
		t.Fatalf("pair action response missing mnemonic:\n%s", pairReply.text)
	}

	gwB.emitMessage(hubSidB, "hub-consume-action", "assistant", fmt.Sprintf("{\"cc\":\"consume\",\"words\":\"%s\"}", mnemonic))
	consumeReply := waitForSessionsSend(t, capB.sends, 6*time.Second, func(s capturedSend) bool {
		return s.sid == hubSidB && strings.Contains(s.text, "Paired with")
	}, "bob hub did not respond to consume action")
	if !strings.Contains(consumeReply.text, "Paired with alice") {
		t.Fatalf("consume response missing peer alias:\n%s", consumeReply.text)
	}

	aliceID := alice.Identity()
	wantPeerSessionName := "clawdchan:" + hex.EncodeToString(aliceID[:])[:8]
	_ = waitForSessionCreate(t, capB.creates, 4*time.Second, func(name string) bool {
		return name == wantPeerSessionName
	}, "bob gateway did not create peer session for alice")

	sidPeerB, ok, err := bob.Store().GetOpenClawSession(ctx, alice.Identity())
	if err != nil {
		t.Fatalf("bob get openclaw session mapping for alice: %v", err)
	}
	if !ok || sidPeerB == "" {
		t.Fatal("bob did not persist openclaw session mapping for alice")
	}

	completion := waitForSessionsSend(t, capA.sends, 8*time.Second, func(s capturedSend) bool {
		return s.sid == hubSidA &&
			strings.Contains(s.text, "Paired with bob") &&
			strings.Contains(s.text, "SAS:") &&
			strings.Contains(s.text, "✓")
	}, "alice hub did not receive pair completion")
	if !strings.Contains(completion.text, "Paired with bob") {
		t.Fatalf("pair completion message missing bob alias:\n%s", completion.text)
	}

	threadA, err := alice.OpenThread(ctx, bob.Identity(), "hub-native-pairing")
	if err != nil {
		t.Fatalf("alice open thread: %v", err)
	}
	msgFromA := "hello from alice via hub pairing integration"
	if err := alice.Send(ctx, threadA, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: msgFromA}); err != nil {
		t.Fatalf("alice send to bob: %v", err)
	}
	_ = waitForSessionsSend(t, capB.sends, 5*time.Second, func(s capturedSend) bool {
		return s.sid == sidPeerB && strings.Contains(s.text, msgFromA)
	}, "bob peer session did not receive alice message")

	var bobThread envelope.ThreadID
	waitUntil(t, 5*time.Second, func() bool {
		th, ok := findThreadForPeer(ctx, bob, alice.Identity())
		if !ok {
			return false
		}
		bobThread = th.ID
		return true
	}, "bob thread for alice was not created")

	peerSubCtx, cancelPeerSub := context.WithCancel(ctx)
	defer cancelPeerSub()
	go bridgeB.RunSubscriber(peerSubCtx, sidPeerB, bob, bobThread)
	waitUntil(t, 3*time.Second, func() bool {
		return gwB.hasSubscription(sidPeerB)
	}, "bob subscriber did not subscribe to peer session")

	replyFromB := "ack from bob peer session"
	gwB.emitMessage(sidPeerB, "peer-reply-1", "assistant", replyFromB)
	waitUntil(t, 6*time.Second, func() bool {
		envs, err := alice.ListEnvelopes(ctx, threadA, 0)
		if err != nil {
			return false
		}
		for _, env := range envs {
			if env.Content.Text == replyFromB && env.From.NodeID == bob.Identity() {
				return true
			}
		}
		return false
	}, "alice did not receive bob peer-session reply envelope")
}

func waitForSessionCreate(t *testing.T, creates <-chan string, timeout time.Duration, match func(string) bool, failMsg string) string {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case name := <-creates:
			if match(name) {
				return name
			}
		case <-timer.C:
			t.Fatal(failMsg)
		}
	}
}
