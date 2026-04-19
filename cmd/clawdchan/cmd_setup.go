package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/vMaroon/ClawdChan/core/node"
)

// cmdSetup is the one-command onboarding flow invoked by `make install`.
// It chains initial config (if missing), project .mcp.json, PATH wiring,
// and the background daemon install — all with inline prompts and sane
// defaults. Each step is idempotent and skips cleanly when already done,
// so rerunning is safe.
func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	yes := fs.Bool("y", false, "assume yes; use defaults everywhere and skip all prompts")
	fs.Parse(args)

	// Step 1: identity + config.
	cfgPath := filepath.Join(defaultDataDir(), configFileName)
	if _, err := os.Stat(cfgPath); err != nil {
		if err := setupInit(*yes); err != nil {
			return fmt.Errorf("init: %w", err)
		}
	} else {
		c, err := loadConfig()
		if err == nil {
			fmt.Printf("[ok] config exists: alias=%q relay=%s\n", c.Alias, c.RelayURL)
		}
	}

	// Step 2: offer to write .mcp.json in the current project dir so CC
	// auto-loads clawdchan-mcp. Skip in -y mode (unattended / CI).
	if !*yes && stdinIsTTY() {
		if err := setupProjectMCP(); err != nil {
			fmt.Printf("note: .mcp.json step: %v\n", err)
		}
	}

	// Step 3: PATH wiring.
	if err := cmdPathSetup(nil); err != nil {
		fmt.Printf("path-setup: %v\n", err)
	}

	// Step 4: background daemon.
	if err := daemonSetup(nil); err != nil {
		fmt.Printf("daemon setup: %v\n", err)
	}

	fmt.Println()
	fmt.Println("Setup complete. Next:")
	fmt.Println("  1. Restart Claude Code so it loads the MCP server.")
	fmt.Println("  2. Ask Claude: 'pair me with <friend> via clawdchan'.")
	return nil
}

// setupInit runs the first-time init: prompts for alias + relay with
// defaults ($USER and ws://localhost:8787), creates the data dir, saves
// config, generates the Ed25519 + X25519 identity.
func setupInit(yes bool) error {
	defaultAlias := os.Getenv("USER")
	if defaultAlias == "" {
		defaultAlias = "me"
	}
	defaultRelay := "ws://localhost:8787"

	alias := defaultAlias
	relay := defaultRelay

	if !yes && stdinIsTTY() {
		fmt.Println()
		fmt.Println("First-time ClawdChan setup.")
		fmt.Println("An alias is how you appear to peers when pairing. The relay is the server")
		fmt.Println("that forwards encrypted envelopes between your node and theirs.")
		fmt.Println()
		alias = promptString(fmt.Sprintf("Display alias [%s]: ", defaultAlias), defaultAlias)
		relay = promptString(fmt.Sprintf("Relay URL [%s]: ", defaultRelay), defaultRelay)
		if strings.Contains(relay, "localhost") || strings.Contains(relay, "127.0.0.1") {
			fmt.Println()
			fmt.Println("Note: you picked a localhost relay. That's fine for a solo test,")
			fmt.Println("but peers on other machines can't reach it. Deploy a shared relay")
			fmt.Println("(see docs/deploy.md) once you want to pair with someone else.")
		}
	}

	dataDir := defaultDataDir()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	c := config{DataDir: dataDir, RelayURL: relay, Alias: alias}
	if err := saveConfig(c); err != nil {
		return err
	}
	n, err := node.New(node.Config{DataDir: c.DataDir, RelayURL: c.RelayURL, Alias: c.Alias})
	if err != nil {
		return err
	}
	defer n.Close()
	nid := n.Identity()
	_ = context.Background // keep import live for future use
	fmt.Printf("[ok] initialized node %s (alias=%q relay=%s)\n",
		hex.EncodeToString(nid[:])[:16], c.Alias, c.RelayURL)
	return nil
}

// setupProjectMCP offers to drop a .mcp.json in the current directory so
// Claude Code discovers clawdchan-mcp in projects opened from here. If one
// already exists, we check whether it references clawdchan and either
// confirm or skip (don't stomp the user's config).
func setupProjectMCP() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	dotMCP := filepath.Join(cwd, ".mcp.json")

	if data, err := os.ReadFile(dotMCP); err == nil {
		if strings.Contains(string(data), "clawdchan") {
			fmt.Printf("[ok] %s already wires clawdchan\n", dotMCP)
			return nil
		}
		fmt.Printf("note: %s exists but doesn't include clawdchan. Skipping to avoid overwriting your config.\n", dotMCP)
		return nil
	}

	fmt.Println()
	fmt.Printf("Drop a .mcp.json in %s so Claude Code loads clawdchan-mcp here?\n", cwd)
	ok, err := promptYN("Write .mcp.json in current directory? [Y/n]: ", true)
	if err != nil || !ok {
		if !ok {
			fmt.Println("Skipped. Run `clawdchan init -write-mcp <dir>` later if you change your mind.")
		}
		return nil
	}
	mcpBin, _ := resolveMCPBinary()
	path, err := writeProjectMCP(cwd, mcpBin)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", path)
	fmt.Println("Restart Claude Code to pick up the new MCP server.")
	return nil
}

func promptString(prompt, defaultVal string) string {
	if !stdinIsTTY() {
		return defaultVal
	}
	fmt.Print(prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return defaultVal
		}
		return defaultVal
	}
	ans := strings.TrimSpace(line)
	if ans == "" {
		return defaultVal
	}
	return ans
}
