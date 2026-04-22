# ClawdChan — Phased Plan

## Phase 0 — Protocol core (shipped)

Status: **done.**

- `core/identity` — Ed25519 + X25519 keypair, JSON-persisted
- `core/envelope` — deterministic CBOR, Ed25519 sign/verify
- `core/session` — per-peer XChaCha20-Poly1305 keyed from X25519 DH
- `core/pairing` — 128-bit code → 12-word BIP39 mnemonic, AEAD card exchange
  over relay `/pair`, SAS derived from transcript
- `core/transport` — WebSocket client to relay, signed `/link` auth
- `core/store` — SQLite persistence (identity, peers, threads, envelopes,
  outbox)
- `core/policy` — minimal allowlist / revoke gate
- `core/surface` — HumanSurface / AgentSurface contracts + Nop defaults
- `core/node` — wired entrypoint with pair / consume / open / send /
  subscribe / submit_human_reply (inbox is cursor-based, not poll-based)
- `internal/relayserver` — reference relay: `/link`, `/pair`, `/healthz`
- `cmd/clawdchan` — CLI: init / whoami / pair / consume / peers / threads /
  open / send / listen / inspect
- `cmd/clawdchan-relay` — relay binary
- Tests: unit at every layer; integration over live relay; Node round-trip
  including `AskHuman`; CLI end-to-end compile-and-drive test.

## Phase 1 — Claude Code host (shipped)

Status: **done.**

- `hosts/claudecode/` — HumanSurface, AgentSurface, MCP tool surface
- `cmd/clawdchan-mcp` — stdio MCP server binary launched by Claude Code
- `hosts/claudecode/plugin/` — `.mcp.json` + plugin manifest +
  `commands/clawdchan.md`

Tool surface (collapsed to 4 tools): `clawdchan_toolkit` (state + paired
peers), `clawdchan_pair` (generate/consume mnemonic), `clawdchan_message`
(send; `as_human=true` answers a standing ask, `collab=true` marks a
live-exchange invite), `clawdchan_inbox` (cursor-based read; with `peer_id`
+ `wait_seconds` up to 60 it's the live-collab await primitive). Thread IDs
are not exposed — the host resolves peer→thread internally. Destructive /
per-peer ops (rename, revoke, remove) are CLI-only.

CC host is reactive: remote `AskHuman` is stored and surfaced on the user's
next CC turn via `clawdchan_inbox`'s `pending_asks` field. The MCP server
surfaces the content there specifically so Claude can present it to the
user; it is omitted from the main envelopes list until answered. Only
`clawdchan_message(..., as_human=true)` closes the ask — with the user's
literal words to reply, or `text="[declined] <reason>"` to decline. For
ambient OS-level notifications, Phase 1.5 is now shipped — `clawdchan
daemon`.

Install-time ergonomics: `clawdchan init -write-mcp <dir>` drops a
`.mcp.json` pre-wired to the absolute `clawdchan-mcp` path; `clawdchan
doctor` validates binary, config, identity, and relay in one shot.

## Phase 1.5 — Always-on daemon (shipped as v0.2)

Status: **done.**

- `clawdchan daemon run` holds the relay link, drains the outbox,
  classifies inbound (new session vs. continuation), and fires native OS
  notifications — osascript on darwin (with `sound name "default"`),
  notify-send on linux, PowerShell balloon tips via `System.Windows.Forms`
  on windows. Debounced per peer within 30s.
- `clawdchan daemon install` / `uninstall` / `status` register the daemon
  as a LaunchAgent (`~/Library/LaunchAgents/com.vmaroon.clawdchan.daemon.plist`,
  darwin), a user systemd unit (`~/.config/systemd/user/clawdchan-daemon.service`,
  linux), or a Scheduled Task (`ClawdChan Daemon`, windows with
  `ONLOGON`/`LIMITED`). No terminal window required.
- The MCP server checks the listener registry at startup and skips its own
  relay connect when a daemon is present — writes outbound to the shared
  SQLite outbox for the daemon to drain (up to 10s tick). Falls back to
  owning the relay link when no daemon is present.
## Phase 2 — OpenClaw host (shipped)

Status: **done.**

OpenClaw is a persistent personal-agent runtime with messenger-gateway
integrations (WhatsApp, Signal, iMessage, etc.). The ClawdChan daemon
now doubles as an OpenClaw *operator client* over the Gateway Protocol
WebSocket: each paired peer is mapped to one OpenClaw session, so
`NotifyHuman`, `AskHuman`, and agent-facing envelopes are rendered into
the peer's session while OS-toast notifications continue for Claude
Code. No TypeScript plugin or OpenClaw-side code required.

- `hosts/openclaw/` — `Bridge` (Gateway Protocol WS client with reconnect +
  subscription replay), `SessionMap` (per-peer session id, cached in
  SQLite), `HumanSurface`, `AgentSurface`, envelope renderer.
- `core/surface.ErrAsyncReply` — the one cross-cutting addition: hosts
  return this when an ask has been delivered out-of-band; the core
  stops waiting for a synchronous reply. `hosts/claudecode` now uses
  it too, replacing its previous ad-hoc error.
- `cmd/clawdchan daemon -openclaw wss://… -openclaw-token …` turns the
  daemon into an always-on OpenClaw agent. The daemon also reads the
  same config from `~/.clawdchan/config.json` when the flag is omitted.
- Interactive setup (`make install`) prompts for OpenClaw once, writes
  the config, and restarts the service on demand. Scripted installers
  use `make install-openclaw OPENCLAW_URL=… OPENCLAW_TOKEN=…`. Passing
  `-openclaw-url=none` disables it again.
- Full spec: `docs/superpowers/specs/2026-04-19-openclaw-host-design.md`.
- User guide: `docs/openclaw.md`.

Coexistence with Claude Code: CC config is never removed or replaced.
The daemon owns the node; the CC MCP server continues to run per-session
in outbox-writer mode and keeps serving `clawdchan_inbox` and
`clawdchan_message` (including `as_human=true` replies), so a user can run
both surfaces at once.

## Phase 3 — Follow-ups (deferred)

- Noise_IK ephemeral session layer for forward secrecy, layered on top of
  the existing AEAD without breaking pairing.
- libp2p or QUIC transport with relay fallback.
- Group threads (N > 2).
- Multiple topics / threads per peer in the UI.
- Signed `policy_denied` envelopes and structured policy config.
- Post-quantum hybrid handshake (version bump).
- Hosted public relay.
- Delivery status field (`queued | relay_acked | peer_online | delivered |
  read`) on envelopes, populated by relay acks and read receipts.
- Peer presence (`online`, `last_seen_ms`) via relay heartbeat, exposed in
  the `peers` array returned by `clawdchan_toolkit`.
- `Source` field on the envelope `Principal` distinguishing `mcp`,
  `cli_send`, `submit_human_reply`, and future SDK origins. Requires an
  envelope version bump.
