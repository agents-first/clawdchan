package openclaw

import (
	"fmt"
	"testing"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/store"
)

func TestForAgentUsesPeerAliasAndIntentNames(t *testing.T) {
	nodeID := testNodeID(0x10)
	peer := &store.Peer{Alias: "alice", NodeID: nodeID}
	intents := []struct {
		intent envelope.Intent
		name   string
	}{
		{envelope.IntentSay, "say"},
		{envelope.IntentAsk, "ask"},
		{envelope.IntentAskHuman, "ask_human"},
		{envelope.IntentNotifyHuman, "notify_human"},
		{envelope.IntentHandoff, "handoff"},
		{envelope.IntentAck, "ack"},
		{envelope.IntentClose, "close"},
	}

	for _, tc := range intents {
		t.Run(tc.name, func(t *testing.T) {
			env := envelope.Envelope{
				From:   envelope.Principal{NodeID: nodeID, Alias: "remote-alias"},
				Intent: tc.intent,
				Content: envelope.Content{
					Kind: envelope.ContentText,
					Text: "hello",
				},
			}
			got := ForAgent(env, peer)
			want := fmt.Sprintf("[clawdchan · from alice · %s]\nhello", tc.name)
			if got != want {
				t.Fatalf("unexpected render:\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

func TestForAgentFallsBackToShortNodeIDWhenPeerAliasEmpty(t *testing.T) {
	nodeID := testNodeID(0x20)
	env := envelope.Envelope{
		From:   envelope.Principal{NodeID: nodeID, Alias: "ignored-when-peer-provided"},
		Intent: envelope.IntentAskHuman,
		Content: envelope.Content{
			Kind: envelope.ContentText,
			Text: "body",
		},
	}

	got := ForAgent(env, &store.Peer{NodeID: nodeID})
	want := fmt.Sprintf("[clawdchan · from %s · ask_human]\nbody", shortNodeID(nodeID))
	if got != want {
		t.Fatalf("unexpected render:\n got: %q\nwant: %q", got, want)
	}
}

func TestNotifyAndAskRenderWithEnvelopeAlias(t *testing.T) {
	nodeID := testNodeID(0x30)
	env := envelope.Envelope{
		From:   envelope.Principal{NodeID: nodeID, Alias: "bob"},
		Intent: envelope.IntentNotifyHuman,
		Content: envelope.Content{
			Kind: envelope.ContentText,
			Text: "heads up",
		},
	}

	if got, want := Notify(env), "[clawdchan · from bob · notify_human]\nheads up"; got != want {
		t.Fatalf("notify render mismatch:\n got: %q\nwant: %q", got, want)
	}

	env.Intent = envelope.IntentAskHuman
	env.Content.Text = "please approve"
	if got, want := Ask(env), "[clawdchan · from bob · ask_human]\nplease approve"; got != want {
		t.Fatalf("ask render mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func testNodeID(seed byte) identity.NodeID {
	var nodeID identity.NodeID
	for i := range nodeID {
		nodeID[i] = seed + byte(i)
	}
	return nodeID
}
