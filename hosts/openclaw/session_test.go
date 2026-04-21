package openclaw

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/agents-first/ClawdChan/core/envelope"
	"github.com/agents-first/ClawdChan/core/identity"
	"github.com/agents-first/ClawdChan/core/store"
)

type fakeBridge struct {
	createFn  func(context.Context, string) (string, error)
	createCnt int
	lastName  string
}

func (b *fakeBridge) SessionCreate(ctx context.Context, name string) (string, error) {
	b.createCnt++
	b.lastName = name
	if b.createFn != nil {
		return b.createFn(ctx, name)
	}
	return "sid-default", nil
}

type spyStore struct {
	store.Store
	getCnt int
	setCnt int

	getFn func(context.Context, identity.NodeID) (string, bool, error)
	setFn func(context.Context, identity.NodeID, string) error
}

func (s *spyStore) GetOpenClawSession(ctx context.Context, nodeID identity.NodeID) (string, bool, error) {
	s.getCnt++
	if s.getFn != nil {
		return s.getFn(ctx, nodeID)
	}
	return s.Store.GetOpenClawSession(ctx, nodeID)
}

func (s *spyStore) SetOpenClawSession(ctx context.Context, nodeID identity.NodeID, sid string) error {
	s.setCnt++
	if s.setFn != nil {
		return s.setFn(ctx, nodeID, sid)
	}
	return s.Store.SetOpenClawSession(ctx, nodeID, sid)
}

func openTempStore(t *testing.T) store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clawdchan.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustNodeID(t *testing.T) identity.NodeID {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id.SigningPublic
}

func TestSessionMapCacheHitSkipsStoreAndBridge(t *testing.T) {
	ctx := context.Background()
	nodeID := mustNodeID(t)
	st := &spyStore{
		getFn: func(context.Context, identity.NodeID) (string, bool, error) {
			t.Fatalf("store GetOpenClawSession should not be called on cache hit")
			return "", false, nil
		},
		setFn: func(context.Context, identity.NodeID, string) error {
			t.Fatalf("store SetOpenClawSession should not be called on cache hit")
			return nil
		},
	}
	br := &fakeBridge{
		createFn: func(context.Context, string) (string, error) {
			t.Fatalf("bridge SessionCreate should not be called on cache hit")
			return "", nil
		},
	}
	m := NewSessionMap(br, st)
	m.cacheSet(nodeID, "sid-cache")

	sid, err := m.EnsureSessionFor(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if sid != "sid-cache" {
		t.Fatalf("expected sid-cache, got %q", sid)
	}
	if st.getCnt != 0 || st.setCnt != 0 {
		t.Fatalf("store should not be touched on cache hit, get=%d set=%d", st.getCnt, st.setCnt)
	}
	if br.createCnt != 0 {
		t.Fatalf("bridge should not be touched on cache hit, create=%d", br.createCnt)
	}
}

func TestSessionMapStoreHitCachesAndSkipsBridge(t *testing.T) {
	ctx := context.Background()
	nodeID := mustNodeID(t)
	backing := openTempStore(t)
	if err := backing.SetOpenClawSession(ctx, nodeID, "sid-store"); err != nil {
		t.Fatal(err)
	}

	st := &spyStore{Store: backing}
	br := &fakeBridge{
		createFn: func(context.Context, string) (string, error) {
			t.Fatalf("bridge SessionCreate should not be called on store hit")
			return "", nil
		},
	}
	m := NewSessionMap(br, st)

	sid, err := m.EnsureSessionFor(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if sid != "sid-store" {
		t.Fatalf("expected sid-store, got %q", sid)
	}
	if st.getCnt != 1 || st.setCnt != 0 {
		t.Fatalf("expected one store get and no set, got get=%d set=%d", st.getCnt, st.setCnt)
	}
	if br.createCnt != 0 {
		t.Fatalf("bridge should not be called on store hit, create=%d", br.createCnt)
	}

	_, err = m.EnsureSessionFor(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if st.getCnt != 1 {
		t.Fatalf("second call should be cache hit; store gets=%d", st.getCnt)
	}
}

func TestSessionMapMissCreatesPersistsAndCaches(t *testing.T) {
	ctx := context.Background()
	nodeID := mustNodeID(t)
	backing := openTempStore(t)

	st := &spyStore{Store: backing}
	br := &fakeBridge{
		createFn: func(context.Context, string) (string, error) {
			return "sid-created", nil
		},
	}
	m := NewSessionMap(br, st)

	sid, err := m.EnsureSessionFor(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if sid != "sid-created" {
		t.Fatalf("expected sid-created, got %q", sid)
	}
	if st.getCnt != 1 || st.setCnt != 1 {
		t.Fatalf("expected get+set once on miss, got get=%d set=%d", st.getCnt, st.setCnt)
	}
	if br.createCnt != 1 {
		t.Fatalf("expected one bridge create, got %d", br.createCnt)
	}

	wantName := "clawdchan:" + hex.EncodeToString(nodeID[:])[:8]
	if br.lastName != wantName {
		t.Fatalf("unexpected session name: got %q want %q", br.lastName, wantName)
	}

	storedSID, ok, err := backing.GetOpenClawSession(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || storedSID != "sid-created" {
		t.Fatalf("session not persisted: sid=%q ok=%v", storedSID, ok)
	}

	_, err = m.EnsureSessionFor(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if br.createCnt != 1 {
		t.Fatalf("cache hit should skip bridge create; got %d", br.createCnt)
	}
}

func TestSessionMapRestartUsesPersistedMapping(t *testing.T) {
	ctx := context.Background()
	nodeID := mustNodeID(t)
	path := filepath.Join(t.TempDir(), "clawdchan.db")

	firstStore, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	firstBridge := &fakeBridge{
		createFn: func(context.Context, string) (string, error) {
			return "sid-initial", nil
		},
	}
	firstMap := NewSessionMap(firstBridge, firstStore)
	firstSID, err := firstMap.EnsureSessionFor(ctx, nodeID)
	if err != nil {
		_ = firstStore.Close()
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	secondBridge := &fakeBridge{
		createFn: func(context.Context, string) (string, error) {
			return "sid-should-not-be-created", nil
		},
	}
	secondMap := NewSessionMap(secondBridge, secondStore)
	secondSID, err := secondMap.EnsureSessionFor(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if secondSID != firstSID {
		t.Fatalf("expected persisted sid %q, got %q", firstSID, secondSID)
	}
	if secondBridge.createCnt != 0 {
		t.Fatalf("bridge should not create session after restart, create=%d", secondBridge.createCnt)
	}
}

func TestSessionMapEnsureSessionForThreadUsesThreadPeer(t *testing.T) {
	ctx := context.Background()
	backing := openTempStore(t)
	nodeID := mustNodeID(t)
	if err := backing.SetOpenClawSession(ctx, nodeID, "sid-thread"); err != nil {
		t.Fatal(err)
	}

	threadID := envelope.ULID{9, 8, 7, 6, 5, 4, 3, 2, 1}
	if err := backing.CreateThread(ctx, store.Thread{
		ID:        threadID,
		PeerID:    nodeID,
		Topic:     "openclaw-session-map",
		CreatedMs: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	br := &fakeBridge{
		createFn: func(context.Context, string) (string, error) {
			t.Fatalf("bridge SessionCreate should not be called on store hit by thread")
			return "", nil
		},
	}
	m := NewSessionMap(br, backing)

	sid, err := m.EnsureSessionForThread(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	if sid != "sid-thread" {
		t.Fatalf("expected sid-thread, got %q", sid)
	}
}
