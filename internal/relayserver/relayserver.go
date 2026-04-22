// Package relayserver implements the ClawdChan reference relay as a library so
// it can be embedded in integration tests as well as driven by the
// clawdchan-relay command.
package relayserver

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/agents-first/clawdchan/core/identity"
	"github.com/agents-first/clawdchan/core/relaywire"
)

// Config tunes retention and limits. Zero values are replaced with defaults.
type Config struct {
	QueueRetention    time.Duration
	MaxQueuedPerPeer  int
	PairMaxMsgBytes   int64
	LinkMaxMsgBytes   int64
	ReadWriteTimeout  time.Duration
	PairRendezvousTTL time.Duration
}

func (c *Config) applyDefaults() {
	if c.QueueRetention == 0 {
		c.QueueRetention = 24 * time.Hour
	}
	if c.MaxQueuedPerPeer == 0 {
		c.MaxQueuedPerPeer = 1024
	}
	if c.PairMaxMsgBytes == 0 {
		c.PairMaxMsgBytes = 1 << 16
	}
	if c.LinkMaxMsgBytes == 0 {
		c.LinkMaxMsgBytes = 1 << 20
	}
	if c.ReadWriteTimeout == 0 {
		c.ReadWriteTimeout = 60 * time.Second
	}
	if c.PairRendezvousTTL == 0 {
		c.PairRendezvousTTL = 10 * time.Minute
	}
}

// Relay is an in-memory ClawdChan relay.
type Relay struct {
	cfg      Config
	upgrader websocket.Upgrader

	mu      sync.Mutex
	links   map[identity.NodeID]*link
	queues  map[identity.NodeID][]queuedFrame
	pairing map[string]*pairSlot
}

type link struct {
	id        identity.NodeID
	conn      *websocket.Conn
	send      chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	cfg       *Config
}

type queuedFrame struct {
	from identity.NodeID
	data []byte
	at   time.Time
}

type pairSlot struct {
	conn *websocket.Conn
	done chan struct{}
}

// New constructs a Relay with the given config.
func New(cfg Config) *Relay {
	cfg.applyDefaults()
	return &Relay{
		cfg:      cfg,
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		links:    make(map[identity.NodeID]*link),
		queues:   make(map[identity.NodeID][]queuedFrame),
		pairing:  make(map[string]*pairSlot),
	}
}

// Handler returns an http.Handler exposing /link, /pair, and /healthz.
func (r *Relay) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/link", r.serveLink)
	mux.HandleFunc("/pair", r.servePair)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return mux
}

func (r *Relay) serveLink(w http.ResponseWriter, req *http.Request) {
	id, err := verifyLinkAuth(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	conn, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(r.cfg.LinkMaxMsgBytes)

	// Reset the read deadline whenever a ping arrives so idle links don't
	// flap. gorilla responds to the ping with a pong automatically once we
	// return from this handler.
	timeout := r.cfg.ReadWriteTimeout
	conn.SetReadDeadline(time.Now().Add(timeout))
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(timeout))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})

	l := &link{
		id:     id,
		conn:   conn,
		send:   make(chan []byte, 64),
		closed: make(chan struct{}),
		cfg:    &r.cfg,
	}

	r.mu.Lock()
	if existing, ok := r.links[id]; ok {
		existing.close()
	}
	r.links[id] = l
	queued := r.queues[id]
	delete(r.queues, id)
	r.mu.Unlock()

	go l.writeLoop()

	for _, q := range queued {
		l.deliver(q.from, q.data)
	}

	l.readLoop(r)

	r.mu.Lock()
	if r.links[id] == l {
		delete(r.links, id)
	}
	r.mu.Unlock()
	l.close()
}

func (l *link) writeLoop() {
	for {
		select {
		case msg, ok := <-l.send:
			if !ok {
				return
			}
			l.conn.SetWriteDeadline(time.Now().Add(l.cfg.ReadWriteTimeout))
			if err := l.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-l.closed:
			return
		}
	}
}

func (l *link) close() {
	l.closeOnce.Do(func() {
		close(l.closed)
		l.conn.Close()
	})
}

func (l *link) readLoop(r *Relay) {
	for {
		l.conn.SetReadDeadline(time.Now().Add(l.cfg.ReadWriteTimeout))
		_, raw, err := l.conn.ReadMessage()
		if err != nil {
			return
		}
		var env relaywire.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			l.sendCtl("error", "bad json")
			continue
		}
		if env.Frame == nil {
			continue
		}
		toBytes, err := base64.StdEncoding.DecodeString(env.Frame.To)
		if err != nil || len(toBytes) != ed25519.PublicKeySize {
			l.sendCtl("error", "bad destination")
			continue
		}
		var dest identity.NodeID
		copy(dest[:], toBytes)
		data, err := base64.StdEncoding.DecodeString(env.Frame.Data)
		if err != nil {
			l.sendCtl("error", "bad data")
			continue
		}
		r.mu.Lock()
		peer, online := r.links[dest]
		r.mu.Unlock()
		if online {
			peer.deliver(l.id, data)
			continue
		}
		r.enqueue(dest, l.id, data)
		l.sendCtl("queued", "recipient offline; frame queued")
	}
}

