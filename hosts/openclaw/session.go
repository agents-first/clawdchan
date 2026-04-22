package openclaw

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/agents-first/clawdchan/core/envelope"
	"github.com/agents-first/clawdchan/core/identity"
	"github.com/agents-first/clawdchan/core/store"
)

type sessionBridge interface {
	SessionCreate(ctx context.Context, name string) (string, error)
}

// SessionMap maps a peer node ID to one OpenClaw session ID.
type SessionMap struct {
	store store.Store
	br    sessionBridge

	mu    sync.RWMutex
	cache map[identity.NodeID]string
}

func NewSessionMap(br sessionBridge, st store.Store) *SessionMap {
	return &SessionMap{
		store: st,
		br:    br,
		cache: make(map[identity.NodeID]string),
	}
}

func (m *SessionMap) EnsureSessionFor(ctx context.Context, nodeID identity.NodeID) (string, error) {
	if m == nil {
		return "", errors.New("openclaw: nil session map")
	}

	m.mu.RLock()
	sid, ok := m.cache[nodeID]
	m.mu.RUnlock()
	if ok {
		return sid, nil
	}

	if m.store == nil {
		return "", errors.New("openclaw: session map missing store")
	}
	if m.br == nil {
		return "", errors.New("openclaw: session map missing bridge")
	}

	sid, ok, err := m.store.GetOpenClawSession(ctx, nodeID)
	if err != nil {
		return "", fmt.Errorf("get openclaw session: %w", err)
	}
	if ok {
		m.cacheSet(nodeID, sid)
		return sid, nil
	}

	name := "clawdchan:" + hex.EncodeToString(nodeID[:])[:8]
	sid, err = m.br.SessionCreate(ctx, name)
	if err != nil {
		return "", err
	}
	if err := m.store.SetOpenClawSession(ctx, nodeID, sid); err != nil {
		return "", fmt.Errorf("set openclaw session: %w", err)
	}

	m.cacheSet(nodeID, sid)
	return sid, nil
}

func (m *SessionMap) EnsureSessionForThread(ctx context.Context, thread envelope.ThreadID) (string, error) {
	if m == nil {
		return "", errors.New("openclaw: nil session map")
	}
	if m.store == nil {
		return "", errors.New("openclaw: session map missing store")
	}
	t, err := m.store.GetThread(ctx, thread)
	if err != nil {
		return "", err
	}
	return m.EnsureSessionFor(ctx, t.PeerID)
}

func (m *SessionMap) cacheSet(nodeID identity.NodeID, sid string) {
	m.mu.Lock()
	if m.cache == nil {
		m.cache = make(map[identity.NodeID]string)
	}
	m.cache[nodeID] = sid
	m.mu.Unlock()
}
