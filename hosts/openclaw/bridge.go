package openclaw

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agents-first/clawdchan/core/envelope"
	"github.com/agents-first/clawdchan/core/node"
	"github.com/gorilla/websocket"
)

var errBridgeClosed = errors.New("openclaw: bridge closed")

var defaultReconnectBackoff = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	30 * time.Second,
}

// Msg is one inbound OpenClaw session message delivered by Subscribe.
type Msg struct {
	Role string
	Text string
	ID   string
}

type gatewayMessage struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"` // client→server RPC name
	Event   string          `json:"event,omitempty"`  // server→client event name
	OK      bool            `json:"ok,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *gatewayError   `json:"error,omitempty"`
}

type gatewayError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type gatewayResponseError struct {
	Code    string
	Message string
}

func (e *gatewayResponseError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("openclaw: gateway response error: %s", e.Message)
	}
	return fmt.Sprintf("openclaw: gateway response error (%s): %s", e.Code, e.Message)
}

type subscription struct {
	sid       string
	ctx       context.Context
	messages  chan Msg
	closeOnce sync.Once
}

func (s *subscription) close() {
	s.closeOnce.Do(func() {
		close(s.messages)
	})
}

// Bridge is a Gateway Protocol WebSocket client for OpenClaw.
type Bridge struct {
	wsURL     string
	token     string
	deviceID  string
	deviceKey ed25519.PrivateKey
	dialer    *websocket.Dialer

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	writeMu sync.Mutex
	nextID  uint64

	mu               sync.Mutex
	conn             *websocket.Conn
	connReady        chan struct{}
	started          bool
	closed           bool
	pending          map[string]chan gatewayMessage
	subscriptions    map[string]*subscription
	toolMu           sync.RWMutex
	tools            map[string]ToolHandler
	reconnectBackoff []time.Duration
}

// NewBridge creates a new OpenClaw gateway bridge.
// deviceKey is the persistent Ed25519 private key for this device; if nil a
// fresh key is generated (only suitable for ephemeral/test use).
func NewBridge(wsURL, token, deviceID string, deviceKey ed25519.PrivateKey) *Bridge {
	if deviceKey == nil {
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			panic("openclaw: generate device key: " + err.Error())
		}
		deviceKey = k
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Bridge{
		wsURL:            wsURL,
		token:            token,
		deviceID:         deviceID,
		deviceKey:        deviceKey,
		dialer:           &websocket.Dialer{HandshakeTimeout: 10 * time.Second},
		ctx:              ctx,
		cancel:           cancel,
		done:             make(chan struct{}),
		connReady:        make(chan struct{}),
		pending:          make(map[string]chan gatewayMessage),
		subscriptions:    make(map[string]*subscription),
		reconnectBackoff: append([]time.Duration(nil), defaultReconnectBackoff...),
		tools:            make(map[string]ToolHandler),
	}
}

// RegisterTool registers a ClawdChan tool handler by name. Calls to
// DispatchTool with the matching name will be routed to the handler.
func (b *Bridge) RegisterTool(name string, h ToolHandler) {
	b.toolMu.Lock()
	b.tools[name] = h
	b.toolMu.Unlock()
}

// DispatchTool looks up a registered tool by name and calls it with the
// provided params. Returns the JSON result string or an error.
func (b *Bridge) DispatchTool(ctx context.Context, name string, params map[string]any) (string, error) {
	b.toolMu.RLock()
	h, ok := b.tools[name]
	b.toolMu.RUnlock()
	if !ok {
		return "", fmt.Errorf("clawdchan tool %q not registered", name)
	}
	return h(ctx, params)
}

// Connect dials the gateway and completes the initial hello/connect/hello-ok handshake.
func (b *Bridge) Connect(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errBridgeClosed
	}
	if b.started {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()

	conn, err := b.dialAndHandshake(ctx)
	if err != nil {
		return err
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		_ = conn.Close()
		return errBridgeClosed
	}
	b.setConnLocked(conn)
	b.started = true
	b.mu.Unlock()

	go b.run()
	return nil
}

// SessionCreate creates a new OpenClaw session and returns its session ID.
func (b *Bridge) SessionCreate(ctx context.Context, key string) (string, error) {
	res, err := b.request(ctx, "sessions.create", map[string]string{
		"key": key,
	}, nil)
	if err != nil {
		return "", err
	}

	var out struct {
		SessionID string `json:"sessionId"`
		LegacyID  string `json:"session_id"`
		SID       string `json:"sid"`
		ID        string `json:"id"`
	}
	if len(res.Payload) > 0 {
		if err := json.Unmarshal(res.Payload, &out); err != nil {
			return "", fmt.Errorf("openclaw: decode sessions.create response: %w", err)
		}
	}

	switch {
	case out.SessionID != "":
		return out.SessionID, nil
	case out.LegacyID != "":
		return out.LegacyID, nil
	case out.SID != "":
		return out.SID, nil
	case out.ID != "":
		return out.ID, nil
	default:
		return "", errors.New("openclaw: sessions.create response missing session id")
	}
}

// SessionsSend sends a text message into an existing OpenClaw session.
func (b *Bridge) SessionsSend(ctx context.Context, sid, text string) error {
	_, err := b.request(ctx, "sessions.send", map[string]string{
		"session_id": sid,
		"text":       text,
	}, nil)
	return err
}

// Subscribe subscribes to messages in the given session.
func (b *Bridge) Subscribe(ctx context.Context, sid string) (<-chan Msg, error) {
	if sid == "" {
		return nil, errors.New("openclaw: empty session id")
	}
	sub := &subscription{
		sid:      sid,
		ctx:      ctx,
		messages: make(chan Msg, 32),
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, errBridgeClosed
	}
	if _, exists := b.subscriptions[sid]; exists {
		b.mu.Unlock()
		return nil, fmt.Errorf("openclaw: session %q already subscribed", sid)
	}
	b.subscriptions[sid] = sub
	b.mu.Unlock()

	_, err := b.request(ctx, "sessions.messages.subscribe", map[string]string{
		"session_id": sid,
	}, nil)
	if err != nil {
		b.removeSubscription(sid, sub)
		return nil, err
	}

	go func() {
		<-ctx.Done()
		b.removeSubscription(sid, sub)
	}()

	return sub.messages, nil
}

// RunSubscriber bridges OpenClaw assistant turns into ClawdChan envelopes on
// a specific thread. It exits when ctx is canceled.
func (b *Bridge) RunSubscriber(ctx context.Context, sid string, n *node.Node, thread envelope.ThreadID) {
	msgs, err := b.Subscribe(ctx, sid)
	if err != nil {
		log.Printf("openclaw: subscribe sid=%q failed: %v", sid, err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-msgs:
			if !ok {
				return
			}
			if msg.Role != "assistant" {
				continue
			}
			if IsClawdChanTurn(msg.Text) {
				continue
			}

			content := envelope.Content{
				Kind: envelope.ContentText,
				Text: msg.Text,
			}
			if n.HasPendingAsk(thread) {
				if err := n.SubmitHumanReply(ctx, thread, content); err != nil {
					log.Printf("openclaw: sid=%q submit human reply failed: %v", sid, err)
				}
				continue
			}
			if err := n.Send(ctx, thread, envelope.IntentSay, content); err != nil {
				log.Printf("openclaw: sid=%q send failed: %v", sid, err)
			}
		}
	}
}

// Close shuts down the bridge and closes all active subscription channels.
func (b *Bridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.cancel()
	conn := b.conn
	b.setConnLocked(nil)

	pending := b.pending
	b.pending = make(map[string]chan gatewayMessage)

	subs := b.subscriptions
	b.subscriptions = make(map[string]*subscription)
	started := b.started
	b.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}

	for _, ch := range pending {
		select {
		case ch <- gatewayMessage{Type: "res", Error: &gatewayError{Code: "transport", Message: errBridgeClosed.Error()}}:
		default:
		}
	}
	for _, sub := range subs {
		sub.close()
	}

	if started {
		<-b.done
	}
	return nil
}

func (b *Bridge) run() {
	defer close(b.done)

	backoffIdx := 0
	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		b.mu.Lock()
		conn := b.conn
		b.mu.Unlock()

		if conn == nil {
			delay := b.reconnectBackoff[backoffIdx]
			if !sleepContext(b.ctx, delay) {
				return
			}

			ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
			newConn, err := b.dialAndHandshake(ctx)
			cancel()
			if err != nil {
				if backoffIdx < len(b.reconnectBackoff)-1 {
					backoffIdx++
				}
				continue
			}

			b.mu.Lock()
			if b.closed {
				b.mu.Unlock()
				_ = newConn.Close()
				return
			}
			b.setConnLocked(newConn)
			b.mu.Unlock()

			backoffIdx = 0
			go b.replaySubscriptions()
			conn = newConn
		}

		if err := b.readLoop(conn); err != nil {
			b.handleDisconnect(conn, err)
			backoffIdx = 0
		}
	}
}

func (b *Bridge) readLoop(conn *websocket.Conn) error {
	for {
		select {
		case <-b.ctx.Done():
			return nil
		default:
		}

		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var msg gatewayMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "res":
			b.deliverResponse(msg)
		case "event":
			b.deliverEvent(msg)
		}
	}
}

func (b *Bridge) deliverResponse(msg gatewayMessage) {
	if msg.ID == "" {
		return
	}
	b.mu.Lock()
	ch, ok := b.pending[msg.ID]
	if ok {
		delete(b.pending, msg.ID)
	}
	b.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func (b *Bridge) deliverEvent(msg gatewayMessage) {
	if msg.Event != "sessions.messages" {
		return
	}

	var payload struct {
		SessionID string `json:"session_id"`
		SID       string `json:"sid"`
		ID        string `json:"id"`
		Role      string `json:"role"`
		Text      string `json:"text"`
		Message   struct {
			ID   string `json:"id"`
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"message"`
	}
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return
	}

	sid := payload.SessionID
	if sid == "" {
		sid = payload.SID
	}
	if sid == "" {
		return
	}

	m := Msg{
		ID:   payload.Message.ID,
		Role: payload.Message.Role,
		Text: payload.Message.Text,
	}
	if m.ID == "" {
		m.ID = payload.ID
	}
	if m.Role == "" {
		m.Role = payload.Role
	}
	if m.Text == "" {
		m.Text = payload.Text
	}

	b.mu.Lock()
	sub := b.subscriptions[sid]
	b.mu.Unlock()
	if sub == nil {
		return
	}

	select {
	case sub.messages <- m:
	case <-sub.ctx.Done():
		b.removeSubscription(sid, sub)
	case <-b.ctx.Done():
	}
}

