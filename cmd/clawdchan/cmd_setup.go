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
// the optional OpenClaw gateway configuration, and the background daemon
// install — all with inline prompts and sane defaults. Each step is
// idempotent and skips cleanly when already done, so rerunning is safe.
// Everything Claude Code already has is preserved; OpenClaw is purely
// additive.
func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	yes := fs.Bool("y", false, "assume yes; use defaults everywhere and skip all prompts")
	// OpenClaw flags let scripted installers (CI, `make install-openclaw`)
	// configure the gateway without a TTY. Passing -openclaw-url=none clears
	// a previously-saved setting so a machine can be taken out of OpenClaw
	// mode without editing config.json by hand.
	openClawURL := fs.String("openclaw-url", "", "OpenClaw gateway URL (ws:// or wss://); pass 'none' to disable")
	openClawToken := fs.String("openclaw-token", "", "OpenClaw gateway bearer token")
	openClawDeviceID := fs.String("openclaw-device-id", "", "OpenClaw device id (default: clawdchan-daemon)")
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

	// Step 4: optional OpenClaw wiring. Flags win over prompt. In -y mode with
	// no flags, we leave the existing config alone — there is no sensible
	// default OpenClaw URL/token to auto-fill.
	if err := setupOpenClaw(*yes, *openClawURL, *openClawToken, *openClawDeviceID); err != nil {
		fmt.Printf("openclaw setup: %v\n", err)
	}

	// Step 5: background daemon. Runs last so the service unit it writes
	// picks up the OpenClaw fields we just saved to config.
	if err := daemonSetup(nil); err != nil {
		fmt.Printf("daemon setup: %v\n", err)
	}

	fmt.Println()
	fmt.Println("Setup complete. Next:")
	fmt.Println("  1. Restart Claude Code so it loads the MCP server.")
	fmt.Println("  2. Ask Claude: 'pair me with <friend> via clawdchan'.")
	if c, err := loadConfig(); err == nil && c.OpenClawURL != "" && daemonAlreadyInstalled() {
		fmt.Println("  3. OpenClaw config changed — restart the daemon to pick it up:")
		fmt.Println("     clawdchan daemon install -force")
	}
	return nil
}

// setupOpenClaw wires the optional OpenClaw gateway. Flags override prompts;
// in -y mode without flags we silently keep whatever is already saved.
// Passing -openclaw-url=none disables OpenClaw by clearing the config entry.
// This step never touches Claude Code configuration.
func setupOpenClaw(yes bool, flagURL, flagToken, flagDeviceID string) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}

	// Non-interactive: explicit flags provided.
	if flagURL != "" {
		if strings.EqualFold(flagURL, "none") {
			if c.OpenClawURL == "" {
				fmt.Println("[ok] OpenClaw already disabled")
				return nil
			}
			c.OpenClawURL = ""
			c.OpenClawToken = ""
			c.OpenClawDeviceID = ""
			if err := saveConfig(c); err != nil {
				return err
			}
			fmt.Println("[ok] OpenClaw cleared from config")
			return nil
		}
		c.OpenClawURL = flagURL
		if flagToken != "" {
			c.OpenClawToken = flagToken
		}
		if flagDeviceID != "" {
			c.OpenClawDeviceID = flagDeviceID
		} else if c.OpenClawDeviceID == "" {
			c.OpenClawDeviceID = "clawdchan-daemon"
		}
		if err := saveConfig(c); err != nil {
			return err
		}
		fmt.Printf("[ok] OpenClaw gateway: %s (device=%s)\n", c.OpenClawURL, c.OpenClawDeviceID)
		return nil
	}

	// -y with no flags: leave it as-is.
	if yes || !stdinIsTTY() {
		if c.OpenClawURL != "" {
			fmt.Printf("[ok] OpenClaw gateway: %s (unchanged)\n", c.OpenClawURL)
		}
		return nil
	}

	// Interactive: explain, ask, prompt.
	fmt.Println()
	if c.OpenClawURL != "" {
		fmt.Printf("OpenClaw is currently enabled: %s\n", c.OpenClawURL)
		ok, err := promptYN("Reconfigure or disable OpenClaw? [y/N]: ", false)
		if err != nil || !ok {
			fmt.Println("Leaving OpenClaw settings unchanged.")
			return nil
		}
	} else {
		fmt.Println("OpenClaw integration (optional) — routes inbound envelopes into")
		fmt.Println("OpenClaw sessions alongside Claude Code. If you're not running an")
		fmt.Println("OpenClaw gateway on this machine, skip this.")
		fmt.Println("Your existing Claude Code setup is NOT touched either way.")
		ok, err := promptYN("Configure OpenClaw now? [y/N]: ", false)
		if err != nil || !ok {
			fmt.Println("Skipped. Run `clawdchan setup -openclaw-url=... -openclaw-token=...` later.")
			return nil
		}
	}

	url := promptString("OpenClaw gateway URL (ws:// or wss://, or 'none' to disable): ", c.OpenClawURL)
	if strings.EqualFold(url, "none") || url == "" {
		c.OpenClawURL = ""
		c.OpenClawToken = ""
		c.OpenClawDeviceID = ""
		if err := saveConfig(c); err != nil {
			return err
		}
		fmt.Println("[ok] OpenClaw disabled")
		return nil
	}
	token := promptString("OpenClaw bearer token: ", c.OpenClawToken)
	defaultDevice := c.OpenClawDeviceID
	if defaultDevice == "" {
		defaultDevice = "clawdchan-daemon"
	}
	device := promptString(fmt.Sprintf("Device id [%s]: ", defaultDevice), defaultDevice)

	c.OpenClawURL = url
	c.OpenClawToken = token
	c.OpenClawDeviceID = device
	if err := saveConfig(c); err != nil {
		return err
	}
	fmt.Printf("[ok] OpenClaw gateway: %s (device=%s)\n", url, device)
	return nil
}

// setupInit runs the first-time init: prompts for alias + relay with
// defaults ($USER and the vMaroon-hosted fly.io convenience relay),
// creates the data dir, saves config, generates the Ed25519 + X25519
// identity.
func setupInit(yes bool) error {
	defaultAlias := os.Getenv("USER")
	if defaultAlias == "" {
		defaultAlias = "me"
	}

	alias := defaultAlias
	relay := defaultPublicRelay

	if !yes && stdinIsTTY() {
		fmt.Println()
		fmt.Println("First-time ClawdChan setup.")
		fmt.Println("An alias is how you appear to peers when pairing. The relay is the server")
		fmt.Println("that forwards encrypted envelopes between your node and theirs. The default")
		fmt.Println("is a convenience relay we host on fly.io — it sees ciphertext only, but has")
		fmt.Println("no SLA. For stable or production use, deploy your own (docs/deploy.md).")
		fmt.Println()
		alias = promptString(fmt.Sprintf("Display alias [%s]: ", defaultAlias), defaultAlias)
		relay = promptString(fmt.Sprintf("Relay URL [%s]: ", defaultPublicRelay), defaultPublicRelay)
		if strings.Contains(relay, "localhost") || strings.Contains(relay, "127.0.0.1") {
			fmt.Println()
			fmt.Println("Note: you picked a localhost relay. Peers on other machines can't reach")
			fmt.Println("it. Use the default public relay or deploy your own (docs/deploy.md).")
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
