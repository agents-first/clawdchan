package openclaw

import (
	"encoding/json"
	"strings"
)

type ActionKind string

const (
	ActionPair    ActionKind = "pair"
	ActionConsume ActionKind = "consume"
	ActionPeers   ActionKind = "peers"
	ActionInbox   ActionKind = "inbox"
)

type Action struct {
	Kind  ActionKind
	Words string
}

// ParseActions scans text for embedded {"cc":...} JSON blocks and returns all
// valid actions found.
func ParseActions(text string) []Action {
	lines := strings.Split(text, "\n")
	actions := make([]Action, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if action, ok := parseActionJSON(trimmed); ok {
			actions = append(actions, action)
			continue
		}

		actions = append(actions, parseEmbeddedActions(line)...)
	}

	return actions
}

// IsClawdChanTurn returns true if the text contains any recognized action.
func IsClawdChanTurn(text string) bool {
	return len(ParseActions(text)) > 0
}

func parseEmbeddedActions(line string) []Action {
	var actions []Action

	for idx := 0; idx < len(line); {
		rel := strings.IndexByte(line[idx:], '{')
		if rel < 0 {
			break
		}
		start := idx + rel

		dec := json.NewDecoder(strings.NewReader(line[start:]))
		var payload map[string]any
		if err := dec.Decode(&payload); err != nil {
			idx = start + 1
			continue
		}

		if action, ok := parseActionMap(payload); ok {
			actions = append(actions, action)
		}

		used := int(dec.InputOffset())
		if used <= 0 {
			idx = start + 1
			continue
		}
		idx = start + used
	}

	return actions
}

func parseActionJSON(raw string) (Action, bool) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return Action{}, false
	}
	return parseActionMap(payload)
}

func parseActionMap(payload map[string]any) (Action, bool) {
	value, ok := payload["cc"].(string)
	if !ok {
		return Action{}, false
	}

	kind := ActionKind(value)
	switch kind {
	case ActionPair, ActionPeers, ActionInbox:
		return Action{Kind: kind}, true
	case ActionConsume:
		words, ok := payload["words"].(string)
		if !ok {
			return Action{}, false
		}
		words = strings.TrimSpace(words)
		if strings.Count(words, " ") != 11 {
			return Action{}, false
		}
		return Action{
			Kind:  kind,
			Words: words,
		}, true
	default:
		return Action{}, false
	}
}
