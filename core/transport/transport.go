// Package transport is the bytes-on-the-wire layer between a node and the
// relay. A node opens one authenticated Link and multiplexes frames to every
// peer across it.
package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/relaywire"
)

// Frame is one inbound message: an opaque payload addressed from a peer.
type Frame struct {
	From identity.NodeID
	Data []byte
}

// CtlEvent is a non-frame signal from the relay (e.g. "queued").
type CtlEvent struct {
	Kind    string
	Message string
}

// Link is a single authenticated connection to the relay. It is safe for
// concurrent Send callers; Recv must be called from a single goroutine.
type Link interface {
	Send(ctx context.Context, to identity.NodeID, data []byte) error
	Recv(ctx context.Context) (Frame, error)
	Events() <-chan CtlEvent
	Close() error
}

// Transport opens Links against a relay.
type Transport interface {
	Connect(ctx context.Context, id *identity.Identity) (Link, error)
}

// WSTransport dials a WebSocket relay at the configured base URL (ws://host or
// wss://host).
type WSTransport struct {
	BaseURL string
	Dialer  *websocket.Dialer
	// PingInterval controls how often the client sends a WebSocket ping to
	// the relay. Zero uses the default (20s). Keepalive is what stops home
	// NATs and default-60s relay read deadlines from flapping idle links.
	PingInterval time.Duration
	// ReadTimeout is the read deadline reset on every frame or pong. Zero
	// uses the default (60s); must be comfortably larger than PingInterval.
	ReadTimeout time.Duration
}

// NewWS returns a Transport that connects to a relay at baseURL.
func NewWS(baseURL string) *WSTransport {
	return &WSTransport{
		BaseURL: baseURL,
		Dialer:  &websocket.Dialer{HandshakeTimeout: 10 * time.Second},
	}
}

// Connect opens a /link WebSocket with a signed auth blob.
func (t *WSTransport) Connect(ctx context.Context, id *identity.Identity) (Link, error) {
	if t.BaseURL == "" {
		return nil, errors.New("transport: empty base URL")
	}
	u, err := url.Parse(t.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	u.Path = "/link"

	ts := time.Now().UnixMilli()
	msg := relaywire.AuthMessage(id.SigningPublic[:], ts)
	sig := ed25519.Sign(id.SigningPrivate, msg)

	q := u.Query()
	q.Set("node_id", base64.RawURLEncoding.EncodeToString(id.SigningPublic[:]))
	q.Set("ts", fmt.Sprintf("%d", ts))
	q.Set("sig", base64.RawURLEncoding.EncodeToString(sig))
	u.RawQuery = q.Encode()

	dialer := t.Dialer
	if dialer == nil {
		dialer = &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	}
	conn, _, err := dialer.DialContext(ctx, u.String(), http.Header{})
	if err != nil {
		return nil, fmt.Errorf("dial relay: %w", err)
	}

	pingEvery := t.PingInterval
	if pingEvery <= 0 {
		pingEvery = 20 * time.Second
	}
	readTimeout := t.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = 60 * time.Second
	}

	link := &wsLink{
		conn:         conn,
		events:       make(chan CtlEvent, 8),
		frames:       make(chan Frame, 32),
		closed:       make(chan struct{}),
		pingInterval: pingEvery,
		readTimeout:  readTimeout,
	}
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		return nil
	})

	go link.readLoop()
	go link.pingLoop()
	return link, nil
}

type wsLink struct {
	conn         *websocket.Conn
	writeMu      sync.Mutex
	events       chan CtlEvent
	frames       chan Frame
	closed       chan struct{}
	closeOnce    sync.Once
	readErr      error
	pingInterval time.Duration
	readTimeout  time.Duration
}

func (l *wsLink) pingLoop() {
	t := time.NewTicker(l.pingInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			l.writeMu.Lock()
			err := l.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			l.writeMu.Unlock()
			if err != nil {
				return
			}
		case <-l.closed:
			return
		}
	}
}

func (l *wsLink) Send(ctx context.Context, to identity.NodeID, data []byte) error {
	env := relaywire.Envelope{Frame: &relaywire.Frame{
		To:   base64.StdEncoding.EncodeToString(to[:]),
		Data: base64.StdEncoding.EncodeToString(data),
	}}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		l.conn.SetWriteDeadline(dl)
	} else {
		l.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	}
	return l.conn.WriteMessage(websocket.TextMessage, raw)
}

func (l *wsLink) Recv(ctx context.Context) (Frame, error) {
	select {
	case f, ok := <-l.frames:
		if !ok {
			if l.readErr != nil {
				return Frame{}, l.readErr
			}
			return Frame{}, errors.New("transport: link closed")
		}
		return f, nil
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	}
}

func (l *wsLink) Events() <-chan CtlEvent { return l.events }

func (l *wsLink) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		l.conn.Close()
	})
	return nil
}

func (l *wsLink) readLoop() {
	defer close(l.frames)
	defer close(l.events)
	for {
		_, raw, err := l.conn.ReadMessage()
		if err != nil {
			l.readErr = err
			return
		}
		l.conn.SetReadDeadline(time.Now().Add(l.readTimeout))
		dec := json.NewDecoder(bytes.NewReader(raw))
		var env relaywire.Envelope
		if err := dec.Decode(&env); err != nil {
			continue
		}
		if env.Ctl != nil {
			select {
			case l.events <- CtlEvent{Kind: env.Ctl.Kind, Message: env.Ctl.Message}:
			case <-l.closed:
				return
			default:
			}
			continue
		}
		if env.Frame == nil {
			continue
		}
		fromBytes, err := base64.StdEncoding.DecodeString(env.Frame.From)
		if err != nil || len(fromBytes) != ed25519.PublicKeySize {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(env.Frame.Data)
		if err != nil {
			continue
		}
		var from identity.NodeID
		copy(from[:], fromBytes)
		select {
		case l.frames <- Frame{From: from, Data: data}:
		case <-l.closed:
			return
		}
	}
}
