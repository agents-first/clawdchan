package node

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/agents-first/clawdchan/core/envelope"
	"github.com/agents-first/clawdchan/core/identity"
	"github.com/agents-first/clawdchan/core/store"
)

const (
	CollabStatusActive    = "active"
	CollabStatusWaiting   = "waiting"
	CollabStatusConverged = "converged"
	CollabStatusTimedOut  = "timed_out"
	CollabStatusCancelled = "cancelled"
	CollabStatusClosed    = "closed"
)

var ErrCollabLeaseHeld = errors.New("collab session lease held by another owner")

type CollabCreateOptions struct {
	PeerID           identity.NodeID
	ThreadID         envelope.ThreadID
	Topic            string
	LastCursor       string
	MaxRounds        int
	DefinitionOfDone string
	OwnerID          string
	LeaseDuration    time.Duration
}

func (n *Node) CreateCollabSession(ctx context.Context, opts CollabCreateOptions) (store.CollabSession, error) {
	if opts.OwnerID == "" {
		opts.OwnerID = newCollabID("owner")
	}
	now := time.Now().UnixMilli()
	leaseExpires := now
	if opts.LeaseDuration > 0 {
		leaseExpires = now + opts.LeaseDuration.Milliseconds()
	}
	cs := store.CollabSession{
		SessionID:        newCollabID("collab"),
		PeerID:           opts.PeerID,
		ThreadID:         opts.ThreadID,
		Topic:            opts.Topic,
		Status:           CollabStatusActive,
		LastCursor:       opts.LastCursor,
		MaxRounds:        opts.MaxRounds,
		DefinitionOfDone: opts.DefinitionOfDone,
		OwnerID:          opts.OwnerID,
		HeartbeatMs:      now,
		LeaseExpiresMs:   leaseExpires,
		CreatedMs:        now,
		UpdatedMs:        now,
		LastActivityMs:   now,
	}
	if err := n.store.CreateCollabSession(ctx, cs); err != nil {
		return store.CollabSession{}, err
	}
	return cs, nil
}

func (n *Node) GetCollabSession(ctx context.Context, sessionID string) (store.CollabSession, error) {
	return n.store.GetCollabSession(ctx, sessionID)
}

func (n *Node) ListCollabSessions(ctx context.Context, activeOnly bool) ([]store.CollabSession, error) {
	return n.store.ListCollabSessions(ctx, activeOnly)
}

func (n *Node) HeartbeatCollabSession(ctx context.Context, sessionID, ownerID string, leaseDuration time.Duration) (store.CollabSession, error) {
	cs, err := n.store.GetCollabSession(ctx, sessionID)
	if err != nil {
		return store.CollabSession{}, err
	}
	if ownerID == "" {
		ownerID = cs.OwnerID
	}
	now := time.Now().UnixMilli()
	if cs.OwnerID != "" && cs.OwnerID != ownerID && cs.LeaseExpiresMs > now {
		return store.CollabSession{}, fmt.Errorf("%w: owner=%s lease_expires_ms=%d", ErrCollabLeaseHeld, cs.OwnerID, cs.LeaseExpiresMs)
	}
	cs.OwnerID = ownerID
	cs.HeartbeatMs = now
	cs.LeaseExpiresMs = now
	if leaseDuration > 0 {
		cs.LeaseExpiresMs = now + leaseDuration.Milliseconds()
	}
	cs.UpdatedMs = now
	if err := n.store.UpdateCollabSession(ctx, cs); err != nil {
		return store.CollabSession{}, err
	}
	return cs, nil
}

func (n *Node) UpdateCollabSession(ctx context.Context, cs store.CollabSession) error {
	cs.UpdatedMs = time.Now().UnixMilli()
	return n.store.UpdateCollabSession(ctx, cs)
}

func (n *Node) UpdateCollabCursor(ctx context.Context, sessionID, cursor, status string, activity bool) (store.CollabSession, error) {
	cs, err := n.store.GetCollabSession(ctx, sessionID)
	if err != nil {
		return store.CollabSession{}, err
	}
	now := time.Now().UnixMilli()
	cs.LastCursor = cursor
	if status != "" {
		cs.Status = status
	}
	cs.UpdatedMs = now
	if activity {
		cs.LastActivityMs = now
	}
	if err := n.store.UpdateCollabSession(ctx, cs); err != nil {
		return store.CollabSession{}, err
	}
	return cs, nil
}

func (n *Node) CloseCollabSession(ctx context.Context, sessionID, status, summary, reason string) error {
	if status == "" {
		status = CollabStatusClosed
	}
	return n.store.CloseCollabSession(ctx, sessionID, status, summary, reason, time.Now().UnixMilli())
}

func newCollabID(prefix string) string {
	id := newULID()
	return prefix + "-" + hex.EncodeToString(id[:])
}
