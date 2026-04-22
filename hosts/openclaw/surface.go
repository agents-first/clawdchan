package openclaw

import (
	"context"
	"errors"

	"github.com/agents-first/clawdchan/core/envelope"
	"github.com/agents-first/clawdchan/core/store"
	"github.com/agents-first/clawdchan/core/surface"
)

type sessionsSender interface {
	SessionsSend(ctx context.Context, sid, text string) error
}

var _ sessionsSender = (*Bridge)(nil)
var _ surface.AgentSurface = (*AgentSurface)(nil)
var _ surface.HumanSurface = (*HumanSurface)(nil)

type AgentSurface struct {
	br sessionsSender
	sm *SessionMap
}

func NewAgentSurface(sm *SessionMap, br sessionsSender) *AgentSurface {
	return &AgentSurface{br: br, sm: sm}
}

func (a *AgentSurface) OnMessage(ctx context.Context, env envelope.Envelope) error {
	if a == nil || a.sm == nil {
		return errors.New("openclaw: agent surface missing session map")
	}
	if a.br == nil {
		return errors.New("openclaw: agent surface missing bridge")
	}

	sid, err := a.sm.EnsureSessionFor(ctx, env.From.NodeID)
	if err != nil {
		return err
	}

	var peer *store.Peer
	if a.sm.store != nil {
		p, err := a.sm.store.GetPeer(ctx, env.From.NodeID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return err
			}
		} else {
			peer = &p
		}
	}

	return a.br.SessionsSend(ctx, sid, ForAgent(env, peer))
}

type HumanSurface struct {
	br sessionsSender
	sm *SessionMap
}

func NewHumanSurface(sm *SessionMap, br sessionsSender) *HumanSurface {
	return &HumanSurface{br: br, sm: sm}
}

func (h *HumanSurface) Notify(ctx context.Context, thread envelope.ThreadID, env envelope.Envelope) error {
	if h == nil || h.sm == nil {
		return errors.New("openclaw: human surface missing session map")
	}
	if h.br == nil {
		return errors.New("openclaw: human surface missing bridge")
	}
	sid, err := h.sm.EnsureSessionForThread(ctx, thread)
	if err != nil {
		return err
	}
	return h.br.SessionsSend(ctx, sid, Notify(env))
}

func (h *HumanSurface) Ask(ctx context.Context, thread envelope.ThreadID, env envelope.Envelope) (envelope.Content, error) {
	if h == nil || h.sm == nil {
		return envelope.Content{}, errors.New("openclaw: human surface missing session map")
	}
	if h.br == nil {
		return envelope.Content{}, errors.New("openclaw: human surface missing bridge")
	}
	sid, err := h.sm.EnsureSessionForThread(ctx, thread)
	if err != nil {
		return envelope.Content{}, err
	}
	if err := h.br.SessionsSend(ctx, sid, Ask(env)); err != nil {
		return envelope.Content{}, err
	}
	return envelope.Content{}, surface.ErrAsyncReply
}

func (HumanSurface) Reachability() surface.Reachability {
	return surface.ReachableAsync
}

func (HumanSurface) PresentThread(context.Context, envelope.ThreadID) error { return nil }
