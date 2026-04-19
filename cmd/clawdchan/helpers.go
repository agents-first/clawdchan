package main

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
)

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func parseNodeID(s string) (identity.NodeID, error) {
	s = strings.TrimSpace(s)
	b, err := hex.DecodeString(s)
	if err != nil {
		return identity.NodeID{}, fmt.Errorf("bad node id hex: %w", err)
	}
	if len(b) != len(identity.NodeID{}) {
		return identity.NodeID{}, fmt.Errorf("node id must be %d bytes hex", len(identity.NodeID{}))
	}
	var id identity.NodeID
	copy(id[:], b)
	return id, nil
}

func parseThreadID(s string) (envelope.ThreadID, error) {
	s = strings.TrimSpace(s)
	b, err := hex.DecodeString(s)
	if err != nil {
		return envelope.ThreadID{}, fmt.Errorf("bad thread id hex: %w", err)
	}
	if len(b) != 16 {
		return envelope.ThreadID{}, fmt.Errorf("thread id must be 16 bytes hex")
	}
	var id envelope.ThreadID
	copy(id[:], b)
	return id, nil
}

func printEnvelope(env envelope.Envelope, me identity.NodeID) {
	dir := "<-"
	if env.From.NodeID == me {
		dir = "->"
	}
	role := "agent"
	if env.From.Role == envelope.RoleHuman {
		role = "human"
	}
	fmt.Printf("[%s] %s %s/%s  thread=%s  %s\n",
		time.UnixMilli(env.CreatedAtMs).Format(time.RFC3339),
		dir, env.From.Alias, role,
		hex.EncodeToString(env.ThreadID[:]),
		renderContent(env.Intent, env.Content))
}

func renderContent(intent envelope.Intent, c envelope.Content) string {
	tag := intentName(intent)
	switch c.Kind {
	case envelope.ContentText:
		return fmt.Sprintf("%s: %s", tag, c.Text)
	case envelope.ContentDigest:
		return fmt.Sprintf("%s digest: %s — %s", tag, c.Title, c.Body)
	default:
		return tag
	}
}

func intentName(i envelope.Intent) string {
	switch i {
	case envelope.IntentSay:
		return "say"
	case envelope.IntentAsk:
		return "ask"
	case envelope.IntentNotifyHuman:
		return "notify-human"
	case envelope.IntentAskHuman:
		return "ask-human"
	case envelope.IntentHandoff:
		return "handoff"
	case envelope.IntentAck:
		return "ack"
	case envelope.IntentClose:
		return "close"
	default:
		return fmt.Sprintf("intent(%d)", i)
	}
}
