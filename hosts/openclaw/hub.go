package openclaw

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/pairing"
	"github.com/vMaroon/ClawdChan/core/store"
)

const (
	hubSessionName       = "clawdchan:hub"
	hubCreateTimeout     = 10 * time.Second
	hubPairTimeout       = 5 * time.Minute
	hubRecentWindowMs    = int64(24 * 60 * 60 * 1000)
	hubSnippetMaxRunes   = 120
	hubBackgroundSendTTL = 10 * time.Second
)

type Hub struct {
	n  *node.Node
	br *Bridge
	sm *SessionMap

	mu     sync.RWMutex
	sid    string
	runCtx context.Context
}

func NewHub(n *node.Node, br *Bridge, sm *SessionMap) *Hub {
	return &Hub{
		n:  n,
		br: br,
		sm: sm,
	}
}

func (h *Hub) Start(ctx context.Context) error {
	if h == nil || h.n == nil || h.br == nil || h.sm == nil {
		return fmt.Errorf("openclaw: hub missing dependency")
	}

	h.mu.Lock()
	h.runCtx = ctx
	h.mu.Unlock()

	createCtx, cancel := context.WithTimeout(ctx, hubCreateTimeout)
	sid, err := h.br.SessionCreate(createCtx, hubSessionName)
	cancel()
	if err != nil {
		log.Printf("openclaw: hub session create failed: %v", err)
		return err
	}
	h.setHubSessionID(sid)

	peers, _ := h.n.ListPeers(ctx)
	if err := h.br.SessionsSend(ctx, sid, HubContext(h.n.Alias(), peers)); err != nil {
		return err
	}

	msgs, err := h.br.Subscribe(ctx, sid)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-msgs:
			if !ok {
				log.Printf("openclaw: hub subscription closed sid=%q", sid)
				return nil
			}
			if msg.Role != "assistant" {
				continue
			}
			actions := ParseActions(msg.Text)
			for _, action := range actions {
				result := h.executeAction(ctx, action)
				if strings.TrimSpace(result) == "" {
					continue
				}
				if err := h.br.SessionsSend(ctx, sid, result); err != nil {
					log.Printf("openclaw: hub sessions.send sid=%q failed: %v", sid, err)
				}
			}
		}
	}
}

func (h *Hub) HubSessionID() string {
	if h == nil {
		return ""
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sid
}

func (h *Hub) setHubSessionID(sid string) {
	h.mu.Lock()
	h.sid = sid
	h.mu.Unlock()
}

func (h *Hub) executeAction(ctx context.Context, action Action) string {
	switch action.Kind {
	case ActionPair:
		pairCtx, cancel := context.WithTimeout(context.Background(), hubPairTimeout)
		code, ch, err := h.n.Pair(pairCtx)
		if err != nil {
			cancel()
			return "ClawdChan pair failed: " + err.Error()
		}
		go h.awaitPairResult(pairCtx, cancel, ch)
		return "Here's your pairing code — give these 12 words to your peer:\n\n" + code.Mnemonic() +
			"\n\nThey paste them to their ClawdChan agent. I'll let you know here as soon as you're connected."
	case ActionConsume:
		peer, err := h.n.Consume(ctx, action.Words)
		if err != nil {
			return "ClawdChan consume failed: " + err.Error()
		}
		h.establishPeerSession(peer)
		return fmt.Sprintf(
			"Paired with %s ✓\nSAS: %s\nVerify the SAS matches on both sides before sharing anything sensitive. A new session with %s is now open.",
			peerDisplay(peer),
			strings.Join(peer.SAS[:], "-"),
			peerDisplay(peer),
		)
	case ActionPeers:
		peers, _ := h.n.ListPeers(ctx)
		if len(peers) == 0 {
			return "No paired peers yet."
		}
		lines := make([]string, 0, len(peers))
		for _, p := range peers {
			lines = append(lines, fmt.Sprintf("%s (%s): %s", peerDisplay(p), shortNodeID(p.NodeID), trustLabelHub(p)))
		}
		return "Paired peers:\n" + strings.Join(lines, "\n")
	case ActionInbox:
		threads, _ := h.n.ListThreads(ctx)
		if len(threads) == 0 {
			return "No recent messages."
		}

		sinceMs := time.Now().UnixMilli() - hubRecentWindowMs
		latestByPeer := make(map[identity.NodeID]envelope.Envelope)

		for _, th := range threads {
			envs, err := h.n.ListEnvelopes(ctx, th.ID, sinceMs)
			if err != nil {
				continue
			}
			for _, env := range envs {
				cur, ok := latestByPeer[th.PeerID]
				if !ok || env.CreatedAtMs > cur.CreatedAtMs {
					latestByPeer[th.PeerID] = env
				}
			}
		}
		if len(latestByPeer) == 0 {
			return "No recent messages."
		}

		type row struct {
			peer    pairing.Peer
			created int64
			snippet string
		}
		rows := make([]row, 0, len(latestByPeer))
		for peerID, env := range latestByPeer {
			peer, err := h.n.Store().GetPeer(ctx, peerID)
			if err != nil {
				peer = pairing.Peer{NodeID: peerID}
			}
			rows = append(rows, row{
				peer:    peer,
				created: env.CreatedAtMs,
				snippet: trimSnippet(contentBody(env.Content)),
			})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].created > rows[j].created })

		lines := make([]string, 0, len(rows))
		for _, r := range rows {
			lines = append(lines, fmt.Sprintf("%s: %s", peerDisplay(r.peer), r.snippet))
		}
		return "Recent messages (24h):\n" + strings.Join(lines, "\n")
	default:
		return ""
	}
}

