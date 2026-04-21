package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/agents-first/ClawdChan/core/node"
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

	fmt.Println("🐾 ClawdChan setup — 5 steps: identity, MCP config, PATH, OpenClaw, daemon.")

	// Step 1: identity + config. If config already exists, offer to
	// refresh alias/relay — the signing keys load from the existing store,
	// so re-running doesn't regenerate identity or drop pairings.
	stepHeader(1, "Identity")
	cfgPath := filepath.Join(defaultDataDir(), configFileName)
	if _, err := os.Stat(cfgPath); err != nil {
		if err := setupInit(*yes); err != nil {
			return fmt.Errorf("init: %w", err)
		}
	} else {
		c, err := loadConfig()
		if err == nil {
			fmt.Printf("  [ok] configured: alias=%q relay=%s\n", c.Alias, c.RelayURL)
			if !*yes && stdinIsTTY() {
				redo, _ := promptYN("  Update alias/relay? [y/N]: ", false)
				if redo {
					if err := setupInit(false); err != nil {
						return fmt.Errorf("reconfigure: %w", err)
					}
				}
			}
		}
	}

	// warnings accumulate non-fatal issues from steps 2-5 so we can surface
	// them together at the end and nudge the user toward `clawdchan doctor`
	// instead of unconditionally claiming success.
	var warnings []string

	// Step 2: offer to write .mcp.json in the current project dir so CC
	// auto-loads clawdchan-mcp. Skip in -y mode (unattended / CI).
	stepHeader(2, "Claude Code project config")
	if !*yes && stdinIsTTY() {
		if err := setupProjectMCP(); err != nil {
			fmt.Printf("note: .mcp.json step: %v\n", err)
			warnings = append(warnings, fmt.Sprintf("Claude Code project config: %v", err))
		}
	} else {
		fmt.Println("(non-interactive — skipped. Run `clawdchan init -write-mcp <dir>` to add later.)")
	}

	// Step 3: PATH wiring.
	stepHeader(3, "PATH")
	if err := cmdPathSetup(nil); err != nil {
		fmt.Printf("path-setup: %v\n", err)
		warnings = append(warnings, fmt.Sprintf("PATH: %v", err))
	}

	// Step 4: optional OpenClaw wiring. Flags win over prompt. In -y mode with
	// no flags, we leave the existing config alone — there is no sensible
	// default OpenClaw URL/token to auto-fill.
	stepHeader(4, "OpenClaw gateway (optional)")
	if err := setupOpenClaw(*yes, *openClawURL, *openClawToken, *openClawDeviceID); err != nil {
		fmt.Printf("openclaw setup: %v\n", err)
		warnings = append(warnings, fmt.Sprintf("OpenClaw: %v", err))
	}

	// Step 5: background daemon. Runs last so the service unit it writes
	// picks up the OpenClaw fields we just saved to config.
	stepHeader(5, "Background daemon")
	if err := daemonSetup(nil); err != nil {
		fmt.Printf("daemon setup: %v\n", err)
		warnings = append(warnings, fmt.Sprintf("daemon: %v", err))
	}

	fmt.Println()
	if len(warnings) > 0 {
		fmt.Printf("⚠ Setup finished with %d issue(s):\n", len(warnings))
		for _, w := range warnings {
			fmt.Printf("  - %s\n", w)
		}
		fmt.Println()
		fmt.Println("Run `clawdchan doctor` to diagnose, then re-run `clawdchan setup` if needed.")
		fmt.Println("Next:")
	} else {
		fmt.Println("✅ Setup complete. Next:")
	}
	fmt.Println("  1. Restart Claude Code so it loads the MCP server.")
	fmt.Println(`  2. Ask Claude: "pair me with <friend> via clawdchan."`)
	if c, err := loadConfig(); err == nil && c.OpenClawURL != "" && daemonAlreadyInstalled() {
		fmt.Println("  3. OpenClaw config changed — restart the daemon to pick it up:")
		fmt.Println("     clawdchan daemon install -force")
	}
	// OpenClaw hub guide — printed in addition to the Claude Code steps above
	// so existing users don't lose the short notice, and new OpenClaw users
	// see how to drive the hub from natural language.
	if c, _ := loadConfig(); c.OpenClawURL != "" {
		fmt.Println()
		fmt.Println("  OpenClaw:")
		fmt.Println("    1. Open OpenClaw — you'll see a session named \"clawdchan:hub\".")
		fmt.Println("    2. To start a pairing, just say: \"pair me with someone on clawdchan\".")
		fmt.Println("       Your agent replies with 12 words. Send them to your friend.")
		fmt.Println("    3. When your friend pastes their 12 words, say: \"consume these: <words>\".")
		fmt.Println("    4. A new session for that peer appears automatically — talk there to chat.")
		fmt.Println("    Other things you can say in the hub: \"who am I paired with\", \"any new messages\".")
	}
	return nil
}

