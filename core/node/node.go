// Package node ties the core packages together into a single running node
// that hosts exactly one human and one or more agents.
package node

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/agents-first/clawdchan/core/envelope"
	"github.com/agents-first/clawdchan/core/identity"
	"github.com/agents-first/clawdchan/core/pairing"
	"github.com/agents-first/clawdchan/core/policy"
	"github.com/agents-first/clawdchan/core/session"
	"github.com/agents-first/clawdchan/core/store"
	"github.com/agents-first/clawdchan/core/surface"
	"github.com/agents-first/clawdchan/core/transport"
)

// Config configures a Node.
type Config struct {
	// DataDir holds the node's SQLite database.
	DataDir string
	// RelayURL is the ws:// or wss:// URL of the ClawdChan relay.
	RelayURL string
	// Alias is the display name broadcast during pairing.
	Alias string
	// Human is the host-provided human surface. nil defaults to NopHuman.
	Human surface.HumanSurface
	// Agent is the host-provided agent surface. nil defaults to NopAgent.
	Agent surface.AgentSurface
	// Policy gates inbound envelopes. nil defaults to policy.Default().
	Policy policy.Engine
	// Ephemeral, when true, wipes any persisted threads / envelopes /
	// outbox at node startup. Identity and pairings are preserved. Intended
	// for hosts (e.g. clawdchan-mcp) whose unit of persistence is a single
	// Claude Code session: each fresh MCP process begins with an empty
	// thread list.
	Ephemeral bool
}

// Node is the public entry point for host bindings and the CLI.
type Node struct {
	cfg       Config
	store     store.Store
	identity  *identity.Identity
	transport transport.Transport
	human     surface.HumanSurface
	agent     surface.AgentSurface
	policy    policy.Engine

	mu       sync.Mutex
	link     transport.Link
	sessions map[identity.NodeID]*session.Session
	subs     map[envelope.ThreadID]map[*subscription]struct{}
	stopCh   chan struct{}
	running  bool
}

type subscription struct {
	thread envelope.ThreadID
	ch     chan envelope.Envelope
}

// PairResult is produced by an asynchronous Pair initiation.
type PairResult struct {
	Peer pairing.Peer
	Err  error
}

// New constructs a Node. It opens the store, ensures an identity exists, and
// prepares the transport. It does not start network I/O; call Start.
func New(cfg Config) (*Node, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("node: DataDir is required")
	}
	if cfg.RelayURL == "" {
		return nil, errors.New("node: RelayURL is required")
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "clawdchan.db"))
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	id, err := st.LoadIdentity(ctx)
	if errors.Is(err, store.ErrNotFound) {
		id, err = identity.Generate()
		if err != nil {
			st.Close()
			return nil, err
		}
		if err := st.SaveIdentity(ctx, id); err != nil {
			st.Close()
			return nil, err
		}
	} else if err != nil {
		st.Close()
		return nil, err
	}
	if cfg.Ephemeral {
		if err := st.PurgeConversations(ctx); err != nil {
			st.Close()
			return nil, fmt.Errorf("node: purge on ephemeral boot: %w", err)
		}
	}
	n := &Node{
		cfg:       cfg,
		store:     st,
		identity:  id,
		transport: transport.NewWS(cfg.RelayURL),
		human:     cfg.Human,
		agent:     cfg.Agent,
		policy:    cfg.Policy,
		sessions:  make(map[identity.NodeID]*session.Session),
		subs:      make(map[envelope.ThreadID]map[*subscription]struct{}),
	}
	if n.human == nil {
		n.human = surface.NopHuman{}
	}
	if n.agent == nil {
		n.agent = surface.NopAgent{}
	}
	if n.policy == nil {
		n.policy = policy.Default()
	}
	return n, nil
}

// Identity returns the node's signing public key.
func (n *Node) Identity() identity.NodeID { return n.identity.SigningPublic }

// DataDir returns the directory holding this node's config and store. Host
// bindings use it to locate sibling resources such as the listener registry.
func (n *Node) DataDir() string { return n.cfg.DataDir }

// RelayURL returns the relay this node is configured against.
func (n *Node) RelayURL() string { return n.cfg.RelayURL }

// Alias returns the node's configured display alias.
func (n *Node) Alias() string { return n.cfg.Alias }