func (b *Bridge) request(ctx context.Context, method string, params, payload any) (gatewayMessage, error) {
	id := strconv.FormatUint(atomic.AddUint64(&b.nextID, 1), 10)

	req := gatewayMessage{
		Type:   "req",
		ID:     id,
		Method: method,
	}
	var err error
	req.Params, err = marshalRaw(params)
	if err != nil {
		return gatewayMessage{}, err
	}
	req.Payload, err = marshalRaw(payload)
	if err != nil {
		return gatewayMessage{}, err
	}

	conn, err := b.waitForConn(ctx)
	if err != nil {
		return gatewayMessage{}, err
	}

	respCh := make(chan gatewayMessage, 1)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return gatewayMessage{}, errBridgeClosed
	}
	b.pending[id] = respCh
	b.mu.Unlock()

	if err := b.writeMessage(ctx, conn, req); err != nil {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		b.handleDisconnect(conn, err)
		return gatewayMessage{}, err
	}

	select {
	case res := <-respCh:
		if res.Error != nil {
			return gatewayMessage{}, &gatewayResponseError{
				Code:    res.Error.Code,
				Message: res.Error.Message,
			}
		}
		return res, nil
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return gatewayMessage{}, ctx.Err()
	case <-b.ctx.Done():
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return gatewayMessage{}, errBridgeClosed
	}
}