func (l *link) deliver(from identity.NodeID, data []byte) {
	env := relaywire.Envelope{Frame: &relaywire.Frame{
		From: base64.StdEncoding.EncodeToString(from[:]),
		Data: base64.StdEncoding.EncodeToString(data),
	}}
	raw, _ := json.Marshal(env)
	select {
	case l.send <- raw:
	case <-l.closed:
	}
}

func (l *link) sendCtl(kind, msg string) {
	env := relaywire.Envelope{Ctl: &relaywire.Ctl{Kind: kind, Message: msg}}
	raw, _ := json.Marshal(env)
	select {
	case l.send <- raw:
	case <-l.closed:
	}
}

func (r *Relay) enqueue(dest, from identity.NodeID, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.queues[dest]
	cutoff := time.Now().Add(-r.cfg.QueueRetention)
	pruned := q[:0]
	for _, f := range q {
		if f.at.After(cutoff) {
			pruned = append(pruned, f)
		}
	}
	pruned = append(pruned, queuedFrame{from: from, data: data, at: time.Now()})
	if len(pruned) > r.cfg.MaxQueuedPerPeer {
		pruned = pruned[len(pruned)-r.cfg.MaxQueuedPerPeer:]
	}
	r.queues[dest] = pruned
}

func verifyLinkAuth(req *http.Request) (identity.NodeID, error) {
	q := req.URL.Query()
	nodeB64 := q.Get("node_id")
	tsStr := q.Get("ts")
	sigB64 := q.Get("sig")
	if nodeB64 == "" || tsStr == "" || sigB64 == "" {
		return identity.NodeID{}, errors.New("missing auth params")
	}
	nodeBytes, err := base64.RawURLEncoding.DecodeString(nodeB64)
	if err != nil || len(nodeBytes) != ed25519.PublicKeySize {
		return identity.NodeID{}, errors.New("bad node_id")
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return identity.NodeID{}, errors.New("bad sig")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return identity.NodeID{}, errors.New("bad ts")
	}
	now := time.Now().UnixMilli()
	if diff := now - ts; diff > relaywire.LinkAuthMaxSkewMs || diff < -relaywire.LinkAuthMaxSkewMs {
		return identity.NodeID{}, errors.New("ts out of range")
	}
	msg := relaywire.AuthMessage(nodeBytes, ts)
	if !ed25519.Verify(ed25519.PublicKey(nodeBytes), msg, sigBytes) {
		return identity.NodeID{}, errors.New("bad signature")
	}
	var id identity.NodeID
	copy(id[:], nodeBytes)
	return id, nil
}

func (r *Relay) servePair(w http.ResponseWriter, req *http.Request) {
	codeHash := req.URL.Query().Get("code_hash")
	if codeHash == "" {
		http.Error(w, "missing code_hash", http.StatusBadRequest)
		return
	}
	conn, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(r.cfg.PairMaxMsgBytes)

	r.mu.Lock()
	if slot, ok := r.pairing[codeHash]; ok {
		delete(r.pairing, codeHash)
		r.mu.Unlock()
		pipe(conn, slot.conn, slot.done, r.cfg.ReadWriteTimeout)
		return
	}
	slot := &pairSlot{conn: conn, done: make(chan struct{})}
	r.pairing[codeHash] = slot
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.PairRendezvousTTL)
	defer cancel()

	select {
	case <-slot.done:
		return
	case <-ctx.Done():
		r.mu.Lock()
		if cur, ok := r.pairing[codeHash]; ok && cur == slot {
			delete(r.pairing, codeHash)
		}
		r.mu.Unlock()
		conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rendezvous timeout"),
			time.Now().Add(time.Second))
		conn.Close()
	}
}

func pipe(a, b *websocket.Conn, done chan struct{}, timeout time.Duration) {
	var wg sync.WaitGroup
	wg.Add(2)
	stop := make(chan struct{})
	one := func(src, dst *websocket.Conn) {
		defer wg.Done()
		for {
			src.SetReadDeadline(time.Now().Add(timeout))
			mt, data, err := src.ReadMessage()
			if err != nil {
				select {
				case <-stop:
				default:
					close(stop)
				}
				return
			}
			dst.SetWriteDeadline(time.Now().Add(timeout))
			if err := dst.WriteMessage(mt, data); err != nil {
				select {
				case <-stop:
				default:
					close(stop)
				}
				return
			}
		}
	}
	go one(a, b)
	go one(b, a)
	<-stop
	a.Close()
	b.Close()
	wg.Wait()
	close(done)
}
