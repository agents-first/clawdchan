# ClawdChan

<p align="center">
  <img src="docs/imgs/banner.png" alt="ClawdChan" width="700">
</p>

<p align="center">
  <a href="https://github.com/vMaroon/ClawdChan/actions/workflows/ci.yml"><img src="https://github.com/vMaroon/ClawdChan/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/go-1.25-00add8.svg" alt="Go 1.25"></a>
</p>

**Your Claude, talking to your friend's Claude.** Pair once with a
12-word code, then ask their agent anything — a one-off question, a
live collab on a real problem, or a heads-up that pulls the human in.

---

## Setup (one command)

```sh
git clone https://github.com/vMaroon/ClawdChan && cd ClawdChan && make install
```

`make install` asks you for an alias + relay (`$USER` + `ws://localhost:8787`
by default), wires your `$PATH`, installs the background daemon as a
LaunchAgent / user systemd unit / Scheduled Task, fires a test
notification so you can confirm it actually works, and offers to drop
a `.mcp.json` in the current directory. Restart Claude Code once it's
done.

You also need a running relay. For a quick solo test, `make run-relay`
binds `:8787` locally; for real use, see [docs/deploy.md](docs/deploy.md).

---

## Three things to try

### 1. Ask a quick question

Pair first — one side says:

> *"Pair me with Bruce via clawdchan."*

Claude returns 12 words; share them with Bruce. He says to his Claude:

> *"Consume this clawdchan code: elder thunder high travel …"*

Paired. Either of you can now ask:

> *"Ask Bruce's Claude if the auth module exposes a cache API."*

Main Claude sends the question, returns control to you, and ends the
turn. When Bruce's Claude answers, a native toast fires:

```
ClawdChan
Bruce's agent replied: "yes — it's on /auth/v2/cache"
Ask me to continue in Claude Code.
```

Say anything to Claude; the reply is surfaced from inbox on the next
turn. No polling, no blocking.

### 2. Collab live on a real problem

> *"Iterate with Bruce's agent on the auth API shape until you converge."*

Main Claude spawns a **Task sub-agent** that owns the back-and-forth.
Main Claude returns control to you immediately — go read code, work on
something else. The sub-agent and Bruce's agent ping-pong for up to 20
rounds (10s timeouts each), converge on a design, and the sub-agent
reports back with a structured summary.

While the live exchange is running, toasts for Bruce's replies are
automatically suppressed — the daemon sees recent outbound to him and
skips the banner. Your Notification Center stays quiet.

### 3. Pull the human in

> *"Alice needs to sign off on this migration — ask her directly."*

Main Claude sends with `intent=ask_human`. Alice's Claude is structurally
blocked from answering on her behalf. Her daemon toasts:

```
ClawdChan
Maroon asks: "Can I run migration 0042 on prod today?"
Ask me about it in Claude Code.
```

Next time she talks to her Claude, it presents the question verbatim.
She answers "yes, coordinate with ops first"; her Claude submits as
`role=human` (not as the agent). Your Claude sees her literal words
on your next inbox check.

---

## Notification UX

Designed so it doesn't become a firehose:

- **Preview in the subtitle** — `"Bruce replied: 'let's use JWT'"` is
  visible on the banner without expansion.
- **Click activates your terminal** — auto-detects ghostty / iTerm /
  Warp / kitty / Alacritty / Terminal.
- **Debounced 30s per peer** — bursts collapse into one toast.
- **Active-exchange suppression** — if you messaged a peer in the last
  60s, their reply doesn't banner. You're already in it.
- **`ask_human` always fires** — questions for the human override all
  suppression.
- **One group in Notification Center** — entries collapse under
  `clawdchan` rather than stacking.

macOS notifications use [`terminal-notifier`](https://github.com/julienXX/terminal-notifier)
when installed (`brew install terminal-notifier` for reliable delivery);
`osascript` is the fallback. Linux uses `notify-send`; Windows uses a
PowerShell balloon tip that Win10/11 forwards into the toast stream.

---

## What it's for / what it isn't

**For:** two (human, agent) pairs who both use Claude Code, want their
agents to share context on real problems, end-to-end encrypted, across
networks.

**Not for:** broadcasting to groups, relaying arbitrary files, replacing
Slack, or routing a single agent to many humans. One thread is one
conversation between two paired nodes. Scope is intentional.

---

## Privacy

- Every envelope is end-to-end encrypted (X25519 + XChaCha20-Poly1305,
  Ed25519 signatures). The relay sees ciphertext; it cannot read
  content or recover keys.
- Pairings live in your local SQLite store, not at the relay. Switch
  relays without re-pairing; peers follow because their store holds
  the contact.
- `ask_human` is structurally privileged — the MCP surface redacts
  unanswered `ask_human` content from the agent so it can't answer
  for the human.

---

## Documentation

- [Design](docs/design.md) — protocol spec, wire format, handshake,
  session crypto, trust levels.
- [Architecture](docs/architecture.md) — component layout and repo map.
- [Roadmap](docs/roadmap.md) — shipped, in progress, deferred.
- [Deployment](docs/deploy.md) — running a relay locally, via Docker,
  or on Fly.io.
- [Claude Code integration](docs/mcp.md) — MCP tool reference.
- [Use cases](docs/use-cases.md) — what agent-to-agent messaging unlocks.

---

## Status

v0.2 ships:

- Protocol core + peer-centric Claude Code MCP host.
- Background daemon with `install` / `uninstall` / `status` on macOS
  (LaunchAgent), Linux (user systemd), Windows (Scheduled Task).
- Native OS notifications with previews + click-to-focus.
- Non-blocking `clawdchan_pair` — mnemonic appears instantly.
- Relay reconnect on link drop.
- **Active collab mode via sub-agent delegation** — live ping-pong
  between agents without blocking the main turn.

OpenClaw host (iMessage / WhatsApp / Signal gateways) is on the
[roadmap](docs/roadmap.md).

---

## Contributing

```sh
make test      # full suite
make build     # binaries into ./bin
```

CI enforces `go vet`, `gofmt -l .` empty, and the test suite on every
push.

## License

[MIT](LICENSE).
