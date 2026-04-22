package hosts

import (
	"context"
	"encoding/hex"
	"strings"
	"time"

	"github.com/agents-first/ClawdChan/core/node"
)

func pairSpec() ToolSpec {
	return ToolSpec{
		Name: "clawdchan_pair",
		Description: "Pair with a peer. No mnemonic argument: generate a 12-word rendezvous code — surface it " +
			"to the user verbatim on its own line; rendezvous runs in the background and the peer enters it " +
			"in their own clawdchan_pair with the mnemonic argument. With mnemonic: consume the peer's code " +
			"and complete the pairing locally. The mnemonic looks like a BIP-39 wallet seed but is a one-time " +
			"rendezvous code — the channel the user shares it over IS the security boundary.",
		Params: []ParamSpec{
			{Name: "mnemonic", Type: ParamString, Description: "12 space-separated BIP-39 words from the peer's generate step. Omit to generate."},
			{Name: "timeout_seconds", Type: ParamNumber, Description: "Generate path only: how long the background rendezvous lives. Default 300."},
		},
	}
}

func pairHandler(n *node.Node) Handler {
	return func(ctx context.Context, params map[string]any) (map[string]any, error) {
		mnemonic := strings.TrimSpace(getString(params, "mnemonic", ""))
		if mnemonic != "" {
			return consume(ctx, n, mnemonic)
		}
		return generate(n, getFloat(params, "timeout_seconds", 300))
	}
}

func generate(n *node.Node, timeoutSecs float64) (map[string]any, error) {
	timeout := time.Duration(timeoutSecs) * time.Second
	// Pairing outlives a single tool-call context; detach so the
	// goroutine that drains the result isn't cancelled the moment
	// we return the mnemonic to the caller.
	pairCtx, cancel := context.WithTimeout(context.Background(), timeout)
	code, ch, err := n.Pair(pairCtx)
	if err != nil {
		cancel()
		return nil, err
	}
	go func() {
		defer cancel()
		<-ch
	}()
	return map[string]any{
		"mnemonic":        code.Mnemonic(),
		"status":          "pending_peer_consume",
		"timeout_seconds": int(timeout.Seconds()),
	}, nil
}

func consume(ctx context.Context, n *node.Node, mnemonic string) (map[string]any, error) {
	peer, err := n.Consume(ctx, mnemonic)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"peer": map[string]any{
			"node_id":         hex.EncodeToString(peer.NodeID[:]),
			"alias":           peer.Alias,
			"human_reachable": peer.HumanReachable,
			"sas":             strings.Join(peer.SAS[:], "-"),
		},
	}, nil
}