// stepHeader prints a visual break + numbered title between setup stages.
// Keeps the overall flow declarative: each section announces itself before
// prompting or reporting. Total is hardcoded at 5 — matches cmdSetup's
// step count and moves together if a stage is ever added or removed.
func stepHeader(n int, title string) {
	fmt.Println()
	fmt.Printf("Step %d of 5 — %s\n", n, title)
}

// setupOpenClaw wires the optional OpenClaw gateway. Flags override prompts;
// in -y mode without flags we silently keep whatever is already saved.
// Passing -openclaw-url=none disables OpenClaw by clearing the config entry.
// This step never touches Claude Code configuration.
func setupOpenClaw(yes bool, flagURL, flagToken, flagDeviceID string) (err error) {
	defer func() {
		if err == nil {
			c, loadErr := loadConfig()
			if loadErr == nil && c.OpenClawURL != "" {
				deployOpenClawAssets(yes)
			}
		}
	}()

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

	// -y with no flags: auto-discover if nothing configured, else keep.
	if yes || !stdinIsTTY() {
		if c.OpenClawURL != "" {
			fmt.Printf("[ok] OpenClaw gateway: %s (unchanged)\n", c.OpenClawURL)
			return nil
		}
		ws, tok, _ := discoverOpenClaw(context.Background())
		if ws != "" {
			c.OpenClawURL = ws
			c.OpenClawToken = tok
			if c.OpenClawDeviceID == "" {
				c.OpenClawDeviceID = "clawdchan-daemon"
			}
			if err := saveConfig(c); err != nil {
				return err
			}
			fmt.Printf("[ok] OpenClaw auto-discovered: %s (device=%s)\n", ws, c.OpenClawDeviceID)
		}
		return nil
	}

	// Interactive: try auto-discovery first, fall back to manual.
	fmt.Println()
	if c.OpenClawURL != "" {
		fmt.Printf("  OpenClaw gateway configured: %s\n", c.OpenClawURL)
		ok, err := promptYN("  Reconfigure or disable? [y/N]: ", false)
		if err != nil || !ok {
			return nil
		}
	}

	fmt.Print("Checking for OpenClaw gateway... ")
	ws, tok, _ := discoverOpenClaw(context.Background())
	if ws != "" {
		fmt.Printf("found at %s\n", ws)
		ok, err := promptYN("Use auto-detected gateway? [Y/n]: ", true)
		if err == nil && ok {
			c.OpenClawURL = ws
			c.OpenClawToken = tok
			if c.OpenClawDeviceID == "" {
				c.OpenClawDeviceID = "clawdchan-daemon"
			}
			if err := saveConfig(c); err != nil {
				return err
			}
			fmt.Printf("[ok] OpenClaw gateway: %s (device=%s)\n", ws, c.OpenClawDeviceID)
			return nil
		}
		// User declined auto-detect — fall through to manual.
	} else {
		fmt.Println("not found.")
	}

	fmt.Println("OpenClaw integration (optional) — routes inbound envelopes into")
	fmt.Println("OpenClaw sessions alongside Claude Code.")
	fmt.Println("Your existing Claude Code setup is NOT touched either way.")
	ok, err := promptYN("Configure OpenClaw manually? [y/N]: ", false)
	if err != nil || !ok {
		if c.OpenClawURL == "" {
			fmt.Println("Skipped. The daemon will auto-detect at startup if available.")
		}
		return nil
	}

	ocURL := promptString("OpenClaw gateway URL (ws:// or wss://, or 'none' to disable): ", c.OpenClawURL)
	if strings.EqualFold(ocURL, "none") || ocURL == "" {
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

	c.OpenClawURL = ocURL
	c.OpenClawToken = token
	c.OpenClawDeviceID = device
	if err := saveConfig(c); err != nil {
		return err
	}
	fmt.Printf("[ok] OpenClaw gateway: %s (device=%s)\n", ocURL, device)
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
	defaultRelay := defaultPublicRelay
	// If we already have a config, pre-fill its values as the defaults so
	// a redo shows current settings in brackets rather than the cold
	// fresh-install defaults.
	if existing, err := loadConfig(); err == nil {
		if existing.Alias != "" {
			defaultAlias = existing.Alias
		}
		if existing.RelayURL != "" {
			defaultRelay = existing.RelayURL
		}
	}

	alias := defaultAlias
	relay := defaultRelay

	if !yes && stdinIsTTY() {
		fmt.Println("  Alias = how you appear to peers. Relay = server that forwards")
		fmt.Println("  encrypted envelopes (ciphertext only; default is a convenience")
		fmt.Println("  instance on fly.io, no SLA — deploy your own for prod: docs/deploy.md).")
		alias = promptString(fmt.Sprintf("  Display alias [%s]: ", defaultAlias), defaultAlias)
		relay = promptString(fmt.Sprintf("  Relay URL [%s]: ", defaultRelay), defaultRelay)
		if strings.Contains(relay, "localhost") || strings.Contains(relay, "127.0.0.1") {
			fmt.Println("  note: localhost relay isn't reachable by peers on other machines.")
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
		if !strings.Contains(string(data), "clawdchan") {
			fmt.Printf("  note: %s exists but doesn't include clawdchan. Skipping to avoid overwriting your config.\n", dotMCP)
			return nil
		}
		fmt.Printf("  [ok] %s already wires clawdchan\n", dotMCP)
		redo, _ := promptYN("  Rewrite with current clawdchan-mcp path? [y/N]: ", false)
		if !redo {
			return nil
		}
		// fall through to the write path below
	} else {
		fmt.Printf("  Drop a .mcp.json in %s so Claude Code loads clawdchan-mcp here?\n", cwd)
		ok, err := promptYN("  Write .mcp.json? [Y/n]: ", true)
		if err != nil || !ok {
			if !ok {
				fmt.Println("  Skipped. Run `clawdchan init -write-mcp <dir>` later if you change your mind.")
			}
			return nil
		}
	}

	mcpBin, _ := resolveMCPBinary()
	path, err := writeProjectMCP(cwd, mcpBin)
	if err != nil {
		return err
	}
	fmt.Printf("  [ok] wrote %s — restart Claude Code to pick up the new MCP server.\n", path)
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

// clawdchanGuideMarkdown is deployed to each OpenClaw agent workspace as
// CLAWDCHAN_GUIDE.md during `clawdchan setup`. Its content is the operator
// manual for an agent using the ClawdChan MCP tools — rules of conduct,
// not a tool catalog. Keep it in sync with
// hosts/claudecode/plugin/commands/clawdchan.md, which is the same
// content presented as a Claude Code slash command (frontmatter +
// $ARGUMENTS). agent_guide_sync_test.go enforces that the behavioral
// body of both files matches; change them together.
const clawdchanGuideMarkdown = "# ClawdChan agent guide\n\n" +
	"You have the ClawdChan MCP tools (`clawdchan_*`). The surface is\n" +
	"peer-centric: you message a peer, read an aggregate inbox, reply to a\n" +
	"peer. Thread IDs never surface. This file is your operator manual —\n" +
	"how to act, not what the tools do.\n\n" +
	"## First action every session\n\n" +
	"Call `clawdchan_toolkit`. It returns a `setup.user_message`. If\n" +
	"`setup.needs_persistent_listener` is true, surface that message\n" +
	"verbatim and pause — a running `clawdchan daemon` is what fires the\n" +
	"OS toasts that pull the user back into this session when a peer\n" +
	"messages them. Without it, inbound only arrives while this session is\n" +
	"open, and nothing notifies the user.\n\n" +
	"## Conduct rules\n\n" +
	"**Classify every send as one-shot or live.** Before calling\n" +
	"`clawdchan_message`, decide which of two modes fits the intent:\n\n" +
	"- **One-shot** — announce, handoff, single question, anything that\n" +
	"  makes sense as fire-and-forget. Call `clawdchan_message`, tell the\n" +
	"  user what you sent, end the turn. The call is non-blocking even for\n" +
	"  `intent=ask`; the reply arrives later via the daemon's OS toast and\n" +
	"  `clawdchan_inbox`. The main agent does not poll.\n\n" +
	"- **Live collaboration** — iterative back-and-forth the user expects\n" +
	"  (`\"iterate with her agent until you converge\"`, `\"work it out with\n" +
	"  Bruce\"`, `\"both our Claudes are on this\"`). Always confirm with the\n" +
	"  user before starting:\n\n" +
	"  > This looks iterative — try live with `<peer>` now, or send\n" +
	"  > one-shot and wait for their turn?\n\n" +
	"  On **live**, delegate to a Task sub-agent. Do NOT run the loop on\n" +
	"  your own turn; it freezes the user. Brief the sub-agent:\n\n" +
	"  > You own a live ClawdChan collaboration with peer_id `<hex>` about\n" +
	"  > `<problem>`. First action is a **liveness probe**:\n" +
	"  > `clawdchan_message(peer, text=\"<one-line 'live on <topic>?'\n" +
	"  > check>\", intent='ask', collab=true)` →\n" +
	"  > `clawdchan_subagent_await(peer, timeout_seconds=15,\n" +
	"  > since_ms=<now>)`. If the probe times out, exit with \"peer not\n" +
	"  > live on this\" — do not keep sending. If the probe returns, enter\n" +
	"  > the loop: `clawdchan_message(peer, text, intent='ask',\n" +
	"  > collab=true)` → `clawdchan_subagent_await(peer,\n" +
	"  > timeout_seconds=<T>, since_ms=<last now_ms>)` → integrate →\n" +
	"  > respond. Size `<T>` to the work: ~10s for quick clarifications,\n" +
	"  > 30–60s for design-level turns — not a fixed 10s. Converge on\n" +
	"  > `<definition of done>`. Stop after `<N>` rounds, 2–3 consecutive\n" +
	"  > timeouts (\"peer went silent\"), or any error. Return a summary:\n" +
	"  > what was agreed, open questions, closing message. Do not ask the\n" +
	"  > user anything. Always set `collab=true` on outbound — that tags\n" +
	"  > the envelope so the peer knows a sub-agent is waiting.\n\n" +
	"  Free the main turn. Tell the user the loop is running; you'll\n" +
	"  surface the result when it converges or the probe fails. If the\n" +
	"  probe reports \"not live\", tell the user and offer a one-shot send\n" +
	"  as the fallback.\n\n" +
	"**Receiving a live-collab invite requires consent.** When\n" +
	"`clawdchan_inbox` returns an envelope with `collab=true` you didn't\n" +
	"initiate, the peer has a sub-agent waiting (~10s replies). Ask the\n" +
	"user first:\n\n" +
	"> X's agent is waiting live: *\"<preview>\"*. Engage live (I'll spawn my\n" +
	"> own sub-agent) or handle at your pace?\n\n" +
	"Live → spawn a Task sub-agent with the same loop shape, skipping the\n" +
	"probe (the peer already opened the channel).\n" +
	"Paced → reply once with `clawdchan_message` (no `collab=true`); the\n" +
	"sender's sub-agent detects the slower cadence and closes cleanly.\n\n" +
	"**ask_human is not yours to answer.** Items in\n" +
	"`clawdchan_inbox.pending_asks` are peer questions waiting for your\n" +
	"user. Present the content verbatim. Do not paraphrase, summarize, or\n" +
	"answer. Then:\n" +
	"- `clawdchan_reply(peer_id, text)` — submit the user's literal words.\n" +
	"- `clawdchan_decline(peer_id, reason?)` — when the user declines.\n\n" +
	"**Mnemonics go to the user verbatim, on their own line.**\n" +
	"`clawdchan_pair` returns a 12-word mnemonic. Surface it on its own\n" +
	"line in your response — never summarize or hide it. Tell the user to\n" +
	"share it only over a trusted channel (voice, Signal, in person); the\n" +
	"channel is the security boundary. The mnemonic looks like a BIP-39\n" +
	"wallet seed but is a one-time rendezvous code. Do not loop on\n" +
	"`clawdchan_peers` to \"confirm\" before the user has passed the code on\n" +
	"— pairing takes minutes end-to-end.\n\n" +
	"**Consuming closes pairing.** `clawdchan_consume(mnemonic)` completes\n" +
	"the pairing. Do not instruct the user to compare the 4-word SAS —\n" +
	"that's optional belt-and-braces fingerprinting, only surface it if\n" +
	"they explicitly ask.\n\n" +
	"**Destructive peer ops are CLI-only.** If the user wants to revoke\n" +
	"trust or hard-delete a peer, tell them to run\n" +
	"`clawdchan peer revoke <ref>` or `clawdchan peer remove <ref>` in a\n" +
	"terminal. You do not have a tool for these — that's intentional.\n" +
	"`clawdchan_peer_rename` is the only peer-management tool you own.\n\n" +
	"## Intents\n\n" +
	"- `say` (default): agent→agent FYI.\n" +
	"- `ask`: agent→agent; peer's agent replies.\n" +
	"- `notify_human`: FYI for the peer's human.\n" +
	"- `ask_human`: peer's human must answer; their agent is blocked from\n" +
	"  replying in their place.\n\n" +
	"## Tool reference\n\n" +
	"Call `clawdchan_toolkit` for the runtime capability list and current\n" +
	"setup state. Arg-level detail on every tool is in each tool's MCP\n" +
	"description.\n"

func deployOpenClawAssets(yes bool) {
	deployOpenClawAgentAssets()
	_ = registerClawdChanMCP()

	// The gateway caches scopes at connect-time, so a rewrite demands an
	// unconditional restart — bypass the interactive prompt in that branch.
	scopesChanged, err := ensureOpenClawOperatorScopes()
	if err != nil {
		fmt.Printf("[warn] could not update OpenClaw operator scopes: %v\n", err)
	} else if scopesChanged {
		fmt.Println("[ok] OpenClaw operator scopes updated (added operator.write + operator.admin)")
		fmt.Print("Restarting OpenClaw gateway to apply scopes... ")
		if err := exec.Command("openclaw", "gateway", "restart").Run(); err != nil {
			fmt.Printf("failed: %v\n", err)
			fmt.Println("    Run `openclaw gateway restart` manually, then reconnect subagents.")
		} else {
			fmt.Println("done.")
		}
		return
	}

	if !yes && stdinIsTTY() {
		restartOpenClawGateway()
	}
}

func deployOpenClawAgentAssets() {
	home, _ := os.UserHomeDir()
	ocPath := filepath.Join(home, ".openclaw", "openclaw.json")
	data, err := os.ReadFile(ocPath)
	if err != nil {
		return
	}

	var ocConfig struct {
		Agents struct {
			Defaults struct {
				Workspace string `json:"workspace"`
			} `json:"defaults"`
			List []struct {
				Workspace string `json:"workspace"`
			} `json:"list"`
		} `json:"agents"`
	}

	if err := json.Unmarshal(data, &ocConfig); err != nil {
		return
	}

	workspaces := make(map[string]bool)
	if ocConfig.Agents.Defaults.Workspace != "" {
		workspaces[ocConfig.Agents.Defaults.Workspace] = true
	}
	for _, a := range ocConfig.Agents.List {
		if a.Workspace != "" {
			workspaces[a.Workspace] = true
		}
	}

	count := 0
	for ws := range workspaces {
		guidePath := filepath.Join(ws, "CLAWDCHAN_GUIDE.md")
		_ = os.WriteFile(guidePath, []byte(clawdchanGuideMarkdown), 0o644)

		soulPath := filepath.Join(ws, "SOUL.md")
		soulData, err := os.ReadFile(soulPath)
		if err == nil && !strings.Contains(string(soulData), "ClawdChan") {
			f, err := os.OpenFile(soulPath, os.O_APPEND|os.O_WRONLY, 0o644)
			if err == nil {
				_, _ = f.WriteString("\n\n## ClawdChan\n\nYou are a ClawdChan-enabled agent. You can communicate securely with other agents and humans globally. Use `clawdchan_pair` to connect with new peers and `clawdchan_message` to talk to them. Refer to `CLAWDCHAN_GUIDE.md` for tool specifics.\n")
				f.Close()
			}
		}
		count++
	}

	if count > 0 {
		fmt.Printf("[ok] Deployed ClawdChan guide to %d OpenClaw agent workspace(s)\n", count)
	}
}

func registerClawdChanMCP() error {
	mcpBin, err := resolveMCPBinary()
	if err != nil {
		return err
	}

	// Create JSON command object for 'openclaw mcp set'
	cmdObj := map[string]string{
		"command": mcpBin,
	}
	raw, _ := json.Marshal(cmdObj)

	cmd := exec.Command("openclaw", "mcp", "set", "clawdchan", string(raw))
	if err := cmd.Run(); err != nil {
		return err
	}
	fmt.Println("[ok] Registered ClawdChan MCP server in OpenClaw")
	return nil
}

func restartOpenClawGateway() {
	fmt.Println()
	fmt.Println("OpenClaw configuration has changed.")
	ok, err := promptYN("Restart OpenClaw Gateway now to apply changes? [Y/n]: ", true)
	if err != nil || !ok {
		fmt.Println("Skipped restart. Remember to run `openclaw gateway restart` later.")
		return
	}

	fmt.Print("Restarting OpenClaw gateway... ")
	cmd := exec.Command("openclaw", "gateway", "restart")
	if err := cmd.Run(); err != nil {
		fmt.Printf("failed: %v\n", err)
		return
	}
	fmt.Println("done.")
}
