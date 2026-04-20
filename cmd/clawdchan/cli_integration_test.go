package main_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestCLIPairAndSend compiles the two commands and drives a full pair+send+
// receive cycle between two isolated CLAWDCHAN_HOME directories. This catches
// any wiring regressions between the core and the CLI.
func TestCLIPairAndSend(t *testing.T) {
	if testing.Short() {
		t.Skip("cli integration builds binaries; slow")
	}
	repoRoot, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	clawdchan := filepath.Join(binDir, binaryName("clawdchan"))
	relay := filepath.Join(binDir, binaryName("clawdchan-relay"))
	if err := goBuild(repoRoot, clawdchan, "./cmd/clawdchan"); err != nil {
		t.Fatalf("build clawdchan: %v", err)
	}
	if err := goBuild(repoRoot, relay, "./cmd/clawdchan-relay"); err != nil {
		t.Fatalf("build clawdchan-relay: %v", err)
	}

	port, err := freeTCPPort()
	if err != nil {
		t.Fatal(err)
	}
	relayURL := fmt.Sprintf("ws://127.0.0.1:%d", port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	relayCmd := exec.CommandContext(ctx, relay, "-addr", fmt.Sprintf(":%d", port))
	relayOut := &bytes.Buffer{}
	relayCmd.Stdout = relayOut
	relayCmd.Stderr = relayOut
	if err := relayCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = relayCmd.Process.Kill()
		_, _ = relayCmd.Process.Wait()
	}()
	if err := waitForPort(ctx, port); err != nil {
		t.Fatalf("relay not ready: %v\n%s", err, relayOut.String())
	}

	alice := t.TempDir()
	bob := t.TempDir()

	run := func(home string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, clawdchan, args...)
		cmd.Env = append(os.Environ(), "CLAWDCHAN_HOME="+home)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if _, err := run(alice, "init", "-data", alice, "-relay", relayURL, "-alias", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(bob, "init", "-data", bob, "-relay", relayURL, "-alias", "bob"); err != nil {
		t.Fatal(err)
	}

	// Start alice pair in a goroutine; extract the mnemonic from its output.
	type pairResult struct {
		out string
		err error
	}
	pairCh := make(chan pairResult, 1)
	alicePair := exec.CommandContext(ctx, clawdchan, "pair", "-timeout", "20s")
	alicePair.Env = append(os.Environ(), "CLAWDCHAN_HOME="+alice)
	pairOut := &bytes.Buffer{}
	alicePair.Stdout = pairOut
	alicePair.Stderr = pairOut
	if err := alicePair.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := alicePair.Wait()
		pairCh <- pairResult{out: pairOut.String(), err: err}
	}()

	// Wait for the "consume" line to appear.
	var mnemonic string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if line := findConsumeLine(pairOut.String()); line != "" {
			mnemonic = line
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if mnemonic == "" {
		t.Fatalf("no consume line from alice pair\n%s", pairOut.String())
	}

	consumeArgs := append([]string{"consume"}, strings.Fields(mnemonic)...)
	out, err := run(bob, consumeArgs...)
	if err != nil {
		t.Fatalf("bob consume failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Paired with") {
		t.Fatalf("unexpected bob consume output:\n%s", out)
	}

	// Wait for alice pair command to finish.
	select {
	case r := <-pairCh:
		if r.err != nil {
			t.Fatalf("alice pair exited with error: %v\n%s", r.err, r.out)
		}
		if !strings.Contains(r.out, "Paired with") {
			t.Fatalf("alice pair did not confirm: %s", r.out)
		}
	case <-ctx.Done():
		t.Fatal("pair timed out")
	}

	// Extract bob's node id from alice's peers output.
	peersOut, err := run(alice, "peers")
	if err != nil {
		t.Fatalf("alice peers: %v\n%s", err, peersOut)
	}
	if !strings.Contains(peersOut, "bob") {
		t.Fatalf("alice peers missing bob:\n%s", peersOut)
	}

	// Pull bob's full node id from whoami.
	bobWhoami, _ := run(bob, "whoami")
	bobID := extractField(bobWhoami, "node id:")
	if bobID == "" {
		t.Fatalf("could not extract bob node id:\n%s", bobWhoami)
	}

	// Start bob listening so inbound frames are persisted to his store.
	bobListen := exec.CommandContext(ctx, clawdchan, "listen")
	bobListen.Env = append(os.Environ(), "CLAWDCHAN_HOME="+bob)
	listenOut := &bytes.Buffer{}
	bobListen.Stdout = listenOut
	bobListen.Stderr = listenOut
	if err := bobListen.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = bobListen.Process.Signal(os.Interrupt)
		_, _ = bobListen.Process.Wait()
	}()
	// Wait for the "listening" banner so we know the link is up.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(listenOut.String(), "listening") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Open a thread from alice -> bob and send a message.
	threadOut, err := run(alice, "open", bobID, "-topic", "greetings")
	if err != nil {
		t.Fatalf("alice open: %v\n%s", err, threadOut)
	}
	threadID := strings.TrimSpace(threadOut)
	if len(threadID) != 32 {
		t.Fatalf("unexpected thread id: %q", threadID)
	}

	if out, err := run(alice, "send", threadID, "ping from alice"); err != nil {
		t.Fatalf("alice send: %v\n%s", err, out)
	}

	// Wait for bob's listener to print the inbound line. SQLite WAL mode lets
	// us also read from the CLI concurrently, but the log check is more
	// direct and races fewer moving parts.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(listenOut.String(), "ping from alice") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("bob never observed alice's message. listen log:\n%s", listenOut.String())
}

func TestCLIStatusGlyph(t *testing.T) {
	if os.Getenv("CLAWDCHAN_HAS_ACK") == "" {
		t.Skip("requires feat/status-node merged — set CLAWDCHAN_HAS_ACK=1")
	}
	if testing.Short() {
		t.Skip("cli integration builds binaries; slow")
	}
	repoRoot, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	clawdchan := filepath.Join(binDir, binaryName("clawdchan"))
	relay := filepath.Join(binDir, binaryName("clawdchan-relay"))
	if err := goBuild(repoRoot, clawdchan, "./cmd/clawdchan"); err != nil {
		t.Fatalf("build clawdchan: %v", err)
	}
	if err := goBuild(repoRoot, relay, "./cmd/clawdchan-relay"); err != nil {
		t.Fatalf("build clawdchan-relay: %v", err)
	}

	port, err := freeTCPPort()
	if err != nil {
		t.Fatal(err)
	}
	relayURL := fmt.Sprintf("ws://127.0.0.1:%d", port)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	relayCmd := exec.CommandContext(ctx, relay, "-addr", fmt.Sprintf(":%d", port))
	relayOut := &bytes.Buffer{}
	relayCmd.Stdout = relayOut
	relayCmd.Stderr = relayOut
	if err := relayCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = relayCmd.Process.Kill()
		_, _ = relayCmd.Process.Wait()
	}()
	if err := waitForPort(ctx, port); err != nil {
		t.Fatalf("relay not ready: %v\n%s", err, relayOut.String())
	}

	alice := t.TempDir()
	bob := t.TempDir()

	run := func(home string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, clawdchan, args...)
		cmd.Env = append(os.Environ(), "CLAWDCHAN_HOME="+home)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if _, err := run(alice, "init", "-data", alice, "-relay", relayURL, "-alias", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(bob, "init", "-data", bob, "-relay", relayURL, "-alias", "bob"); err != nil {
		t.Fatal(err)
	}

	type pairResult struct {
		out string
		err error
	}
	pairCh := make(chan pairResult, 1)
	alicePair := exec.CommandContext(ctx, clawdchan, "pair", "-timeout", "20s")
	alicePair.Env = append(os.Environ(), "CLAWDCHAN_HOME="+alice)
	pairOut := &bytes.Buffer{}
	alicePair.Stdout = pairOut
	alicePair.Stderr = pairOut
	if err := alicePair.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := alicePair.Wait()
		pairCh <- pairResult{out: pairOut.String(), err: err}
	}()

	var mnemonic string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if line := findConsumeLine(pairOut.String()); line != "" {
			mnemonic = line
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if mnemonic == "" {
		t.Fatalf("no consume line from alice pair\n%s", pairOut.String())
	}

	consumeArgs := append([]string{"consume"}, strings.Fields(mnemonic)...)
	out, err := run(bob, consumeArgs...)
	if err != nil {
		t.Fatalf("bob consume failed: %v\n%s", err, out)
	}

	select {
	case r := <-pairCh:
		if r.err != nil {
			t.Fatalf("alice pair exited with error: %v\n%s", r.err, r.out)
		}
	case <-ctx.Done():
		t.Fatal("pair timed out")
	}

	bobWhoami, _ := run(bob, "whoami")
	bobID := extractField(bobWhoami, "node id:")
	if bobID == "" {
		t.Fatalf("could not extract bob node id:\n%s", bobWhoami)
	}

	bobListen := exec.CommandContext(ctx, clawdchan, "listen")
	bobListen.Env = append(os.Environ(), "CLAWDCHAN_HOME="+bob)
	listenOut := &bytes.Buffer{}
	bobListen.Stdout = listenOut
	bobListen.Stderr = listenOut
	if err := bobListen.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = bobListen.Process.Signal(os.Interrupt)
		_, _ = bobListen.Process.Wait()
	}()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(listenOut.String(), "listening") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Alice also needs a long-running node process to receive the ack
	// bob emits. Without it, alice send is a one-shot that exits before
	// the ack arrives and alice threads never reads the updated status.
	aliceListen := exec.CommandContext(ctx, clawdchan, "listen")
	aliceListen.Env = append(os.Environ(), "CLAWDCHAN_HOME="+alice)
	aliceListenOut := &bytes.Buffer{}
	aliceListen.Stdout = aliceListenOut
	aliceListen.Stderr = aliceListenOut
	if err := aliceListen.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = aliceListen.Process.Signal(os.Interrupt)
		_, _ = aliceListen.Process.Wait()
	}()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(aliceListenOut.String(), "listening") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	threadOut, err := run(alice, "open", bobID, "-topic", "status-glyph")
	if err != nil {
		t.Fatalf("alice open: %v\n%s", err, threadOut)
	}
	threadID := strings.TrimSpace(threadOut)
	if len(threadID) != 32 {
		t.Fatalf("unexpected thread id: %q", threadID)
	}
	if out, err := run(alice, "send", threadID, "status please"); err != nil {
		t.Fatalf("alice send: %v\n%s", err, out)
	}

	deliveredGlyph := expectedDeliveredGlyph()
	var lastThreads string
	deadline = time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		threadsOut, err := run(alice, "threads")
		if err == nil {
			lastThreads = threadsOut
			if strings.Contains(threadsOut, threadID) && strings.Contains(threadsOut, deliveredGlyph) {
				return
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("threads never reached delivered glyph %q for thread %s\n%s", deliveredGlyph, threadID, lastThreads)
}

func findConsumeLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "clawdchan consume ") {
			return strings.TrimPrefix(line, "clawdchan consume ")
		}
	}
	return ""
}

func extractField(s, prefix string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func repoRoot() (string, error) {
	// cmd/clawdchan → go up two levels.
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(wd, "..", "..")), nil
}

func goBuild(repoRoot, out, pkg string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitForPort(ctx context.Context, port int) error {
	for {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func binaryName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func expectedDeliveredGlyph() string {
	if runtime.GOOS == "windows" && os.Getenv("WT_SESSION") == "" && os.Getenv("TERM_PROGRAM") == "" {
		return "v"
	}
	return "✓"
}
