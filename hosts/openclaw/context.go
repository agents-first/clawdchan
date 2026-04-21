package openclaw

import (
	"fmt"
	"strings"

	"github.com/agents-first/ClawdChan/core/store"
)

// HubContext returns the capability context message for the hub session.
func HubContext(alias string, peers []store.Peer) string {
	self := strings.TrimSpace(alias)
	if self == "" {
		self = "your user"
	}

	return fmt.Sprintf(
		"You are %s's ClawdChan agent.\n"+
			"ClawdChan is end-to-end encrypted peer-to-peer messaging between AI agents.\n\n"+
			"To trigger ClawdChan actions, include one compact JSON block in your response, either on a line by itself or at the end of a response:\n"+
			`PAIR    -> {"cc":"pair"}`+"\n"+
			`CONSUME -> {"cc":"consume","words":"word1 word2 ... word12"}`+"\n"+
			`PEERS   -> {"cc":"peers"}`+"\n"+
			`INBOX   -> {"cc":"inbox"}`+"\n\n"+
			"Workflow examples:\n"+
			`User: "pair me with someone"`+"\n"+
			`Assistant: "I'll generate a pairing code now."`+"\n"+
			`{"cc":"pair"}`+"\n"+
			"System reply: a 12-word mnemonic for the user to share.\n\n"+
			`User: "consume this: elder thunder high ..."`+"\n"+
			`{"cc":"consume","words":"elder thunder high ..."}`+"\n"+
			`User: "who am I paired with?"`+"\n"+
			`{"cc":"peers"}`+"\n"+
			`User: "any new messages?"`+"\n"+
			`{"cc":"inbox"}`+"\n\n"+
			"After consume succeeds, a dedicated conversation session appears for that peer. "+
			"Messages to and from that peer flow through that peer session.\n\n"+
			"%s",
		self,
		formatPeers(peers),
	)
}

// PeerContext returns the capability context message for a peer session.
func PeerContext(selfAlias, peerAlias string) string {
	self := strings.TrimSpace(selfAlias)
	if self == "" {
		self = "your user"
	}
	peer := strings.TrimSpace(peerAlias)
	if peer == "" {
		peer = "this peer"
	}

	return fmt.Sprintf(
		"This session is a direct ClawdChan channel to %s.\n"+
			"You are %s's ClawdChan agent in this peer session.\n"+
			"Assistant messages you write that are not prefixed with a cc action block are delivered to %s as ClawdChan envelopes.\n"+
			"To check for new messages, use:\n"+
			`{"cc":"inbox"}`+"\n"+
			"Your job is to reply to incoming messages from this peer, surface important content to the user, and route questions to the user when needed.",
		peer,
		self,
		peer,
	)
}

func formatPeers(peers []store.Peer) string {
	if len(peers) == 0 {
		return "You have no paired peers yet."
	}

	lines := make([]string, 0, len(peers))
	for _, p := range peers {
		label := strings.TrimSpace(p.Alias)
		if label == "" {
			label = shortNodeID(p.NodeID)
		}
		lines = append(lines, "- "+label)
	}
	return "Current peers:\n" + strings.Join(lines, "\n")
}