// Store returns the node's SQLite-backed persistence. Host bindings (e.g.
// openclaw) reuse it rather than opening a second connection to the same
// file — SQLite is best with a single handle per process.
func (n *Node) Store() store.Store { return n.store }

// SetHumanSurface swaps the current human surface. It is safe to call before
// Start, and during runtime if a host needs to transfer ownership.
func (n *Node) SetHumanSurface(h surface.HumanSurface) {
	if h == nil {
		h = surface.NopHuman{}
	}
	n.mu.Lock()
	n.human = h
	n.mu.Unlock()
}

// SetAgentSurface swaps the current agent surface. It is safe to call before
// Start, and during runtime if a host needs to transfer ownership.
func (n *Node) SetAgentSurface(a surface.AgentSurface) {
	if a == nil {
		a = surface.NopAgent{}
	}
	n.mu.Lock()
	n.agent = a
	n.mu.Unlock()
}

// Close releases node resources. After Close, the node is unusable.
func (n *Node) Close() error {
	n.Stop()
	return n.store.Close()
}

// Start opens the transport link and spawns inbound and outbox drain loops.
// Start returns immediately. It is safe to call Pair/Consume/Send before
// Start, but those calls may not route traffic until Start completes.
func (n *Node) Start(ctx context.Context) error {
	n.mu.Lock()
	if n.running {
		n.mu.Unlock()
		return nil
	}
	link, err := n.transport.Connect(ctx, n.identity)
	if err != nil {
		n.mu.Unlock()
		return fmt.Errorf("node: connect relay: %w", err)
	}
	n.link = link
	n.stopCh = make(chan struct{})
	n.running = true
	n.mu.Unlock()

	go n.inboundLoop()
	go n.drainOutboxLoop()
	return nil
}

// Stop ends inbound/outbox loops and closes the relay link.
func (n *Node) Stop() {
	n.mu.Lock()
	if !n.running {
		n.mu.Unlock()
		return
	}
	n.running = false
	close(n.stopCh)
	link := n.link
	n.link = nil
	n.mu.Unlock()
	if link != nil {
		link.Close()
	}
}

// Pair generates a pairing code and begins rendezvous in the background. The
// mnemonic representation of the returned Code is what the user shows to the
// peer. The returned channel yields a PairResult when the peer consumes.
func (n *Node) Pair(ctx context.Context) (pairing.Code, <-chan PairResult, error) {
	code, err := pairing.GenerateCode()
	if err != nil {
		return pairing.Code{}, nil, err
	}
	card := pairing.MyCard(n.identity, n.cfg.Alias, n.currentHuman().Reachability() != surface.Unreachable)
	out := make(chan PairResult, 1)
	go func() {
		peer, err := pairing.Rendezvous(ctx, n.cfg.RelayURL, code, card, true)
		if err != nil {
			out <- PairResult{Err: err}
			return
		}
		if err := n.store.UpsertPeer(context.Background(), peer); err != nil {
			out <- PairResult{Peer: peer, Err: fmt.Errorf("persist peer: %w", err)}
			return
		}
		out <- PairResult{Peer: peer}
	}()
	return code, out, nil
}

// Consume pairs with a peer using a mnemonic generated on the other side.
func (n *Node) Consume(ctx context.Context, mnemonic string) (pairing.Peer, error) {
	code, err := pairing.ParseCode(mnemonic)
	if err != nil {
		return pairing.Peer{}, err
	}
	card := pairing.MyCard(n.identity, n.cfg.Alias, n.currentHuman().Reachability() != surface.Unreachable)
	peer, err := pairing.Rendezvous(ctx, n.cfg.RelayURL, code, card, false)
	if err != nil {
		return pairing.Peer{}, err
	}
	if err := n.store.UpsertPeer(ctx, peer); err != nil {
		return peer, fmt.Errorf("persist peer: %w", err)
	}
	return peer, nil
}

// ListPeers returns all paired peers.
func (n *Node) ListPeers(ctx context.Context) ([]pairing.Peer, error) {
	return n.store.ListPeers(ctx)
}

// GetPeer returns a peer by node id.
func (n *Node) GetPeer(ctx context.Context, id identity.NodeID) (pairing.Peer, error) {
	return n.store.GetPeer(ctx, id)
}

