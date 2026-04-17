package transport_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/transport"
	"github.com/vMaroon/ClawdChan/internal/relayserver"
)

func TestRoundTripThroughRelay(t *testing.T) {
	srv := httptest.NewServer(relayserver.New(relayserver.Config{}).Handler())
	t.Cleanup(srv.Close)
	baseURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	alice, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	bob, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	tp := transport.NewWS(baseURL)
	aLink, err := tp.Connect(ctx, alice)
	if err != nil {
		t.Fatalf("alice connect: %v", err)
	}
	defer aLink.Close()
	bLink, err := tp.Connect(ctx, bob)
	if err != nil {
		t.Fatalf("bob connect: %v", err)
	}
	defer bLink.Close()

	payload := []byte("hello bob, this is alice")
	if err := aLink.Send(ctx, bob.SigningPublic, payload); err != nil {
		t.Fatalf("send: %v", err)
	}

	recvCtx, recvCancel := context.WithTimeout(ctx, 3*time.Second)
	defer recvCancel()
	frame, err := bLink.Recv(recvCtx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if frame.From != alice.SigningPublic {
		t.Fatalf("unexpected sender")
	}
	if string(frame.Data) != string(payload) {
		t.Fatalf("payload mismatch: %q", frame.Data)
	}
}

func TestIdleLinkSurvivesKeepalive(t *testing.T) {
	// Use a tight relay read timeout to make the test quick. Without
	// keepalive, the link would time out after 500ms of silence; with
	// pings every 100ms, it should survive 1.5s of idleness and still
	// deliver a frame afterwards.
	srv := httptest.NewServer(relayserver.New(relayserver.Config{
		ReadWriteTimeout: 500 * time.Millisecond,
	}).Handler())
	t.Cleanup(srv.Close)
	baseURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

	tp := &transport.WSTransport{
		BaseURL:      baseURL,
		PingInterval: 100 * time.Millisecond,
		ReadTimeout:  500 * time.Millisecond,
	}
	aLink, err := tp.Connect(ctx, alice)
	if err != nil {
		t.Fatalf("alice connect: %v", err)
	}
	defer aLink.Close()
	bLink, err := tp.Connect(ctx, bob)
	if err != nil {
		t.Fatalf("bob connect: %v", err)
	}
	defer bLink.Close()

	// Stay idle for 3× the relay timeout.
	time.Sleep(1500 * time.Millisecond)

	payload := []byte("after long idle")
	if err := aLink.Send(ctx, bob.SigningPublic, payload); err != nil {
		t.Fatalf("send after idle: %v", err)
	}
	recvCtx, cancelR := context.WithTimeout(ctx, 2*time.Second)
	defer cancelR()
	frame, err := bLink.Recv(recvCtx)
	if err != nil {
		t.Fatalf("recv after idle: %v", err)
	}
	if string(frame.Data) != string(payload) {
		t.Fatalf("payload mismatch: %q", frame.Data)
	}
}

func TestOfflineFrameIsQueuedAndFlushed(t *testing.T) {
	srv := httptest.NewServer(relayserver.New(relayserver.Config{}).Handler())
	t.Cleanup(srv.Close)
	baseURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

	tp := transport.NewWS(baseURL)

	// Alice connects, sends to offline Bob, then disconnects.
	aLink, err := tp.Connect(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("queued while offline")
	if err := aLink.Send(ctx, bob.SigningPublic, payload); err != nil {
		t.Fatal(err)
	}
	// Wait briefly for the relay to enqueue and send the "queued" ctl event.
	select {
	case ev := <-aLink.Events():
		if ev.Kind != "queued" {
			t.Fatalf("expected queued event, got %q", ev.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no queued event received")
	}
	aLink.Close()

	// Bob comes online and should get the queued frame.
	bLink, err := tp.Connect(ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	defer bLink.Close()
	recvCtx, recvCancel := context.WithTimeout(ctx, 3*time.Second)
	defer recvCancel()
	frame, err := bLink.Recv(recvCtx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if string(frame.Data) != string(payload) {
		t.Fatalf("payload mismatch: %q", frame.Data)
	}
}
