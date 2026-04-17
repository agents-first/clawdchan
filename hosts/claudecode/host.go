// Package claudecode is the Claude Code host binding for ClawdChan.
//
// It embeds the core as a library and exposes it to a Claude Code session as
// an MCP server with tools for pair / consume / send / poll / etc. The host's
// HumanSurface does not block on Ask — the remote peer's AskHuman envelope is
// stored and surfaced to Claude via the clawdchan_pending_asks tool. Claude
// then asks the user in-session and calls clawdchan_submit_human_reply.
package claudecode

import (
	"context"
	"errors"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/surface"
)

// HumanSurface is the CC-host implementation of surface.HumanSurface. All
// notifications and asks flow through the store; the CC session reads them
// via MCP tools.
type HumanSurface struct{}

func (HumanSurface) Notify(context.Context, envelope.ThreadID, envelope.Envelope) error {
	return nil // stored by the node; Claude surfaces via clawdchan_poll/pending
}

func (HumanSurface) Ask(context.Context, envelope.ThreadID, envelope.Envelope) (envelope.Content, error) {
	// Return error so the node does not auto-reply. The envelope stays in
	// the store; Claude picks it up via clawdchan_pending_asks and calls
	// clawdchan_submit_human_reply once the user has answered in-session.
	return envelope.Content{}, errors.New("claudecode: AskHuman surfaces asynchronously via MCP tools")
}

func (HumanSurface) Reachability() surface.Reachability { return surface.ReachableSync }

func (HumanSurface) PresentThread(context.Context, envelope.ThreadID) error { return nil }

// AgentSurface is a no-op; Claude consumes envelopes via the clawdchan_poll
// tool instead of a callback.
type AgentSurface struct{}

func (AgentSurface) OnMessage(context.Context, envelope.Envelope) error { return nil }
