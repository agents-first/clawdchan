package openclaw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agents-first/clawdchan/core/envelope"
	"github.com/agents-first/clawdchan/core/node"
	"github.com/agents-first/clawdchan/internal/relayserver"
	"github.com/gorilla/websocket"
)

type fakeGatewayClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
	subs map[string]bool
}

type fakeGateway struct {
	t     *testing.T
	token string

	server *httptest.Server
	wsURL  string

	mu          sync.Mutex
	clients     map[*fakeGatewayClient]struct{}
	sessionSeq  uint64
	connects    int
	onRequestFn func(*fakeGatewayClient, gatewayMessage) bool
}

func newFakeGateway(t *testing.T, token string, onRequest func(*fakeGatewayClient, gatewayMessage) bool) *fakeGateway {
	t.Helper()
	g := &fakeGateway{
		t:           t,
		token:       token,
		clients:     make(map[*fakeGatewayClient]struct{}),
		onRequestFn: onRequest,
	}
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}

	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+g.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		client := &fakeGatewayClient{
			conn: conn,
			subs: make(map[string]bool),
		}
		g.mu.Lock()
		g.clients[client] = struct{}{}
		g.connects++
		g.mu.Unlock()
		defer func() {
			g.mu.Lock()
			delete(g.clients, client)
			g.mu.Unlock()
			_ = conn.Close()
		}()

		// Real OpenClaw protocol: server sends connect.challenge first.
		if err := g.write(client, gatewayMessage{
			Type:  "event",
			Event: "connect.challenge",
			Payload: mustJSON(t, map[string]any{
				"nonce": "nonce-1",
				"ts":    0,
			}),
		}); err != nil {
			return
		}

		var connectReq gatewayMessage
		if err := conn.ReadJSON(&connectReq); err != nil {
			return
		}
		if connectReq.Type != "req" || connectReq.Method != "connect" {
			_ = g.write(client, gatewayMessage{
				Type: "res",
				ID:   connectReq.ID,
				Error: &gatewayError{
					Code:    "bad_connect",
					Message: "expected connect request",
				},
			})
			return
		}
		// Real OpenClaw protocol: hello-ok comes as res, not event.
		if err := g.write(client, gatewayMessage{
			Type: "res",
			ID:   connectReq.ID,
			OK:   true,
			Payload: mustJSON(t, map[string]any{
				"type": "hello-ok",
			}),
		}); err != nil {
			return
		}

		for {
			var req gatewayMessage
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			if req.Type != "req" {
				continue
			}
			if g.onRequestFn != nil && g.onRequestFn(client, req) {
				continue
			}
			if err := g.handleDefaultRequest(client, req); err != nil {
				return
			}
		}
	}))
	g.wsURL = "ws" + strings.TrimPrefix(g.server.URL, "http")
	t.Cleanup(func() {
		g.Close()
	})
	return g
}

func (g *fakeGateway) handleDefaultRequest(client *fakeGatewayClient, req gatewayMessage) error {
	switch req.Method {
	case "sessions.create":
		sid := fmt.Sprintf("sid-%d", atomic.AddUint64(&g.sessionSeq, 1))
		return g.write(client, gatewayMessage{
			Type: "res",
			ID:   req.ID,
			Payload: mustJSON(g.t, map[string]any{
				"session_id": sid,
			}),
		})
	case "sessions.send":
		return g.write(client, gatewayMessage{
			Type: "res",
			ID:   req.ID,
			Payload: mustJSON(g.t, map[string]any{
				"ok": true,
			}),
		})
	case "sessions.messages.subscribe":
		var params struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return g.write(client, gatewayMessage{
				Type: "res",
				ID:   req.ID,
				Error: &gatewayError{
					Code:    "bad_params",
					Message: "invalid params",
				},
			})
		}
		client.mu.Lock()
		client.subs[params.SessionID] = true
		client.mu.Unlock()
		return g.write(client, gatewayMessage{
			Type: "res",
			ID:   req.ID,
			Payload: mustJSON(g.t, map[string]any{
				"ok": true,
			}),
		})
	default:
		return g.write(client, gatewayMessage{
			Type: "res",
			ID:   req.ID,
			Error: &gatewayError{
				Code:    "unknown_method",
				Message: req.Method,
			},
		})
	}
}

func (g *fakeGateway) write(client *fakeGatewayClient, msg gatewayMessage) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.conn.WriteJSON(msg)
}

func (g *fakeGateway) emitMessage(sid, id, role, text string) {
	g.mu.Lock()
	clients := make([]*fakeGatewayClient, 0, len(g.clients))
	for c := range g.clients {
		clients = append(clients, c)
	}
	g.mu.Unlock()

	for _, c := range clients {
		c.mu.Lock()
		subscribed := c.subs[sid]
		c.mu.Unlock()
		if !subscribed {
			continue
		}
		_ = g.write(c, gatewayMessage{
			Type:  "event",
			Event: "sessions.messages",
			Payload: mustJSON(g.t, map[string]any{
				"session_id": sid,
				"message": map[string]any{
					"id":   id,
					"role": role,
					"text": text,
				},
			}),
		})
	}
}

