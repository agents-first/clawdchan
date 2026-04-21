package openclaw

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/agents-first/ClawdChan/core/envelope"
	"github.com/agents-first/ClawdChan/core/pairing"
	"github.com/agents-first/ClawdChan/core/store"
	"github.com/agents-first/ClawdChan/core/surface"
)

type mockSurfaceBridge struct {
	createCnt int
	createSID string
	sends     []sentMessage
	sendErr   error
}

type sentMessage struct {
	sid  string
	text string
}

func (m *mockSurfaceBridge) SessionCreate(context.Context, string) (string, error) {
	m.createCnt++
	if m.createSID == "" {
		m.createSID = "sid-created"
	}
	return m.createSID, nil
}

func (m *mockSurfaceBridge) SessionsSend(_ context.Context, sid, text string) error {
	m.sends = append(m.sends, sentMessage{sid: sid, text: text})
	return m.sendErr
}

func TestAgentSurfaceOnMessageSendsForAgentRenderToPeerSession(t *testing.T) {
	ctx := context.Background()
	st := openSurfaceStore(t)
	peerID := testNodeID(0x41)
	threadID := envelope.ULID{1, 2, 3, 4}
	if err := st.UpsertPeer(ctx, pairing.Peer{
		NodeID:         peerID,
		Alias:          "alice-local",
		Trust:          pairing.TrustPaired,
		HumanReachable: true,
		PairedAtMs:     time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateThread(ctx, store.Thread{
		ID:        threadID,
		PeerID:    peerID,
		Topic:     "agent-surface",
		CreatedMs: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOpenClawSession(ctx, peerID, "sid-agent"); err != nil {
		t.Fatal(err)
	}

	mockBr := &mockSurfaceBridge{}
	s := &AgentSurface{br: mockBr, sm: NewSessionMap(mockBr, st)}

	env := envelope.Envelope{
		From:   envelope.Principal{NodeID: peerID, Alias: "remote-alias"},
		Intent: envelope.IntentAskHuman,
		Content: envelope.Content{
			Kind: envelope.ContentText,
			Text: "need approval",
		},
	}
	if err := s.OnMessage(ctx, env); err != nil {
		t.Fatalf("OnMessage: %v", err)
	}

	if len(mockBr.sends) != 1 {
		t.Fatalf("expected one SessionsSend call, got %d", len(mockBr.sends))
	}
	if mockBr.sends[0].sid != "sid-agent" {
		t.Fatalf("expected sid-agent, got %q", mockBr.sends[0].sid)
	}
	want := ForAgent(env, &store.Peer{NodeID: peerID, Alias: "alice-local"})
	if mockBr.sends[0].text != want {
		t.Fatalf("unexpected send payload:\n got: %q\nwant: %q", mockBr.sends[0].text, want)
	}
}

func TestHumanSurfaceNotifySendsNotifyRender(t *testing.T) {
	ctx := context.Background()
	st := openSurfaceStore(t)
	peerID := testNodeID(0x51)
	threadID := envelope.ULID{5, 4, 3, 2, 1}
	if err := st.CreateThread(ctx, store.Thread{
		ID:        threadID,
		PeerID:    peerID,
		Topic:     "notify",
		CreatedMs: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOpenClawSession(ctx, peerID, "sid-notify"); err != nil {
		t.Fatal(err)
	}

	mockBr := &mockSurfaceBridge{}
	h := &HumanSurface{br: mockBr, sm: NewSessionMap(mockBr, st)}
	env := envelope.Envelope{
		From:   envelope.Principal{NodeID: peerID, Alias: "bob"},
		Intent: envelope.IntentNotifyHuman,
		Content: envelope.Content{
			Kind: envelope.ContentText,
			Text: "heads up",
		},
	}

	if err := h.Notify(ctx, threadID, env); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if len(mockBr.sends) != 1 {
		t.Fatalf("expected one SessionsSend call, got %d", len(mockBr.sends))
	}
	if mockBr.sends[0].sid != "sid-notify" {
		t.Fatalf("expected sid-notify, got %q", mockBr.sends[0].sid)
	}
	if want := Notify(env); mockBr.sends[0].text != want {
		t.Fatalf("unexpected send payload:\n got: %q\nwant: %q", mockBr.sends[0].text, want)
	}
}

func TestHumanSurfaceAskSendsAskRenderAndReturnsAsyncReply(t *testing.T) {
	ctx := context.Background()
	st := openSurfaceStore(t)
	peerID := testNodeID(0x61)
	threadID := envelope.ULID{6, 1, 6, 1}
	if err := st.CreateThread(ctx, store.Thread{
		ID:        threadID,
		PeerID:    peerID,
		Topic:     "ask",
		CreatedMs: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOpenClawSession(ctx, peerID, "sid-ask"); err != nil {
		t.Fatal(err)
	}

	mockBr := &mockSurfaceBridge{}
	h := &HumanSurface{br: mockBr, sm: NewSessionMap(mockBr, st)}
	env := envelope.Envelope{
		From:   envelope.Principal{NodeID: peerID, Alias: "carol"},
		Intent: envelope.IntentAskHuman,
		Content: envelope.Content{
			Kind: envelope.ContentText,
			Text: "approve deploy?",
		},
	}

	_, err := h.Ask(ctx, threadID, env)
	if !errors.Is(err, surface.ErrAsyncReply) {
		t.Fatalf("expected ErrAsyncReply, got %v", err)
	}
	if len(mockBr.sends) != 1 {
		t.Fatalf("expected one SessionsSend call, got %d", len(mockBr.sends))
	}
	if mockBr.sends[0].sid != "sid-ask" {
		t.Fatalf("expected sid-ask, got %q", mockBr.sends[0].sid)
	}
	if want := Ask(env); mockBr.sends[0].text != want {
		t.Fatalf("unexpected send payload:\n got: %q\nwant: %q", mockBr.sends[0].text, want)
	}
}

func TestHumanSurfaceReachabilityIsAsync(t *testing.T) {
	var h HumanSurface
	if got := h.Reachability(); got != surface.ReachableAsync {
		t.Fatalf("unexpected reachability: got %v want %v", got, surface.ReachableAsync)
	}
}

func openSurfaceStore(t *testing.T) store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clawdchan.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
