package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/agents-first/clawdchan/hosts/openclaw"
	"github.com/agents-first/clawdchan/internal/listenerreg"
)

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	timeout := fs.Duration("timeout", 5*time.Second, "relay connect timeout")
	fs.Parse(args)

	fmt.Println("clawdchan doctor")

	// 1. config
	cfgPath := filepath.Join(defaultDataDir(), configFileName)
	c, cfgErr := loadConfig()
	if cfgErr != nil {
		fmt.Printf("  [FAIL] config: %v\n", cfgErr)
		fmt.Printf("         run: clawdchan init -relay <url> -alias <name>\n")
		return cfgErr
	}
	fmt.Printf("  [ok]  config: %s\n", cfgPath)
	fmt.Printf("         data dir: %s\n", c.DataDir)
	fmt.Printf("         relay:    %s\n", c.RelayURL)
	fmt.Printf("         alias:    %s\n", c.Alias)

	// 2. relay URL shape
	if err := checkRelayURL(c.RelayURL); err != nil {
		fmt.Printf("  [WARN] relay url: %v\n", err)
	}

	// 3. clawdchan CLI on PATH
	if p, err := exec.LookPath("clawdchan"); err == nil {
		fmt.Printf("  [ok]  clawdchan on PATH: %s\n", p)
	} else {
		fmt.Printf("  [WARN] clawdchan not on PATH. You are running: %s\n", firstNonEmpty(os.Args[0], "?"))
	}

	// 4. clawdchan-mcp discoverable
	mcpBin, mcpErr := resolveMCPBinary()
	if mcpErr != nil {
		fmt.Printf("  [FAIL] clawdchan-mcp: %v\n", mcpErr)
		fmt.Printf("         Claude Code's .mcp.json needs this binary on PATH, or an absolute\n")
		fmt.Printf("         path. Fix with `make install` then add $(go env GOPATH)/bin to PATH,\n")
		fmt.Printf("         or rerun `clawdchan init -write-mcp <project-dir>` to hardcode the path.\n")
	} else {
		fmt.Printf("  [ok]  clawdchan-mcp: %s\n", mcpBin)
	}

	// 5. node / identity / store
	n, err := openNode(context.Background(), c)
	if err != nil {
		fmt.Printf("  [FAIL] open node: %v\n", err)
		return err
	}
	defer n.Close()
	id := n.Identity()
	fmt.Printf("  [ok]  identity loaded: node id %s\n", hex.EncodeToString(id[:]))

	// 6. relay reachability
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := n.Start(ctx); err != nil {
		fmt.Printf("  [FAIL] relay connect: %v\n", err)
		return err
	}
	defer n.Stop()
	fmt.Printf("  [ok]  relay reachable\n")

	// Minimum-change OpenClaw config surface: detect active daemon OpenClaw mode
	// from the listener registry entry the daemon writes at startup.
	if openClawCfg, ok := activeOpenClawConfig(c.DataDir, hex.EncodeToString(id[:])); ok {
		fmt.Printf("  [ok]  openclaw mode active: %s\n", openClawCfg.OpenClawURL)
		openClawCtx, openClawCancel := context.WithTimeout(context.Background(), *timeout)
		defer openClawCancel()
		if err := checkOpenClawGateway(openClawCtx, openClawCfg); err != nil {
			fmt.Printf("  [FAIL] openclaw gateway connect: %v\n", err)
			fmt.Printf("         %s\n", openClawRemediation(err, openClawCfg.OpenClawURL))
			return err
		}
		fmt.Printf("  [ok]  openclaw gateway reachable\n")
	}

	// 7. peers / threads summary
	peers, _ := n.ListPeers(context.Background())
	threads, _ := n.ListThreads(context.Background())
	fmt.Printf("  [ok]  peers: %d, threads: %d\n", len(peers), len(threads))

	// 8. agent wiring — one block per registered host. An agent stays
	// silent when it reports nothing, so users who only wired CC don't
	// see noise for Gemini / Codex / Copilot.
	for _, a := range allAgents() {
		for _, line := range a.doctorReport() {
			fmt.Printf("  [ok]  %s\n", line)
		}
	}

	if mcpErr != nil {
		return mcpErr
	}
	fmt.Println("all checks passed")
	return nil
}

func checkRelayURL(raw string) error {
	if raw == "" {
		return errors.New("relay url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	switch u.Scheme {
	case "ws", "wss", "http", "https":
		return nil
	default:
		return fmt.Errorf("unexpected scheme %q (want ws/wss/http/https)", u.Scheme)
	}
}

func activeOpenClawConfig(dataDir, nodeID string) (listenerreg.Entry, bool) {
	entries, err := listenerreg.List(dataDir)
	if err != nil {
		return listenerreg.Entry{}, false
	}
	var best listenerreg.Entry
	ok := false
	for _, e := range entries {
		if e.Kind != listenerreg.KindCLI || !strings.EqualFold(e.NodeID, nodeID) || !e.OpenClawHostActive {
			continue
		}
		if !ok || e.StartedMs > best.StartedMs {
			best = e
			ok = true
		}
	}
	return best, ok
}

func checkOpenClawGateway(ctx context.Context, cfg listenerreg.Entry) error {
	bridge := openclaw.NewBridge(cfg.OpenClawURL, cfg.OpenClawToken, firstNonEmpty(cfg.OpenClawDeviceID, "clawdchan-daemon"), nil)
	if err := bridge.Connect(ctx); err != nil {
		return err
	}
	return bridge.Close()
}

func openClawRemediation(err error, wsURL string) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "401"), strings.Contains(msg, "403"), strings.Contains(msg, "unauthorized"), strings.Contains(msg, "forbidden"), strings.Contains(msg, "auth"):
		return "gateway rejected the token; update daemon -openclaw-token and restart the daemon service."
	case strings.Contains(msg, "dial"), strings.Contains(msg, "connection refused"), strings.Contains(msg, "no such host"), strings.Contains(msg, "timeout"):
		return fmt.Sprintf("gateway unreachable; ensure OpenClaw is listening at %s and daemon -openclaw matches that URL.", wsURL)
	default:
		return "check OpenClaw gateway logs and daemon -openclaw/-openclaw-token flags for mismatches."
	}
}