func (g *fakeGateway) hasSubscription(sid string) bool {
	g.mu.Lock()
	clients := make([]*fakeGatewayClient, 0, len(g.clients))
	for c := range g.clients {
		clients = append(clients, c)
	}
	g.mu.Unlock()

	for _, c := range clients {
		c.mu.Lock()
		subscribed := c.subs[sid]
		c.mu.Unlock()
		if subscribed {
			return true
		}
	}
	return false
}

func (g *fakeGateway) dropAll() {
	g.mu.Lock()
	clients := make([]*fakeGatewayClient, 0, len(g.clients))
	for c := range g.clients {
		clients = append(clients, c)
	}
	g.mu.Unlock()
	for _, c := range clients {
		_ = c.conn.Close()
	}
}

func (g *fakeGateway) connectionCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.connects
}

func (g *fakeGateway) Close() {
	g.dropAll()
	if g.server != nil {
		g.server.Close()
	}
}

func TestBridgeHandshakeTokenValidation(t *testing.T) {
	gw := newFakeGateway(t, "good-token", nil)

	okBridge := NewBridge(gw.wsURL, "good-token", "device-1", nil)
	t.Cleanup(func() { _ = okBridge.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := okBridge.Connect(ctx); err != nil {
		t.Fatalf("connect with valid token failed: %v", err)
	}

	badBridge := NewBridge(gw.wsURL, "wrong-token", "device-2", nil)
	ctxBad, cancelBad := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelBad()
	if err := badBridge.Connect(ctxBad); err == nil {
		t.Fatal("expected connect with bad token to fail")
	}
}

func TestBridgeSessionsSendCorrelatesResponsesByID(t *testing.T) {
	var gw *fakeGateway
	gw = newFakeGateway(t, "token", func(client *fakeGatewayClient, req gatewayMessage) bool {
		if req.Method != "sessions.send" {
			return false
		}
		var params struct {
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			_ = gw.write(client, gatewayMessage{
				Type: "res",
				ID:   req.ID,
				Error: &gatewayError{
					Code:    "bad_params",
					Message: err.Error(),
				},
			})
			return true
		}

		if params.Text == "first" {
			go func(id string) {
				time.Sleep(150 * time.Millisecond)
				_ = gw.write(client, gatewayMessage{
					Type: "res",
					ID:   id,
					Payload: mustJSON(t, map[string]any{
						"ok": true,
					}),
				})
			}(req.ID)
			return true
		}

		if params.Text == "second" {
			_ = gw.write(client, gatewayMessage{
				Type: "res",
				ID:   req.ID,
				Error: &gatewayError{
					Code:    "denied",
					Message: "second fails",
				},
			})
			return true
		}

		return false
	})

	bridge := NewBridge(gw.wsURL, "token", "device-1", nil)
	t.Cleanup(func() { _ = bridge.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := bridge.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	start := make(chan struct{})
	errFirst := make(chan error, 1)
	errSecond := make(chan error, 1)

	go func() {
		<-start
		errFirst <- bridge.SessionsSend(ctx, "sid-1", "first")
	}()
	go func() {
		<-start
		errSecond <- bridge.SessionsSend(ctx, "sid-1", "second")
	}()
	close(start)

	e1 := <-errFirst
	e2 := <-errSecond

	if e1 != nil {
		t.Fatalf("first send should succeed, got err: %v", e1)
	}
	if e2 == nil {
		t.Fatal("second send should fail")
	}
}

func TestBridgeSubscribeDeliversUntilContextCanceled(t *testing.T) {
	gw := newFakeGateway(t, "token", nil)

	bridge := NewBridge(gw.wsURL, "token", "device-1", nil)
	t.Cleanup(func() { _ = bridge.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := bridge.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	subCtx, subCancel := context.WithCancel(context.Background())
	msgs, err := bridge.Subscribe(subCtx, "sid-sub")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	gw.emitMessage("sid-sub", "m1", "assistant", "hello from gateway")

	select {
	case msg := <-msgs:
		if msg.ID != "m1" || msg.Role != "assistant" || msg.Text != "hello from gateway" {
			t.Fatalf("unexpected message: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscribed message")
	}

	subCancel()

	select {
	case _, ok := <-msgs:
		if ok {
			t.Fatal("expected subscription channel to close after ctx cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscription channel did not close after ctx cancel")
	}
}

func TestBridgeReconnectThenSessionsSendWorks(t *testing.T) {
	gw := newFakeGateway(t, "token", nil)

	bridge := NewBridge(gw.wsURL, "token", "device-1", nil)
	t.Cleanup(func() { _ = bridge.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := bridge.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	if err := bridge.SessionsSend(ctx, "sid-reconnect", "before drop"); err != nil {
		t.Fatalf("pre-drop send failed: %v", err)
	}

	gw.dropAll()

	waitUntil(t, 6*time.Second, func() bool {
		return gw.connectionCount() >= 2
	}, "bridge did not reconnect")

	if err := bridge.SessionsSend(ctx, "sid-reconnect", "after reconnect"); err != nil {
		t.Fatalf("send after reconnect failed: %v", err)
	}
}

func TestBridgeRunSubscriberRoutesAssistantTurns(t *testing.T) {
	gw := newFakeGateway(t, "token", nil)
	bridge := NewBridge(gw.wsURL, "token", "device-1", nil)
	t.Cleanup(func() { _ = bridge.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := bridge.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	relay := spinRelay(t)
	alice := mkTestNode(t, relay, "alice")
	bob := mkTestNode(t, relay, "bob")

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
	if res := <-pairCh; res.Err != nil {
		t.Fatalf("pair result: %v", res.Err)
	}

	thread, err := alice.OpenThread(ctx, bob.Identity(), "openclaw-subscriber")
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

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	go bridge.RunSubscriber(subCtx, "sid-sub", bob, thread)
	waitUntil(t, 2*time.Second, func() bool {
		return gw.hasSubscription("sid-sub")
	}, "subscriber did not subscribe to session")

	gw.emitMessage("sid-sub", "m-human", "human", "ignore this")
	gw.emitMessage("sid-sub", "m1", "assistant", "assistant send")

	waitUntil(t, 4*time.Second, func() bool {
		envs, err := alice.ListEnvelopes(ctx, thread, 0)
		if err != nil {
			return false
		}
		for _, e := range envs {
			if e.Content.Text == "assistant send" && e.From.Role == envelope.RoleAgent && e.Intent == envelope.IntentSay {
				return true
			}
		}
		return false
	}, "assistant turn without pending ask was not sent as role=agent")
	envs, err := alice.ListEnvelopes(ctx, thread, 0)
	if err != nil {
		t.Fatalf("list envelopes after first assistant turn: %v", err)
	}
	for _, e := range envs {
		if e.Content.Text == "ignore this" {
			t.Fatal("non-assistant turn should not be forwarded")
		}
	}

	if err := alice.Send(ctx, thread, envelope.IntentAskHuman, envelope.Content{Kind: envelope.ContentText, Text: "need human"}); err != nil {
		t.Fatalf("send ask_human: %v", err)
	}
	waitUntil(t, 4*time.Second, func() bool {
		return bob.HasPendingAsk(thread)
	}, "expected pending ask on bob thread")

	gw.emitMessage("sid-sub", "m2", "assistant", "assistant human reply")
	waitUntil(t, 4*time.Second, func() bool {
		envs, err := alice.ListEnvelopes(ctx, thread, 0)
		if err != nil {
			return false
		}
		for _, e := range envs {
			if e.Content.Text == "assistant human reply" && e.From.Role == envelope.RoleHuman && e.Intent == envelope.IntentSay {
				return true
			}
		}
		return false
	}, "first assistant turn with pending ask was not submitted as human reply")
	waitUntil(t, 4*time.Second, func() bool {
		return !bob.HasPendingAsk(thread)
	}, "pending ask did not clear after SubmitHumanReply")

	gw.emitMessage("sid-sub", "m3", "assistant", "assistant send again")
	waitUntil(t, 4*time.Second, func() bool {
		envs, err := alice.ListEnvelopes(ctx, thread, 0)
		if err != nil {
			return false
		}
		for _, e := range envs {
			if e.Content.Text == "assistant send again" && e.From.Role == envelope.RoleAgent && e.Intent == envelope.IntentSay {
				return true
			}
		}
		return false
	}, "assistant turn after reply did not go back to Send")
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, failMsg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(failMsg)
}

func spinRelay(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(relayserver.New(relayserver.Config{PairRendezvousTTL: 5 * time.Second}).Handler())
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func mkTestNode(t *testing.T, relay, alias string) *node.Node {
	t.Helper()
	n, err := node.New(node.Config{
		DataDir:  t.TempDir(),
		RelayURL: relay,
		Alias:    alias,
	})
	if err != nil {
		t.Fatalf("new %s: %v", alias, err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func TestBridgeSubscribeChannelClosesOnServerSubscriptionFailure(t *testing.T) {
	var gw *fakeGateway
	gw = newFakeGateway(t, "token", func(client *fakeGatewayClient, req gatewayMessage) bool {
		if req.Method != "sessions.messages.subscribe" {
			return false
		}
		_ = gw.write(client, gatewayMessage{
			Type: "res",
			ID:   req.ID,
			Error: &gatewayError{
				Code:    "denied",
				Message: "subscription rejected",
			},
		})
		return true
	})

	bridge := NewBridge(gw.wsURL, "token", "device-1", nil)
	t.Cleanup(func() { _ = bridge.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := bridge.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	msgs, err := bridge.Subscribe(subCtx, "sid-denied")
	if err == nil {
		t.Fatal("expected subscribe to fail")
	}
	if msgs != nil {
		t.Fatal("expected nil channel on subscribe failure")
	}

	var respErr *gatewayResponseError
	if !errors.As(err, &respErr) {
		t.Fatalf("expected gatewayResponseError, got: %T (%v)", err, err)
	}
}
