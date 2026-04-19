package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/notify"
	"github.com/vMaroon/ClawdChan/core/pairing"
	"github.com/vMaroon/ClawdChan/core/policy"
)

// claimDispatch is a per-peer one-in-flight lock. Returns true if the
// caller owns the dispatch slot and must release it with releaseDispatch
// when done. Used to guarantee we never run two subprocesses for one
// peer at the same time — an honest sub-agent loop shouldn't produce
// overlapping asks, but relay retries or client bugs shouldn't force us
// to fan out parallel Claude invocations either.
func (d *daemonSurface) claimDispatch(peer identity.NodeID) bool {
	d.inFlightMu.Lock()
	defer d.inFlightMu.Unlock()
	if d.inFlight == nil {
		d.inFlight = map[identity.NodeID]bool{}
	}
	if d.inFlight[peer] {
		return false
	}
	d.inFlight[peer] = true
	return true
}

func (d *daemonSurface) releaseDispatch(peer identity.NodeID) {
	d.inFlightMu.Lock()
	delete(d.inFlight, peer)
	d.inFlightMu.Unlock()
}

// runCollabDispatch runs the configured subprocess for one collab-sync
// ask and routes the answer (or a decline) back to the peer as a normal
// envelope. On dispatch error or decline we fall back to firing the OS
// toast so the local human still learns something happened.
func (d *daemonSurface) runCollabDispatch(tid envelope.ThreadID, env envelope.Envelope) {
	defer d.releaseDispatch(env.From.NodeID)

	ctx, cancel := context.WithTimeout(context.Background(), d.dispatchTimeout()+30*time.Second)
	defer cancel()

	req, err := d.buildDispatchRequest(ctx, tid, env)
	if err != nil {
		if d.verbose {
			fmt.Fprintf(os.Stderr, "[dispatch] build request failed: %v\n", err)
		}
		d.fallbackToToast(tid, env, fmt.Sprintf("dispatch setup failed: %v", err))
		return
	}

	if d.verbose {
		fmt.Fprintf(os.Stderr, "[dispatch] invoking agent for peer=%s rounds=%d\n", env.From.Alias, req.Policy.CollabRounds)
	}
	outcome, err := d.dispatcher.Dispatch(ctx, req)
	if err != nil {
		if d.verbose {
			fmt.Fprintf(os.Stderr, "[dispatch] dispatcher error: %v\n", err)
		}
		d.fallbackToToast(tid, env, fmt.Sprintf("dispatcher error: %v", err))
		return
	}

	if outcome.Declined {
		if d.verbose {
			fmt.Fprintf(os.Stderr, "[dispatch] declined: %s\n", outcome.DeclineReason)
		}
		d.sendDispatchDecline(ctx, tid, outcome.DeclineReason)
		d.fallbackToToast(tid, env, "dispatch declined — human engagement needed")
		return
	}

	// Successful dispatch: send the subprocess's answer back as a normal
	// envelope. If the subprocess asked for another collab round, mark
	// the outbound envelope as collab-sync so the remote sub-agent keeps
	// iterating.
	intent := envelope.IntentAsk
	if outcome.Intent == "say" {
		intent = envelope.IntentSay
	}
	content := envelope.Content{Kind: envelope.ContentText, Text: outcome.Reply}
	if outcome.Collab {
		content = envelope.Content{Kind: envelope.ContentDigest, Title: policy.CollabSyncTitle, Body: outcome.Reply}
	}
	if err := d.node.Send(ctx, tid, intent, content); err != nil {
		if d.verbose {
			fmt.Fprintf(os.Stderr, "[dispatch] send reply failed: %v\n", err)
		}
		d.fallbackToToast(tid, env, fmt.Sprintf("send reply failed: %v", err))
		return
	}
	if d.verbose {
		fmt.Fprintf(os.Stderr, "[dispatch] replied (collab=%v intent=%s len=%d)\n", outcome.Collab, outcome.Intent, len(outcome.Reply))
	}
}

func (d *daemonSurface) dispatchTimeout() time.Duration {
	if d.dispatchCfg == nil || d.dispatchCfg.TimeoutSeconds <= 0 {
		return policy.DefaultTimeout
	}
	return time.Duration(d.dispatchCfg.TimeoutSeconds) * time.Second
}