func (b *Bridge) waitForConn(ctx context.Context) (*websocket.Conn, error) {
	for {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return nil, errBridgeClosed
		}
		conn := b.conn
		ready := b.connReady
		b.mu.Unlock()

		if conn != nil {
			return conn, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-b.ctx.Done():
			return nil, errBridgeClosed
		case <-ready:
		}
	}
}

func (b *Bridge) handleDisconnect(conn *websocket.Conn, err error) {
	b.mu.Lock()
	if b.conn == conn {
		b.setConnLocked(nil)
	}
	b.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	b.failPending(err)
}

func (b *Bridge) failPending(err error) {
	if err == nil {
		err = errors.New("openclaw: disconnected")
	}
	b.mu.Lock()
	pending := b.pending
	b.pending = make(map[string]chan gatewayMessage)
	b.mu.Unlock()

	fail := gatewayMessage{
		Type: "res",
		Error: &gatewayError{
			Code:    "transport",
			Message: err.Error(),
		},
	}
	for _, ch := range pending {
		select {
		case ch <- fail:
		default:
		}
	}
}

func (b *Bridge) dialAndHandshake(ctx context.Context) (*websocket.Conn, error) {
	if b.wsURL == "" {
		return nil, errors.New("openclaw: empty gateway URL")
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+b.token)

	conn, resp, err := b.dialer.DialContext(ctx, b.wsURL, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("openclaw: dial gateway: %w (status %s)", err, resp.Status)
		}
		return nil, fmt.Errorf("openclaw: dial gateway: %w", err)
	}
	if err := b.handshake(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (b *Bridge) handshake(ctx context.Context, conn *websocket.Conn) error {
	// Step 1: server sends connect.challenge with a nonce.
	challenge, err := readGatewayMessage(ctx, conn)
	if err != nil {
		return fmt.Errorf("openclaw: handshake challenge: %w", err)
	}
	if challenge.Type != "event" || challenge.Event != "connect.challenge" {
		return fmt.Errorf("openclaw: expected connect.challenge event, got type=%q event=%q", challenge.Type, challenge.Event)
	}

	var challengePayload struct {
		Nonce string `json:"nonce"`
	}
	if len(challenge.Payload) > 0 {
		if err := json.Unmarshal(challenge.Payload, &challengePayload); err != nil {
			return fmt.Errorf("openclaw: decode challenge payload: %w", err)
		}
	}

	// Step 2: compute device ID and sign the v2 payload with the stable device key.
	pub := b.deviceKey.Public().(ed25519.PublicKey)
	pubHash := sha256.Sum256(pub)
	actualDeviceID := hex.EncodeToString(pubHash[:])

	signedAt := time.Now().UnixMilli()
	payloadStr := fmt.Sprintf("v2|%s|cli|node|operator|operator.read,operator.write|%d|%s|%s", actualDeviceID, signedAt, b.token, challengePayload.Nonce)

	sig := ed25519.Sign(b.deviceKey, []byte(payloadStr))
	pubB64 := base64.RawURLEncoding.EncodeToString(pub)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	// Step 3: send connect with auth token and signed device identity.
	reqID := strconv.FormatUint(atomic.AddUint64(&b.nextID, 1), 10)
	params, err := marshalRaw(map[string]any{
		"minProtocol": 3,
		"maxProtocol": 3,
		"client": map[string]any{
			"id":       "cli",
			"version":  "1.0.0",
			"platform": runtime.GOOS,
			"mode":     "node",
		},
		"role":   "operator",
		"scopes": []string{"operator.read", "operator.write"},
		"caps":   []string{},
		"auth": map[string]string{
			"token": b.token,
		},
		"device": map[string]any{
			"id":        actualDeviceID,
			"nonce":     challengePayload.Nonce,
			"publicKey": pubB64,
			"signature": sigB64,
			"signedAt":  signedAt,
		},
	})
	if err != nil {
		return err
	}
	req := gatewayMessage{
		Type:   "req",
		ID:     reqID,
		Method: "connect",
		Params: params,
	}
	if err := b.writeMessage(ctx, conn, req); err != nil {
		return fmt.Errorf("openclaw: send connect request: %w", err)
	}

	// Step 3: wait for res with ok=true (payload.type=="hello-ok").
	for {
		msg, err := readGatewayMessage(ctx, conn)
		if err != nil {
			return fmt.Errorf("openclaw: wait hello-ok: %w", err)
		}
		if msg.Type == "res" && msg.ID == reqID {
			if msg.Error != nil {
				return &gatewayResponseError{Code: msg.Error.Code, Message: msg.Error.Message}
			}
			return nil
		}
	}
}

func (b *Bridge) replaySubscriptions() {
	b.mu.Lock()
	subs := make([]*subscription, 0, len(b.subscriptions))
	for _, sub := range b.subscriptions {
		subs = append(subs, sub)
	}
	b.mu.Unlock()

	for _, sub := range subs {
		select {
		case <-b.ctx.Done():
			return
		default:
		}
		if sub.ctx.Err() != nil {
			b.removeSubscription(sub.sid, sub)
			continue
		}

		ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
		_, err := b.request(ctx, "sessions.messages.subscribe", map[string]string{
			"session_id": sub.sid,
		}, nil)
		cancel()
		if err == nil {
			continue
		}
		var gwErr *gatewayResponseError
		if errors.As(err, &gwErr) {
			b.removeSubscription(sub.sid, sub)
		}
	}
}

func (b *Bridge) removeSubscription(sid string, target *subscription) {
	var sub *subscription

	b.mu.Lock()
	current, ok := b.subscriptions[sid]
	if ok && (target == nil || target == current) {
		sub = current
		delete(b.subscriptions, sid)
	}
	b.mu.Unlock()

	if sub != nil {
		sub.close()
	}
}

func (b *Bridge) setConnLocked(conn *websocket.Conn) {
	b.conn = conn
	if conn != nil {
		select {
		case <-b.connReady:
		default:
			close(b.connReady)
		}
		return
	}
	b.connReady = make(chan struct{})
}

func (b *Bridge) writeMessage(ctx context.Context, conn *websocket.Conn, msg gatewayMessage) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
	} else {
		_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	}
	return conn.WriteMessage(websocket.TextMessage, raw)
}

func readGatewayMessage(ctx context.Context, conn *websocket.Conn) (gatewayMessage, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(dl)
	} else {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return gatewayMessage{}, err
	}
	var msg gatewayMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return gatewayMessage{}, err
	}
	return msg, nil
}

func marshalRaw(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// LoadOrCreateDeviceKey loads a persistent Ed25519 device key from keyPath,
// creating and saving a new one if the file doesn't exist. The key is stored
// as raw 64-byte seed+private in binary — opaque to users, only used by the
// gateway to verify device identity across daemon restarts.
func LoadOrCreateDeviceKey(keyPath string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(keyPath)
	if err == nil && len(data) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(data), nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("openclaw: generate device key: %w", err)
	}
	if err := os.WriteFile(keyPath, []byte(priv), 0o600); err != nil {
		return nil, fmt.Errorf("openclaw: save device key: %w", err)
	}
	return priv, nil
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
