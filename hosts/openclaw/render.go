package openclaw

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/agents-first/ClawdChan/core/envelope"
	"github.com/agents-first/ClawdChan/core/store"
)

func ForAgent(env envelope.Envelope, peer *store.Peer) string {
	alias := shortNodeID(env.From.NodeID)
	if peer != nil {
		if a := strings.TrimSpace(peer.Alias); a != "" {
			alias = a
		}
	}
	return renderEnvelope(alias, env.Intent, env.Content)
}

func Notify(env envelope.Envelope) string {
	return renderEnvelope(aliasFromEnvelope(env), env.Intent, env.Content)
}

func Ask(env envelope.Envelope) string {
	return renderEnvelope(aliasFromEnvelope(env), env.Intent, env.Content)
}

func renderEnvelope(alias string, intent envelope.Intent, content envelope.Content) string {
	return fmt.Sprintf("[clawdchan · from %s · %s]\n%s", alias, intentName(intent), contentBody(content))
}

func aliasFromEnvelope(env envelope.Envelope) string {
	if a := strings.TrimSpace(env.From.Alias); a != "" {
		return a
	}
	return shortNodeID(env.From.NodeID)
}

func shortNodeID(nodeID [32]byte) string {
	return hex.EncodeToString(nodeID[:])[:8]
}

func intentName(intent envelope.Intent) string {
	switch intent {
	case envelope.IntentSay:
		return "say"
	case envelope.IntentAsk:
		return "ask"
	case envelope.IntentAskHuman:
		return "ask_human"
	case envelope.IntentNotifyHuman:
		return "notify_human"
	case envelope.IntentHandoff:
		return "handoff"
	case envelope.IntentAck:
		return "ack"
	case envelope.IntentClose:
		return "close"
	default:
		return fmt.Sprintf("intent_%d", intent)
	}
}

func contentBody(c envelope.Content) string {
	switch c.Kind {
	case envelope.ContentText:
		return c.Text
	case envelope.ContentDigest:
		if c.Title != "" && c.Body != "" {
			return c.Title + "\n" + c.Body
		}
		if c.Body != "" {
			return c.Body
		}
		return c.Title
	default:
		if c.Text != "" {
			return c.Text
		}
		if c.Title != "" && c.Body != "" {
			return c.Title + "\n" + c.Body
		}
		if c.Body != "" {
			return c.Body
		}
		return c.Title
	}
}
