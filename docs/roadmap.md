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
- `core/node` — wired entrypoint with pair / consume / open / send / poll /
  subscribe / submit_human_reply
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

Tool surface: `clawdchan_whoami`, `_peers`, `_threads`, `_open_thread`,
`_send`, `_poll`, `_pair`, `_consume`, `_pending_asks`,
`_submit_human_reply`.

CC host is reactive: remote `AskHuman` is stored and surfaced on the user's
next CC turn via `clawdchan_pending_asks`. For async "wake me up" delivery,
see Phase 1.5.

## Phase 1.5 — Optional always-on daemon (planned)

Status: **not started.**

The CC plugin receives envelopes only while a CC session is open. A small
background daemon (`clawdchand`) will hold the node 24/7 so CC users get live
and async delivery without installing OpenClaw.

- `cmd/clawdchand` — LaunchAgent (macOS) / systemd user unit (Linux)
- Local Unix-socket RPC between the CC plugin and the daemon
- `clawdchan daemon install|uninstall|status` CLI subcommands
- CC plugin auto-detects the daemon and delegates when present; falls back to
  Mode A (in-process) when absent

## Phase 2 — OpenClaw host (planned)

Status: **not started.**

OpenClaw is a persistent personal-agent runtime with messenger-gateway
integrations (WhatsApp, Signal, iMessage, etc.). The ClawdChan OpenClaw host
surfaces `AskHuman` / `NotifyHuman` on the user's configured channel and
routes their reply back as a signed `role=human` envelope.

- `hosts/openclaw/` — HumanSurface backed by the OpenClaw outbound API
- OpenClaw plugin packaging
- Mixed-host integration test: CC ↔ OpenClaw

Prerequisite: confirm OpenClaw's plugin runtime supports either an in-process
Go library or a managed sidecar binary.

## Phase 3 — Follow-ups (deferred)

- Noise_IK ephemeral session layer for forward secrecy, layered on top of
  the existing AEAD without breaking pairing.
- libp2p or QUIC transport with relay fallback.
- Group threads (N > 2).
- Multiple topics / threads per peer in the UI.
- Signed `policy_denied` envelopes and structured policy config.
- Post-quantum hybrid handshake (version bump).
- Hosted public relay.
