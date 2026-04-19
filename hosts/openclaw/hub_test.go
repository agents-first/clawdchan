package openclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
)

var mnemonicPattern = regexp.MustCompile(`[a-z]+(?: [a-z]+){11}`)

type hubGatewayCapture struct {
	creates chan string
	sends   chan capturedSend
}

func newHubGateway(t *testing.T, token string) (*fakeGateway, *hubGatewayCapture) {
	t.Helper()
	cap := &hubGatewayCapture{
		creates: make(chan string, 64),
		sends:   make(chan capturedSend, 128),
	}
	gw := newFakeGateway(t, token, func(_ *fakeGatewayClient, req gatewayMessage) bool {
		switch req.Method {
		case "sessions.create":
			var params struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(req.Params, &params); err == nil && params.Name != "" {
				cap.creates <- params.Name
			}
		case "sessions.send":
			var params struct {
				SessionID string `json:"session_id"`
				Text      string `json:"text"`
			}
			if err := json.Unmarshal(req.Params, &params); err == nil {
				cap.sends <- capturedSend{sid: params.SessionID, text: params.Text}
			}
		}
		return false
	})
	return gw, cap
}

func startHubForTest(t *testing.T, parent context.Context, h *Hub, gw *fakeGateway) (context.CancelFunc, <-chan error, string) {
	t.Helper()
	runCtx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	go func() {
		done <- h.Start(runCtx)
	}()

	var sid string
	waitUntil(t, 3*time.Second, func() bool {
		sid = h.HubSessionID()
		return sid != ""
	}, "hub did not create session")
	waitUntil(t, 3*time.Second, func() bool {
		return gw.hasSubscription(sid)
	}, "hub did not subscribe to hub session")
	return cancel, done, sid
}

func stopHubForTest(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("hub returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hub did not stop after cancel")
	}
}

func extractMnemonic(text string) string {
	return mnemonicPattern.FindString(strings.ToLower(text))
}

func TestHubStartCreatesSessionAndInjectsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	relay := spinRelay(t)
	n := mkTestNode(t, relay, "alice")

	gw, cap := newHubGateway(t, "token")
	br := NewBridge(gw.wsURL, "token", "device-hub-start", nil)
	t.Cleanup(func() { _ = br.Close() })
	if err := br.Connect(ctx); err != nil {
		t.Fatalf("bridge connect: %v", err)
	}

	hub := NewHub(n, br, NewSessionMap(br, n.Store()))
	stop, done, sid := startHubForTest(t, ctx, hub, gw)
	defer stopHubForTest(t, stop, done)

	select {
	case name := <-cap.creates:
		if name != hubSessionName {
			t.Fatalf("sessions.create name = %q, want %q", name, hubSessionName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sessions.create")
	}

	sent := waitForSessionsSend(t, cap.sends, 3*time.Second, func(s capturedSend) bool {
		return s.sid == sid && strings.Contains(s.text, "ClawdChan agent")
	}, "hub context was not sent")
	if !strings.Contains(sent.text, "alice's ClawdChan agent") {
		t.Fatalf("hub context missing alias:\n%s", sent.text)
	}
	if !strings.Contains(sent.text, `{"cc":"pair"}`) {
		t.Fatalf("hub context missing action guide:\n%s", sent.text)
	}
}

func TestHubPairActionSendsMnemonicAndCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	relay := spinRelay(t)
	alice := mkTestNode(t, relay, "alice")
	bob := mkTestNode(t, relay, "bob")

	gw, cap := newHubGateway(t, "token")
	br := NewBridge(gw.wsURL, "token", "device-hub-pair", nil)
	t.Cleanup(func() { _ = br.Close() })
	if err := br.Connect(ctx); err != nil {
		t.Fatalf("bridge connect: %v", err)
	}

	hub := NewHub(alice, br, NewSessionMap(br, alice.Store()))
	stop, done, sid := startHubForTest(t, ctx, hub, gw)
	defer stopHubForTest(t, stop, done)

	_ = waitForSessionsSend(t, cap.sends, 3*time.Second, func(s capturedSend) bool {
		return s.sid == sid && strings.Contains(s.text, "ClawdChan agent")
	}, "hub context was not sent")

	gw.emitMessage(sid, "pair-action-1", "assistant", "I'll pair you now.\n{\"cc\":\"pair\"}")
	pairReply := waitForSessionsSend(t, cap.sends, 4*time.Second, func(s capturedSend) bool {
		return s.sid == sid && strings.Contains(s.text, "Here's your pairing code")
	}, "pair action response was not sent")

	mnemonic := extractMnemonic(pairReply.text)
	if mnemonic == "" {
		t.Fatalf("pair response missing mnemonic:\n%s", pairReply.text)
	}

	peer, err := bob.Consume(ctx, mnemonic)
	if err != nil {
		t.Fatalf("bob consume mnemonic: %v", err)
	}
	if peer.NodeID != alice.Identity() {
		t.Fatalf("bob consume returned wrong peer id")
	}

	completion := waitForSessionsSend(t, cap.sends, 6*time.Second, func(s capturedSend) bool {
		return s.sid == sid && strings.Contains(s.text, "Paired with") && strings.Contains(s.text, "SAS:")
	}, "pair completion response was not sent")
	if !strings.Contains(completion.text, "Paired with bob") {
		t.Fatalf("pair completion missing peer alias:\n%s", completion.text)
	}
}