// buildDispatchRequest assembles the JSON payload that will be written
// to the subprocess's stdin. ThreadContext is bounded by
// config.MaxContext (default 20) — enough for a subprocess to reason
// about the exchange without bloating the prompt.
func (d *daemonSurface) buildDispatchRequest(ctx context.Context, tid envelope.ThreadID, ask envelope.Envelope) (policy.DispatchRequest, error) {
	peer, err := d.node.GetPeer(ctx, ask.From.NodeID)
	if err != nil {
		return policy.DispatchRequest{}, err
	}
	envs, err := d.node.ListEnvelopes(ctx, tid, 0)
	if err != nil {
		return policy.DispatchRequest{}, err
	}

	maxCtx := 20
	if d.dispatchCfg != nil && d.dispatchCfg.MaxContext > 0 {
		maxCtx = d.dispatchCfg.MaxContext
	}
	me := d.node.Identity()
	rounds := countCollabRounds(envs, policy.DefaultMaxCollabRounds*2+1)
	peerAlias := ask.From.Alias
	if peer.Alias != "" {
		peerAlias = peer.Alias
	}

	// Keep the tail of the thread so the subprocess sees the running
	// exchange, not ancient chatter. The ask itself is excluded from
	// ThreadContext because it's in req.Ask explicitly.
	tail := envs
	if len(tail) > maxCtx {
		tail = tail[len(tail)-maxCtx:]
	}
	thread := make([]policy.DispatchEnvelope, 0, len(tail))
	for _, e := range tail {
		if e.EnvelopeID == ask.EnvelopeID {
			continue
		}
		thread = append(thread, serializeDispatchEnvelope(e, me))
	}

	req := policy.DispatchRequest{
		Ask:           serializeDispatchEnvelope(ask, me),
		ThreadContext: thread,
		Peer: policy.DispatchPeer{
			NodeID:         hex.EncodeToString(peer.NodeID[:]),
			Alias:          peerAlias,
			Trust:          trustLabel(peer),
			HumanReachable: peer.HumanReachable,
		},
		Self: policy.DispatchSelf{
			NodeID: hex.EncodeToString(me[:]),
			Alias:  d.node.Alias(),
		},
		Policy: policy.DispatchPolicyHints{
			CollabRounds: rounds,
		},
	}
	return req, nil
}

// countCollabRounds counts envelopes on the thread that are marked with
// the collab-sync title, capped at maxCount so scans of long threads
// remain bounded. The caller uses this as a hop counter to let the
// dispatcher decline before runaway loops.
func countCollabRounds(envs []envelope.Envelope, maxCount int) int {
	n := 0
	for _, e := range envs {
		if e.Content.Kind == envelope.ContentDigest && e.Content.Title == policy.CollabSyncTitle {
			n++
			if n >= maxCount {
				return n
			}
		}
	}
	return n
}

func serializeDispatchEnvelope(e envelope.Envelope, me identity.NodeID) policy.DispatchEnvelope {
	dir := "in"
	if e.From.NodeID == me {
		dir = "out"
	}
	kind := "text"
	if e.Content.Kind == envelope.ContentDigest {
		kind = "digest"
	}
	role := "agent"
	if e.From.Role == envelope.RoleHuman {
		role = "human"
	}
	collab := e.Content.Kind == envelope.ContentDigest && e.Content.Title == policy.CollabSyncTitle
	return policy.DispatchEnvelope{
		EnvelopeID:  hex.EncodeToString(e.EnvelopeID[:]),
		ThreadID:    hex.EncodeToString(e.ThreadID[:]),
		FromNode:    hex.EncodeToString(e.From.NodeID[:]),
		FromAlias:   e.From.Alias,
		FromRole:    role,
		Intent:      intentName(e.Intent),
		CreatedAtMs: e.CreatedAtMs,
		Kind:        kind,
		Text:        e.Content.Text,
		Title:       e.Content.Title,
		Body:        e.Content.Body,
		Direction:   dir,
		Collab:      collab,
	}
}

func trustLabel(p pairing.Peer) string {
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

// sendDispatchDecline posts a plain-text reply to close the collab loop
// gracefully. The sender's sub-agent is still in a `clawdchan_await`
// cycle — without this nudge it would burn its whole timeout waiting
// for an answer that will never come.
func (d *daemonSurface) sendDispatchDecline(ctx context.Context, tid envelope.ThreadID, reason string) {
	if reason == "" {
		reason = "dispatch declined"
	}
	msg := "[collab-dispatch declined] " + reason
	if err := d.node.Send(ctx, tid, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: msg}); err != nil && d.verbose {
		fmt.Fprintf(os.Stderr, "[dispatch] send decline failed: %v\n", err)
	}
}

// fallbackToToast runs the classic toast path for an envelope that the
// dispatcher couldn't handle. Kept separate from the main dispatch()
// body so the happy path doesn't have to thread "skip-toast" booleans.
func (d *daemonSurface) fallbackToToast(tid envelope.ThreadID, env envelope.Envelope, note string) {
	if d.verbose && note != "" {
		fmt.Fprintf(os.Stderr, "[dispatch] fallback toast: %s\n", note)
	}
	if _, active := d.inActiveSession(tid, env.From.NodeID, env.CreatedAtMs); active {
		return
	}
	if !d.claimNotify(env.From.NodeID) {
		return
	}
	alias := d.resolveAlias(env)
	msg := notificationCopy(alias, env.Intent, env.Content, d.isNewSession(tid))
	msg.ActivateApp = preferredActivateBundle()
	if err := notify.Dispatch(msg); err != nil {
		fmt.Fprintf(os.Stderr, "notify: %v\n", err)
	}
}
