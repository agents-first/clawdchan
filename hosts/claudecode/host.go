// Package claudecode is the Claude Code host binding for ClawdChan.
//
// It embeds the core as a library and exposes it to a Claude Code session
// as a peer-centric MCP server. The tool surface is deliberately small —
// clawdchan_toolkit / clawdchan_pair / clawdchan_message / clawdchan_inbox —
// with the full handler set living in the hosts package and this package
// acting as a thin adapter to mark3labs/mcp-go.
//
// The host's HumanSurface does not block on Ask — the remote peer's
// AskHuman envelope is stored and surfaced to Claude via the pending_asks
// field of clawdchan_inbox. Claude then asks the user in-session and calls
// clawdchan_message with as_human=true and the user's literal answer.
package claudecode

import (
	"context"

	"github.com/agents-first/ClawdChan/core/envelope"
	"github.com/agents-first/ClawdChan/core/surface"
)

// HumanSurface is the CC-host implementation of surface.HumanSurface. All
// notifications and asks flow through the store; the CC session reads them
// via MCP tools.
type HumanSurface struct{}

func (HumanSurface) Notify(context.Context, envelope.ThreadID, envelope.Envelope) error {
	return nil // stored by the node; Claude surfaces via clawdchan_inbox
}

func (HumanSurface) Ask(context.Context, envelope.ThreadID, envelope.Envelope) (envelope.Content, error) {
	// Signal async delivery: the envelope stays in the store; Claude picks
	// it up via clawdchan_inbox and calls clawdchan_message with
	// as_human=true once the user answers in-session.
	return envelope.Content{}, surface.ErrAsyncReply
}

func (HumanSurface) Reachability() surface.Reachability { return surface.ReachableSync }

func (HumanSurface) PresentThread(context.Context, envelope.ThreadID) error { return nil }

// AgentSurface is a no-op; Claude consumes envelopes via the clawdchan_inbox
// tool instead of a callback.
type AgentSurface struct{}

func (AgentSurface) OnMessage(context.Context, envelope.Envelope) error { return nil }
