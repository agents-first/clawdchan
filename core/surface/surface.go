// Package surface defines the two host-implemented interfaces that keep the
// core host-agnostic: HumanSurface and AgentSurface. The core imports nothing
// Claude- or OpenClaw-specific; host bindings live under hosts/.
package surface

import (
	"context"
	"errors"

	"github.com/agents-first/ClawdChan/core/envelope"
)

// Reachability advertises, at handshake time, how the peer's human can be
// reached. See docs/design.md § Handshake and § Human surface contract.
type Reachability int

const (
	ReachableSync  Reachability = 1 // human is likely present now
	ReachableAsync Reachability = 2 // reachable via push / messenger
	Unreachable    Reachability = 3
)

// HumanSurface is implemented by each host (Claude Code plugin, OpenClaw
// plugin, ...). The core drives it; the host decides the UX.
type HumanSurface interface {
	Notify(ctx context.Context, thread envelope.ThreadID, env envelope.Envelope) error
	Ask(ctx context.Context, thread envelope.ThreadID, env envelope.Envelope) (envelope.Content, error)
	Reachability() Reachability
	PresentThread(ctx context.Context, thread envelope.ThreadID) error
}

// ErrAsyncReply is returned by HumanSurface.Ask when the ask has been
// delivered to an async surface (OpenClaw session, CC inbox, messenger
// gateway, ...) and the reply will arrive later via SubmitHumanReply.
// The core treats this as success — no auto-reply is generated.
var ErrAsyncReply = errors.New("surface: ask delivered; reply is async")

// AgentSurface is the reverse direction: the core delivers agent-facing
// envelopes here.
type AgentSurface interface {
	OnMessage(ctx context.Context, env envelope.Envelope) error
}

// NopHuman is a HumanSurface that ignores notifies, refuses asks, and
// advertises Unreachable. Useful for relay-only nodes and tests.
type NopHuman struct{}

func (NopHuman) Notify(context.Context, envelope.ThreadID, envelope.Envelope) error { return nil }
func (NopHuman) Ask(context.Context, envelope.ThreadID, envelope.Envelope) (envelope.Content, error) {
	return envelope.Content{}, errors.New("surface: no human available")
}
func (NopHuman) Reachability() Reachability                             { return Unreachable }
func (NopHuman) PresentThread(context.Context, envelope.ThreadID) error { return nil }

// NopAgent ignores all inbound messages.
type NopAgent struct{}

func (NopAgent) OnMessage(context.Context, envelope.Envelope) error { return nil }