func TestHubConsumeActionCreatesPeerSessionAndReplies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	relay := spinRelay(t)
	alice := mkTestNode(t, relay, "alice")
	bob := mkTestNode(t, relay, "bob")

	code, pairCh, err := alice.Pair(ctx)
	if err != nil {
		t.Fatalf("alice pair: %v", err)
	}

	gw, cap := newHubGateway(t, "token")
	br := NewBridge(gw.wsURL, "token", "device-hub-consume", nil)
	t.Cleanup(func() { _ = br.Close() })
	if err := br.Connect(ctx); err != nil {
		t.Fatalf("bridge connect: %v", err)
	}

	hub := NewHub(bob, br, NewSessionMap(br, bob.Store()))
	stop, done, sid := startHubForTest(t, ctx, hub, gw)
	defer stopHubForTest(t, stop, done)

	_ = waitForSessionsSend(t, cap.sends, 3*time.Second, func(s capturedSend) bool {
		return s.sid == sid && strings.Contains(s.text, "ClawdChan agent")
	}, "hub context was not sent")

	gw.emitMessage(sid, "consume-action-1", "assistant", fmt.Sprintf("{\"cc\":\"consume\",\"words\":\"%s\"}", code.Mnemonic()))
	consumeReply := waitForSessionsSend(t, cap.sends, 6*time.Second, func(s capturedSend) bool {
		return s.sid == sid && strings.Contains(s.text, "Paired with")
	}, "consume action response was not sent")
	if !strings.Contains(consumeReply.text, "Paired with alice") {
		t.Fatalf("consume response missing alias:\n%s", consumeReply.text)
	}

	select {
	case res := <-pairCh:
		if res.Err != nil {
			t.Fatalf("alice pair completion error: %v", res.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for alice pair completion")
	}

	sid2, ok, err := bob.Store().GetOpenClawSession(ctx, alice.Identity())
	if err != nil {
		t.Fatalf("get openclaw session mapping: %v", err)
	}
	if !ok || sid2 == "" {
		t.Fatal("consume action did not persist peer session mapping")
	}
}

func TestHubPeersActionWithZeroAndTwoPeers(t *testing.T) {
	t.Run("no peers", func(t *testing.T) {
		relay := spinRelay(t)
		n := mkTestNode(t, relay, "alice")
		h := NewHub(n, nil, nil)
		got := h.executeAction(context.Background(), Action{Kind: ActionPeers})
		if got != "No paired peers yet." {
			t.Fatalf("unexpected peers response: %q", got)
		}
	})

	t.Run("two peers", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		relay := spinRelay(t)
		alice := mkTestNode(t, relay, "alice")
		bob := mkTestNode(t, relay, "bob")
		carol := mkTestNode(t, relay, "carol")

		code1, ch1, err := alice.Pair(ctx)
		if err != nil {
			t.Fatalf("pair with bob: %v", err)
		}
		if _, err := bob.Consume(ctx, code1.Mnemonic()); err != nil {
			t.Fatalf("consume by bob: %v", err)
		}
		if res := <-ch1; res.Err != nil {
			t.Fatalf("pair completion with bob: %v", res.Err)
		}

		code2, ch2, err := alice.Pair(ctx)
		if err != nil {
			t.Fatalf("pair with carol: %v", err)
		}
		if _, err := carol.Consume(ctx, code2.Mnemonic()); err != nil {
			t.Fatalf("consume by carol: %v", err)
		}
		if res := <-ch2; res.Err != nil {
			t.Fatalf("pair completion with carol: %v", res.Err)
		}

		h := NewHub(alice, nil, nil)
		got := h.executeAction(ctx, Action{Kind: ActionPeers})
		if !strings.Contains(got, "bob (") || !strings.Contains(got, "carol (") {
			t.Fatalf("peers response missing aliases:\n%s", got)
		}
		if !strings.Contains(got, ": paired") {
			t.Fatalf("peers response missing trust labels:\n%s", got)
		}
	})
}