// SetPeerAlias overrides the local display alias for a peer. The peer's
// own self-declared alias (from pairing or envelope headers) is kept in
// the envelope's From.Alias but the store-backed alias is what surfaces
// in CLI listings, MCP tool output, and daemon notifications.
func (n *Node) SetPeerAlias(ctx context.Context, id identity.NodeID, alias string) error {
	return n.store.SetPeerAlias(ctx, id, alias)
}

// RevokePeer marks a peer's trust as revoked. Inbound envelopes from the
// peer are dropped; outbound sends return an error. The record is kept
// so history remains inspectable.
func (n *Node) RevokePeer(ctx context.Context, id identity.NodeID) error {
	return n.store.RevokePeer(ctx, id)
}

// DeletePeer hard-removes the peer plus all threads, envelopes, and outbox
// entries tied to that peer. Use when the user wants a full forget;
// prefer RevokePeer to preserve history while cutting trust.
func (n *Node) DeletePeer(ctx context.Context, id identity.NodeID) error {
	return n.store.DeletePeer(ctx, id)
}

// OpenThread creates a new thread with the given peer.
func (n *Node) OpenThread(ctx context.Context, peer identity.NodeID, topic string) (envelope.ThreadID, error) {
	if _, err := n.store.GetPeer(ctx, peer); err != nil {
		return envelope.ThreadID{}, fmt.Errorf("open thread: %w", err)
	}
	t := store.Thread{
		ID:        newULID(),
		PeerID:    peer,
		Topic:     topic,
		CreatedMs: time.Now().UnixMilli(),
	}
	if err := n.store.CreateThread(ctx, t); err != nil {
		return envelope.ThreadID{}, err
	}
	return t.ID, nil
}

// ListThreads returns all threads.
func (n *Node) ListThreads(ctx context.Context) ([]store.Thread, error) {
	return n.store.ListThreads(ctx)
}

// GetThread returns a thread descriptor.
func (n *Node) GetThread(ctx context.Context, id envelope.ThreadID) (store.Thread, error) {
	return n.store.GetThread(ctx, id)
}

// ListEnvelopes returns envelopes on thread newer than sinceMs.
func (n *Node) ListEnvelopes(ctx context.Context, thread envelope.ThreadID, sinceMs int64) ([]envelope.Envelope, error) {
	return n.store.ListEnvelopes(ctx, thread, sinceMs)
}

// Send emits a new envelope on thread with the given intent and content.
func (n *Node) Send(ctx context.Context, thread envelope.ThreadID, intent envelope.Intent, content envelope.Content) error {
	return n.sendAs(ctx, thread, envelope.RoleAgent, intent, content)
}

// SubmitHumanReply sends content from role=human on thread. Called by a
// HumanSurface whose Ask path is inherently asynchronous (e.g. OpenClaw
// routing via WhatsApp).
func (n *Node) SubmitHumanReply(ctx context.Context, thread envelope.ThreadID, content envelope.Content) error {
	return n.sendAs(ctx, thread, envelope.RoleHuman, envelope.IntentSay, content)
}

// HasPendingAsk reports whether thread has at least one inbound ask_human
// envelope that has not yet been answered by a local role=human reply.
func (n *Node) HasPendingAsk(thread envelope.ThreadID) bool {
	envs, err := n.store.ListEnvelopes(context.Background(), thread, 0)
	if err != nil {
		return false
	}
	me := n.identity.SigningPublic
	pending := 0
	for _, e := range envs {
		if e.Intent == envelope.IntentAskHuman && e.From.NodeID != me {
			pending++
			continue
		}
		if pending > 0 && e.From.NodeID == me && e.From.Role == envelope.RoleHuman {
			pending--
		}
	}
	return pending > 0
}

