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

Tool surface (peer-centric, v0.2): `clawdchan_toolkit`, `_whoami`, `_peers`,
`_pair`, `_consume`, `_message`, `_inbox`, `_reply`, `_decline`. Thread IDs
are not exposed — the host resolves peer→thread internally.

CC host is reactive: remote `AskHuman` is stored and surfaced on the user's
next CC turn via `clawdchan_inbox`'s `pending_asks` field. The MCP server
surfaces the content there specifically so Claude can present it to the
user; it is omitted from the main envelopes list until answered. Only
`_reply` (with the user's words, sent as role=human) or `_decline` closes
the ask. For ambient OS-level notifications, Phase 1.5 is now shipped —
`clawdchan daemon`.

Install-time ergonomics: `clawdchan init -write-mcp <dir>` drops a
`.mcp.json` pre-wired to the absolute `clawdchan-mcp` path; `clawdchan
doctor` validates binary, config, identity, and relay in one shot.

## Phase 1.5 — Always-on daemon (shipped as v0.2)

Status: **partial.** Manual-launch daemon shipped; launchd/systemd install
still pending.

- `clawdchan daemon` subcommand holds the relay link, drains the outbox,
  classifies inbound (new session vs. continuation), and fires OS
  notifications (osascript on macOS, notify-send on Linux).
- The MCP server checks the listener registry at startup and skips its own
  relay connect when a daemon is present — writes outbound to the shared
  SQLite outbox for the daemon to drain (up to 10s tick). Falls back to
  owning the relay link when no daemon is present.
- Still TODO: `clawdchan daemon install` to drop a launchd plist / systemd
  user unit so the daemon is truly always-on without manual terminal use.
- Still TODO: UserPromptSubmit hook that reads the store and injects an
  inbox digest into Claude's context on each turn, so the agent sees new
  traffic without needing to call `clawdchan_inbox` explicitly.

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
- Delivery status field (`queued | relay_acked | peer_online | delivered |
  read`) on envelopes, populated by relay acks and read receipts.
- Peer presence (`online`, `last_seen_ms`) via relay heartbeat, exposed on
  `clawdchan_peers`.
- `Source` field on the envelope `Principal` distinguishing `mcp`,
  `cli_send`, `submit_human_reply`, and future SDK origins. Requires an
  envelope version bump.