func TestHubInboxActionWithNoThreadsAndWithMessages(t *testing.T) {
	t.Run("no threads", func(t *testing.T) {
		relay := spinRelay(t)
		n := mkTestNode(t, relay, "alice")
		h := NewHub(n, nil, nil)
		got := h.executeAction(context.Background(), Action{Kind: ActionInbox})
		if got != "No recent messages." {
			t.Fatalf("unexpected inbox response: %q", got)
		}
	})

	t.Run("with messages", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		relay := spinRelay(t)
		alice := mkTestNode(t, relay, "alice")
		bob := mkTestNode(t, relay, "bob")

		code, pairCh, err := alice.Pair(ctx)
		if err != nil {
			t.Fatalf("pair: %v", err)
		}
		if _, err := bob.Consume(ctx, code.Mnemonic()); err != nil {
			t.Fatalf("consume: %v", err)
		}
		if res := <-pairCh; res.Err != nil {
			t.Fatalf("pair completion: %v", res.Err)
		}

		thread, err := alice.OpenThread(ctx, bob.Identity(), "hub-inbox")
		if err != nil {
			t.Fatalf("open thread: %v", err)
		}
		if err := alice.Send(ctx, thread, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: "older message"}); err != nil {
			t.Fatalf("send older message: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
		if err := alice.Send(ctx, thread, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: "latest message snippet"}); err != nil {
			t.Fatalf("send latest message: %v", err)
		}

		h := NewHub(alice, nil, nil)
		got := h.executeAction(ctx, Action{Kind: ActionInbox})
		if !strings.Contains(got, "Recent messages (24h):") {
			t.Fatalf("inbox response missing header:\n%s", got)
		}
		if !strings.Contains(got, "bob: latest message snippet") {
			t.Fatalf("inbox response missing peer snippet:\n%s", got)
		}
	})
}

func TestHubIgnoresNonActionAssistantAndHumanTurns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	relay := spinRelay(t)
	n := mkTestNode(t, relay, "alice")

	gw, cap := newHubGateway(t, "token")
	br := NewBridge(gw.wsURL, "token", "device-hub-ignore", nil)
	t.Cleanup(func() { _ = br.Close() })
	if err := br.Connect(ctx); err != nil {
		t.Fatalf("bridge connect: %v", err)
	}

	hub := NewHub(n, br, NewSessionMap(br, n.Store()))
	stop, done, sid := startHubForTest(t, ctx, hub, gw)
	defer stopHubForTest(t, stop, done)

	_ = waitForSessionsSend(t, cap.sends, 3*time.Second, func(s capturedSend) bool {
		return s.sid == sid && strings.Contains(s.text, "ClawdChan agent")
	}, "hub context was not sent")

	gw.emitMessage(sid, "no-action-turn", "assistant", "This is a normal assistant turn without action JSON.")
	gw.emitMessage(sid, "human-turn", "human", "{\"cc\":\"peers\"}")

	select {
	case sent := <-cap.sends:
		t.Fatalf("unexpected sessions.send from ignored turns: sid=%s text=%q", sent.sid, sent.text)
	case <-time.After(500 * time.Millisecond):
	}
}
