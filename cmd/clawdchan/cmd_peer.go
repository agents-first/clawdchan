package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/pairing"
)

// cmdPeer dispatches peer-management subcommands:
//
//	clawdchan peer show     <ref>           # details for one peer
//	clawdchan peer rename   <ref> <alias>   # change local display alias
//	clawdchan peer revoke   <ref>           # mark trust revoked (keep history)
//	clawdchan peer remove   <ref>           # hard delete (peer + threads + envelopes)
//
// <ref> matches the peer by any of:
//   - full 64-char hex node id
//   - unique hex prefix (≥ 4 chars)
//   - exact alias match (case-insensitive)
func cmdPeer(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: clawdchan peer <show|rename|revoke|remove> <ref> [args]")
	}
	sub := args[0]
	rest := args[1:]

	c, err := loadConfig()
	if err != nil {
		return err
	}
	n, err := openNode(context.Background(), c)
	if err != nil {
		return err
	}
	defer n.Close()
	ctx := context.Background()

	switch sub {
	case "show":
		if len(rest) < 1 {
			return errors.New("usage: clawdchan peer show <ref>")
		}
		return peerShow(ctx, n, rest[0])
	case "rename":
		if len(rest) < 2 {
			return errors.New("usage: clawdchan peer rename <ref> <new-alias>")
		}
		return peerRename(ctx, n, rest[0], strings.Join(rest[1:], " "))
	case "revoke":
		if len(rest) < 1 {
			return errors.New("usage: clawdchan peer revoke <ref>")
		}
		return peerRevoke(ctx, n, rest[0])
	case "remove", "delete", "forget":
		if len(rest) < 1 {
			return errors.New("usage: clawdchan peer remove <ref>")
		}
		return peerRemove(ctx, n, rest[0])
	default:
		return fmt.Errorf("unknown peer subcommand %q (use show|rename|revoke|remove)", sub)
	}
}

func peerShow(ctx context.Context, n *node.Node, ref string) error {
	p, err := resolvePeer(ctx, n, ref)
	if err != nil {
		return err
	}
	fmt.Printf("node_id:         %s\n", hex.EncodeToString(p.NodeID[:]))
	fmt.Printf("alias:           %s\n", p.Alias)
	fmt.Printf("trust:           %s\n", peerTrustName(p.Trust))
	fmt.Printf("human_reachable: %v\n", p.HumanReachable)
	fmt.Printf("paired_at:       %s\n", time.UnixMilli(p.PairedAtMs).Format(time.RFC3339))
	fmt.Printf("sas:             %s\n", strings.Join(p.SAS[:], "-"))
	return nil
}

func peerRename(ctx context.Context, n *node.Node, ref, newAlias string) error {
	if strings.TrimSpace(newAlias) == "" {
		return errors.New("new alias cannot be empty")
	}
	p, err := resolvePeer(ctx, n, ref)
	if err != nil {
		return err
	}
	if err := n.SetPeerAlias(ctx, p.NodeID, newAlias); err != nil {
		return err
	}
	fmt.Printf("renamed %s: %q → %q\n", hex.EncodeToString(p.NodeID[:])[:16], p.Alias, newAlias)
	return nil
}

func peerRevoke(ctx context.Context, n *node.Node, ref string) error {
	p, err := resolvePeer(ctx, n, ref)
	if err != nil {
		return err
	}
	if err := n.RevokePeer(ctx, p.NodeID); err != nil {
		return err
	}
	fmt.Printf("revoked %s (%q). Inbound from this peer will be dropped; history kept.\n",
		hex.EncodeToString(p.NodeID[:])[:16], p.Alias)
	return nil
}

func peerRemove(ctx context.Context, n *node.Node, ref string) error {
	p, err := resolvePeer(ctx, n, ref)
	if err != nil {
		return err
	}
	fmt.Printf("About to HARD DELETE peer %q (%s) plus all threads, envelopes, and outbox entries.\n",
		p.Alias, hex.EncodeToString(p.NodeID[:])[:16])
	ok, err := promptYN("This cannot be undone. Continue? [y/N]: ", false)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("cancelled.")
		return nil
	}
	if err := n.DeletePeer(ctx, p.NodeID); err != nil {
		return err
	}
	fmt.Printf("removed %s (%q).\n", hex.EncodeToString(p.NodeID[:])[:16], p.Alias)
	return nil
}

// resolvePeer accepts a full hex node id, a unique hex prefix (>=4), or an
// exact alias match, and returns the Peer record. Ambiguity or no match
// errors. Alias matching is case-insensitive; hex matching is not.
func resolvePeer(ctx context.Context, n *node.Node, ref string) (pairing.Peer, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return pairing.Peer{}, errors.New("empty peer reference")
	}
	peers, err := n.ListPeers(ctx)
	if err != nil {
		return pairing.Peer{}, err
	}

	// Full hex fast path.
	if len(ref) == 64 {
		id, err := parseNodeID(ref)
		if err == nil {
			for _, p := range peers {
				if p.NodeID == id {
					return p, nil
				}
			}
			return pairing.Peer{}, fmt.Errorf("no peer with node_id %s", ref)
		}
	}

	lower := strings.ToLower(ref)
	// Alias exact match.
	var aliasMatches []pairing.Peer
	for _, p := range peers {
		if strings.EqualFold(p.Alias, ref) {
			aliasMatches = append(aliasMatches, p)
		}
	}
	if len(aliasMatches) == 1 {
		return aliasMatches[0], nil
	}
	if len(aliasMatches) > 1 {
		return pairing.Peer{}, fmt.Errorf("alias %q is ambiguous (%d peers); use a hex prefix", ref, len(aliasMatches))
	}

	// Hex prefix.
	if len(lower) >= 4 {
		var prefixMatches []pairing.Peer
		for _, p := range peers {
			if strings.HasPrefix(hex.EncodeToString(p.NodeID[:]), lower) {
				prefixMatches = append(prefixMatches, p)
			}
		}
		if len(prefixMatches) == 1 {
			return prefixMatches[0], nil
		}
		if len(prefixMatches) > 1 {
			return pairing.Peer{}, fmt.Errorf("hex prefix %q is ambiguous (%d peers); use more characters", ref, len(prefixMatches))
		}
	}

	return pairing.Peer{}, fmt.Errorf("no peer matches %q (try an alias or hex prefix from `clawdchan peers`)", ref)
}

func peerTrustName(t pairing.Trust) string {
	switch t {
	case pairing.TrustPaired:
		return "paired"
	case pairing.TrustBridged:
		return "bridged"
	case pairing.TrustRevoked:
		return "revoked"
	default:
		return "unknown"
	}
}

var _ = identity.NodeID{} // keep import live
