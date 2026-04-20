package openclaw

import (
	"strings"
	"testing"

	"github.com/vMaroon/ClawdChan/core/store"
)

func TestHubContextEmptyPeersMentionsNoPairedPeers(t *testing.T) {
	got := HubContext("alice", nil)
	if got == "" {
		t.Fatal("HubContext() returned empty string")
	}
	if !strings.Contains(strings.ToLower(got), "no paired peers") {
		t.Fatalf("HubContext() missing no-peers hint:\n%s", got)
	}
	if !strings.Contains(got, `{"cc":`) {
		t.Fatalf("HubContext() missing cc action pattern:\n%s", got)
	}
}

func TestHubContextMentionsPeerAliases(t *testing.T) {
	peers := []store.Peer{
		{Alias: "alice"},
		{Alias: "bob"},
	}
	got := HubContext("me", peers)
	if !strings.Contains(got, "alice") || !strings.Contains(got, "bob") {
		t.Fatalf("HubContext() missing peer aliases:\n%s", got)
	}
}

func TestPeerContextMentionsPeerAlias(t *testing.T) {
	got := PeerContext("self", "carol")
	if got == "" {
		t.Fatal("PeerContext() returned empty string")
	}
	if !strings.Contains(got, "carol") {
		t.Fatalf("PeerContext() missing peer alias:\n%s", got)
	}
	if !strings.Contains(got, `{"cc":`) {
		t.Fatalf("PeerContext() missing cc action pattern:\n%s", got)
	}
}