func (n *Node) sendAs(ctx context.Context, thread envelope.ThreadID, role envelope.Role, intent envelope.Intent, content envelope.Content) error {
	t, err := n.store.GetThread(ctx, thread)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	peer, err := n.store.GetPeer(ctx, t.PeerID)
	if err != nil {
		return fmt.Errorf("send: peer: %w", err)
	}
	if peer.Trust == pairing.TrustRevoked {
		return errors.New("send: peer is revoked")
	}

	env := envelope.Envelope{
		Version:     envelope.Version,
		EnvelopeID:  newULID(),
		ThreadID:    thread,
		From:        envelope.Principal{NodeID: n.identity.SigningPublic, Role: role, Alias: n.cfg.Alias},
		Intent:      intent,
		CreatedAtMs: time.Now().UnixMilli(),
		Content:     content,
	}
	if err := envelope.Sign(&env, n.identity); err != nil {
		return err
	}

	// Store locally before attempting to send; drivers are idempotent on re-send.
	if err := n.store.AppendEnvelope(ctx, env, false); err != nil {
		return err
	}
	n.publish(env)

	delivered, sendErr := n.deliver(ctx, peer, env)
	if !delivered {
		if err := n.store.EnqueueOutbox(ctx, peer.NodeID, env); err != nil {
			return fmt.Errorf("queue outbox: %w", err)
		}
	}
	if sendErr != nil {
		return nil // stored + queued for retry; caller doesn't need to know
	}
	return nil
}

// deliver serializes, encrypts, and sends env to peer over the transport.
// Returns (true, nil) on success.
func (n *Node) deliver(ctx context.Context, peer pairing.Peer, env envelope.Envelope) (bool, error) {
	n.mu.Lock()
	link := n.link
	n.mu.Unlock()
	if link == nil {
		return false, errors.New("link not started")
	}
	sess, err := n.sessionFor(peer)
	if err != nil {
		return false, err
	}
	blob, err := envelope.Marshal(env)
	if err != nil {
		return false, err
	}
	frame, err := sess.Seal(blob)
	if err != nil {
		return false, err
	}
	if err := link.Send(ctx, peer.NodeID, frame); err != nil {
		return false, err
	}
	return true, nil
}

func (n *Node) sessionFor(peer pairing.Peer) (*session.Session, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if s, ok := n.sessions[peer.NodeID]; ok {
		return s, nil
	}
	s, err := session.New(n.identity, peer.KexPub)
	if err != nil {
		return nil, err
	}
	n.sessions[peer.NodeID] = s
	return s, nil
}

func (n *Node) inboundLoop() {
	for {
		select {
		case <-n.stopCh:
			return
		default:
		}
		n.mu.Lock()
		link := n.link
		n.mu.Unlock()
		if link == nil {
			if !n.attemptReconnect() {
				return
			}
			continue
		}
		frame, err := link.Recv(context.Background())
		if err != nil {
			select {
			case <-n.stopCh:
				return
			default:
			}
			// Link dropped. Clear it and let the next iteration reconnect.
			n.mu.Lock()
			if n.link == link {
				n.link = nil
			}
			n.mu.Unlock()
			link.Close()
			continue
		}
		n.handleInbound(frame)
	}
}

