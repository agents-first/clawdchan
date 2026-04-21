package pairing_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agents-first/ClawdChan/core/identity"
	"github.com/agents-first/ClawdChan/core/pairing"
	"github.com/agents-first/ClawdChan/internal/relayserver"
)

func TestCodeMnemonicRoundTrip(t *testing.T) {
	c, err := pairing.GenerateCode()
	if err != nil {
		t.Fatal(err)
	}
	m := c.Mnemonic()
	if len(strings.Fields(m)) != 12 {
		t.Fatalf("expected 12-word mnemonic, got %q", m)
	}
	got, err := pairing.ParseCode(m)
	if err != nil {
		t.Fatal(err)
	}
	if got != c {
		t.Fatal("parsed code != original")
	}
}

func TestRendezvousExchange(t *testing.T) {
	srv := httptest.NewServer(relayserver.New(relayserver.Config{PairRendezvousTTL: 10 * time.Second}).Handler())
	t.Cleanup(srv.Close)
	baseURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

	code, err := pairing.GenerateCode()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	aCard := pairing.MyCard(alice, "alice", true)
	bCard := pairing.MyCard(bob, "bob", false)

	type result struct {
		peer pairing.Peer
		err  error
	}
	aCh := make(chan result, 1)
	bCh := make(chan result, 1)

	go func() {
		p, err := pairing.Rendezvous(ctx, baseURL, code, aCard, true)
		aCh <- result{p, err}
	}()
	go func() {
		p, err := pairing.Rendezvous(ctx, baseURL, code, bCard, false)
		bCh <- result{p, err}
	}()

	aRes := <-aCh
	bRes := <-bCh

	if aRes.err != nil {
		t.Fatalf("alice: %v", aRes.err)
	}
	if bRes.err != nil {
		t.Fatalf("bob: %v", bRes.err)
	}
	if aRes.peer.NodeID != bob.SigningPublic {
		t.Fatal("alice got wrong peer node id")
	}
	if bRes.peer.NodeID != alice.SigningPublic {
		t.Fatal("bob got wrong peer node id")
	}
	if aRes.peer.Alias != "bob" || bRes.peer.Alias != "alice" {
		t.Fatalf("alias mismatch: a=%q b=%q", aRes.peer.Alias, bRes.peer.Alias)
	}
	if aRes.peer.Trust != pairing.TrustPaired {
		t.Fatal("expected TrustPaired")
	}
	if aRes.peer.SAS != bRes.peer.SAS {
		t.Fatalf("SAS mismatch: %v vs %v", aRes.peer.SAS, bRes.peer.SAS)
	}
	for _, w := range aRes.peer.SAS {
		if w == "" {
			t.Fatal("empty SAS word")
		}
	}
}