func (h *Hub) awaitPairResult(pairCtx context.Context, cancel context.CancelFunc, ch <-chan node.PairResult) {
	defer cancel()

	var msg string
	select {
	case <-pairCtx.Done():
		msg = "ClawdChan pair failed: " + pairCtx.Err().Error()
	case res, ok := <-ch:
		if !ok {
			msg = "ClawdChan pair failed: pair channel closed unexpectedly"
		} else if res.Err != nil {
			msg = "ClawdChan pair failed: " + res.Err.Error()
		} else {
			h.establishPeerSession(res.Peer)
			msg = fmt.Sprintf(
				"Paired with %s ✓\nSAS: %s\nVerify the SAS matches on both sides before sharing anything sensitive. A new session with %s is now open.",
				peerDisplay(res.Peer),
				strings.Join(res.Peer.SAS[:], "-"),
				peerDisplay(res.Peer),
			)
		}
	}

	sendCtx, sendCancel := context.WithTimeout(context.Background(), hubBackgroundSendTTL)
	defer sendCancel()
	if err := h.br.SessionsSend(sendCtx, h.HubSessionID(), msg); err != nil {
		log.Printf("openclaw: hub pair completion send failed: %v", err)
	}
}

// establishPeerSession ensures an OpenClaw session exists for peer, seeds it
// with PeerContext, and starts a subscriber tied to the hub's long-lived run
// context so it outlives short-scoped action contexts (e.g. pair timeout).
func (h *Hub) establishPeerSession(peer pairing.Peer) {
	h.mu.RLock()
	runCtx := h.runCtx
	h.mu.RUnlock()
	if runCtx == nil {
		runCtx = context.Background()
	}

	ctx, cancel := context.WithTimeout(runCtx, hubCreateTimeout)
	defer cancel()

	sid, err := h.sm.EnsureSessionFor(ctx, peer.NodeID)
	if err != nil {
		log.Printf("openclaw: ensure peer session for %x failed: %v", peer.NodeID[:8], err)
		return
	}
	if err := h.br.SessionsSend(ctx, sid, PeerContext(h.n.Alias(), peer.Alias)); err != nil {
		log.Printf("openclaw: send peer context for %x failed: %v", peer.NodeID[:8], err)
	}
	thread, ok := findThreadForPeer(ctx, h.n, peer.NodeID)
	if !ok {
		return
	}
	go h.br.RunSubscriber(runCtx, sid, h.n, thread.ID)
}

func findThreadForPeer(ctx context.Context, n *node.Node, nodeID identity.NodeID) (store.Thread, bool) {
	threads, err := n.ListThreads(ctx)
	if err != nil {
		return store.Thread{}, false
	}
	var latest store.Thread
	found := false
	for _, th := range threads {
		if th.PeerID != nodeID {
			continue
		}
		if !found || th.CreatedMs > latest.CreatedMs {
			latest = th
			found = true
		}
	}
	return latest, found
}

func trustLabelHub(p pairing.Peer) string {
	switch p.Trust {
	case pairing.TrustPaired:
		return "paired"
	case pairing.TrustBridged:
		return "bridged"
	case pairing.TrustRevoked:
		return "revoked"
	default:
		return "unknown"
	}
}

func trimSnippet(in string) string {
	s := strings.TrimSpace(strings.Join(strings.Fields(in), " "))
	if s == "" {
		return "(no text)"
	}
	r := []rune(s)
	if len(r) <= hubSnippetMaxRunes {
		return s
	}
	if hubSnippetMaxRunes <= 3 {
		return "..."
	}
	return string(r[:hubSnippetMaxRunes-3]) + "..."
}

func peerDisplay(p pairing.Peer) string {
	if alias := strings.TrimSpace(p.Alias); alias != "" {
		return alias
	}
	return shortNodeID(p.NodeID)
}