// attemptReconnect redials the relay with exponential backoff. Returns true
// once a new link is established, false if Stop was called while retrying.
// This is what keeps a long-running daemon alive through transient network
// drops, laptop sleep/wake, relay restarts, etc. — without it, the very
// first websocket failure silently bricks the node.
func (n *Node) attemptReconnect() bool {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		select {
		case <-n.stopCh:
			return false
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		link, err := n.transport.Connect(ctx, n.identity)
		cancel()
		if err == nil {
			n.mu.Lock()
			n.link = link
			n.mu.Unlock()
			return true
		}
		select {
		case <-n.stopCh:
			return false
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (n *Node) handleInbound(frame transport.Frame) {
	ctx := context.Background()
	peer, err := n.store.GetPeer(ctx, frame.From)
	if err != nil {
		return // unknown peer; drop
	}
	if peer.Trust == pairing.TrustRevoked {
		return
	}
	sess, err := n.sessionFor(peer)
	if err != nil {
		return
	}
	pt, err := sess.Open(frame.Data)
	if err != nil {
		return
	}
	env, err := envelope.Unmarshal(pt)
	if err != nil {
		return
	}
	if env.From.NodeID != peer.NodeID {
		return // envelope claims a different signer than the transport source
	}
	if err := envelope.Verify(env); err != nil {
		return
	}
	// Drop envelopes for threads we don't know about, unless the peer itself
	// is opening a new thread (v0 simplification: creator opens).
	thread, err := n.store.GetThread(ctx, env.ThreadID)
	if err != nil {
		t := store.Thread{ID: env.ThreadID, PeerID: peer.NodeID, Topic: "", CreatedMs: env.CreatedAtMs}
		if err := n.store.CreateThread(ctx, t); err != nil {
			return
		}
		thread = t
	}
	if thread.PeerID != peer.NodeID {
		return
	}

	decision := n.policy.Evaluate(env, peer)
	if decision == policy.Deny {
		return
	}
	if decision == policy.Downgrade && env.Intent == envelope.IntentAskHuman {
		env.Intent = envelope.IntentNotifyHuman
	}

	if err := n.store.AppendEnvelope(ctx, env, true); err != nil {
		return
	}
	n.publish(env)
	n.route(ctx, env)
}

func (n *Node) route(ctx context.Context, env envelope.Envelope) {
	human, agent := n.currentSurfaces()
	switch env.Intent {
	case envelope.IntentNotifyHuman:
		_ = human.Notify(ctx, env.ThreadID, env)
	case envelope.IntentAskHuman:
		go n.handleAskHuman(env)
	case envelope.IntentHandoff:
		_ = human.PresentThread(ctx, env.ThreadID)
	default:
		_ = agent.OnMessage(ctx, env)
	}
}

func (n *Node) handleAskHuman(env envelope.Envelope) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	reply, err := n.currentHuman().Ask(ctx, env.ThreadID, env)
	if err != nil {
		if errors.Is(err, surface.ErrAsyncReply) {
			return
		}
		return
	}
	_ = n.SubmitHumanReply(ctx, env.ThreadID, reply)
}

func (n *Node) drainOutboxLoop() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-n.stopCh:
			return
		case <-t.C:
			n.tryDrainOutbox()
		}
	}
}

func (n *Node) tryDrainOutbox() {
	ctx := context.Background()
	peers, err := n.store.ListPeers(ctx)
	if err != nil {
		return
	}
	for _, peer := range peers {
		if peer.Trust == pairing.TrustRevoked {
			continue
		}
		entries, err := n.store.ListOutbox(ctx, peer.NodeID)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			delivered, _ := n.deliver(ctx, peer, entry.Envelope)
			if !delivered {
				break
			}
			if err := n.store.DeleteOutbox(ctx, entry.ID); err != nil {
				break
			}
		}
	}
}

// Subscribe returns a channel of envelopes appended to thread and a cancel
// function that removes the subscription.
func (n *Node) Subscribe(thread envelope.ThreadID) (<-chan envelope.Envelope, func()) {
	ch := make(chan envelope.Envelope, 16)
	sub := &subscription{thread: thread, ch: ch}
	n.mu.Lock()
	m, ok := n.subs[thread]
	if !ok {
		m = make(map[*subscription]struct{})
		n.subs[thread] = m
	}
	m[sub] = struct{}{}
	n.mu.Unlock()
	cancel := func() {
		n.mu.Lock()
		if m, ok := n.subs[thread]; ok {
			delete(m, sub)
			if len(m) == 0 {
				delete(n.subs, thread)
			}
		}
		n.mu.Unlock()
		close(ch)
	}
	return ch, cancel
}

func (n *Node) publish(env envelope.Envelope) {
	n.mu.Lock()
	m := n.subs[env.ThreadID]
	subs := make([]*subscription, 0, len(m))
	for s := range m {
		subs = append(subs, s)
	}
	n.mu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- env:
		default:
		}
	}
}

// newULID builds a time-prefixed 16-byte identifier: 48-bit ms timestamp
// big-endian, 80 random bits.
func newULID() envelope.ULID {
	var u envelope.ULID
	ts := time.Now().UnixMilli()
	u[0] = byte(ts >> 40)
	u[1] = byte(ts >> 32)
	u[2] = byte(ts >> 24)
	u[3] = byte(ts >> 16)
	u[4] = byte(ts >> 8)
	u[5] = byte(ts)
	_, _ = rand.Read(u[6:])
	return u
}

func (n *Node) currentSurfaces() (surface.HumanSurface, surface.AgentSurface) {
	n.mu.Lock()
	human := n.human
	agent := n.agent
	n.mu.Unlock()
	return human, agent
}

func (n *Node) currentHuman() surface.HumanSurface {
	n.mu.Lock()
	human := n.human
	n.mu.Unlock()
	return human
}
