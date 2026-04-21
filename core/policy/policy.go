// Package policy is the small local gate applied before invoking the
// HumanSurface. It prevents a remote agent from dictating how or when the
// local human is interrupted.
package policy

import (
	"github.com/agents-first/ClawdChan/core/envelope"
	"github.com/agents-first/ClawdChan/core/identity"
	"github.com/agents-first/ClawdChan/core/pairing"
)

// Decision is the policy output for an incoming envelope.
type Decision uint8

const (
	// Allow delivers the envelope as intended.
	Allow Decision = 1
	// Downgrade turns an AskHuman into a NotifyHuman (stored, not blocking).
	Downgrade Decision = 2
	// Deny drops the envelope without side effects.
	Deny Decision = 3
)

// Config is the user-editable policy.
type Config struct {
	// AskHumanAllowlist limits which peers can trigger AskHuman; nil allows
	// all paired peers. Revoked peers are always denied regardless.
	AskHumanAllowlist map[identity.NodeID]bool
	// DefaultAskBehavior is applied when a peer is not in AskHumanAllowlist.
	// Zero value defaults to Downgrade.
	DefaultAskBehavior Decision
}

// Engine evaluates Decisions for incoming envelopes.
type Engine interface {
	Evaluate(env envelope.Envelope, peer pairing.Peer) Decision
}

type engine struct{ cfg Config }

// New returns an Engine that applies cfg.
func New(cfg Config) Engine { return &engine{cfg: cfg} }

// Default is a permissive engine useful for tests and initial deployments.
// It allows all envelopes from non-revoked peers.
func Default() Engine { return &engine{} }

func (e *engine) Evaluate(env envelope.Envelope, peer pairing.Peer) Decision {
	if peer.Trust == pairing.TrustRevoked {
		return Deny
	}
	if env.Intent == envelope.IntentAskHuman {
		if e.cfg.AskHumanAllowlist != nil {
			if e.cfg.AskHumanAllowlist[peer.NodeID] {
				return Allow
			}
			if e.cfg.DefaultAskBehavior != 0 {
				return e.cfg.DefaultAskBehavior
			}
			return Downgrade
		}
		return Allow
	}
	return Allow
}
