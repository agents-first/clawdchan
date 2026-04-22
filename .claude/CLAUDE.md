# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

All development flows through the `Makefile` (Go 1.25, pure-Go deps — no CGO).

- `make build` — builds all three binaries (`clawdchan`, `clawdchan-relay`, `clawdchan-mcp`) into `./bin`.
- `make install` — `go install` the three `cmd/` binaries to `GOPATH/bin`. `clawdchan-mcp` must be on `PATH` for Claude Code to launch it.
- `make test` — runs the full suite with a 120s timeout. CI additionally uses `-count=1` to defeat the test cache.
- `make run-relay` — runs a local relay on `:8787` for integration testing (two local `clawdchan` nodes can share it).
- `make tidy` — `go mod tidy`.

Run a single package's tests: `go test ./core/envelope/...`. Single test: `go test ./core/envelope -run TestName`. The CLI has a compile-and-drive integration test at `cmd/clawdchan/cli_integration_test.go`.

CI (`.github/workflows/ci.yml`) runs `go vet ./...`, a **`gofmt -l .` must-be-empty** check, `go build ./...`, and the test suite. Run `gofmt -w .` before pushing — a single unformatted file fails CI.

Relay deployment: `Dockerfile` builds only the relay into a distroless image; `docker-compose.yml` runs it locally on `:8787`; `fly.toml` deploys it to Fly.io (terminates TLS at the edge, relay speaks plain WS on `:8787`).

## Architecture

ClawdChan is an end-to-end-encrypted protocol that lets two (human, agent) pairs share a conversation. Two design invariants shape the whole tree — keep them in mind before making structural changes.

### 1. Core is host-agnostic

```
core/                           # imports NOTHING from hosts/
  identity/   Ed25519 + X25519 node keypair
  envelope/   wire format — deterministic CBOR + Ed25519 signatures
  session/    per-peer XChaCha20-Poly1305 keyed from X25519 DH (no handshake, no FS)
  pairing/    128-bit code → 12-word BIP39 mnemonic + AEAD card exchange over /pair
  transport/  WebSocket client to relay
  relaywire/  JSON types shared between client and relay
  store/      SQLite persistence (modernc.org/sqlite — pure Go)
  policy/     local gate for inbound intents (rate-limit, allowlist, quiet hours)
  surface/    HumanSurface, AgentSurface interfaces + Nop defaults
  node/       Node type — the thing hosts embed

hosts/
  claudecode/ Claude Code binding (MCP server)
  openclaw/   OpenClaw gateway binding — bridge, session map, human/agent surfaces

internal/relayserver/  reference relay (/link, /pair, /healthz)

cmd/
  clawdchan/       CLI: setup, init, whoami, pair, consume, peers, peer, threads,
                   open, send, listen, daemon, path-setup, inspect, doctor
  clawdchan-mcp/   MCP server launched per CC session over stdio
  clawdchan-relay/ relay binary
```

**`core/*` must not import anything from `hosts/` or any host-specific library** (no `mark3labs/mcp-go` in core, etc.). Host bindings depend on core, never the reverse. Adding a new host (e.g. OpenClaw) is a new `hosts/<name>/` subtree, not a modification to core.

### 2. The node is the trust boundary

A `core/node.Node` owns one Ed25519+X25519 identity. Every envelope it emits is signed with that key. Agent and human principals on the same node **share** the signing key and are distinguished only by the `role` field in the envelope (`agent` | `human`). When handling an envelope from a remote peer, local policy — not the peer — decides whether to honor `AskHuman` / `NotifyHuman`. Do not add code paths where a remote can unconditionally trigger a human prompt.

### Claude Code host is deliberately reactive

`hosts/claudecode/host.go` implements `HumanSurface` so that:

- `Notify` is a no-op — the envelope is already persisted by the node.
- `Ask` **returns an error on purpose**. This is not a bug. A CC plugin cannot push into an idle session, so the envelope stays in the store and is surfaced to Claude on the user's next turn via `clawdchan_inbox` (its `pending_asks` field); Claude then calls `clawdchan_reply` or `clawdchan_decline`. Do not "fix" this by blocking or auto-replying.
- `AgentSurface.OnMessage` is a no-op — Claude consumes envelopes by calling `clawdchan_inbox`, not via callback.

The MCP tool surface Claude sees is peer-centric: `clawdchan_message` / `clawdchan_inbox` / `clawdchan_reply` / `clawdchan_decline`, plus `clawdchan_subagent_await` (used by Task sub-agents running live collab loops), `clawdchan_peer_rename` / `_revoke` / `_remove`, and pair / consume / peers / whoami / toolkit. Thread IDs are never exposed to the agent — the host resolves peer→thread internally. Ambient inbound delivery (OS toasts like *"Alice's agent replied — ask me about it"*) comes from the separate `clawdchan daemon` process, which owns the relay link when running. The MCP server defers to it via the listener registry: if a daemon is present the MCP server skips its own relay connect and writes outbound to the shared SQLite outbox for the daemon to drain. Full tool surface is in `docs/mcp.md`. Identity/store live under `~/.clawdchan/`; CLI and MCP share state.

Inbound envelopes marked `Content.Title="clawdchan:collab_sync"` (sent via `collab=true` on `clawdchan_message`) are a sender hint that the exchange is a live iterative loop; the receiver uses it to differentiate notification copy and tune in-session suppression. Notification policy lives in `cmd/clawdchan/daemon_notify.go`; the marker constant is in `core/policy/collab.go`. An agent-cadence subprocess that would auto-answer these asks is a future extension, not part of v0 — do not reintroduce a dispatcher without an explicit decision.

### Wire format and crypto

The wire format, handshake, and session derivation are specified in `docs/design.md` — treat it as the source of truth. Changing an envelope field, intent, or key-derivation string requires updating both the code and the spec. Canonical signing form is deterministic CBOR (RFC 8949 §4.2.1) over every field except `signature`. Pairings are **local** (stored in each side's SQLite): changing the relay URL must not invalidate existing peers.

Crypto primitives are stdlib or `golang.org/x/crypto`: Ed25519 (`crypto/ed25519`), X25519 (`crypto/ecdh`), HKDF-SHA256, XChaCha20-Poly1305. No custom crypto — if something new is needed, pull it from `x/crypto`.

## Conventions

- Go 1.25, pure-Go dependency set — do not introduce CGO. `modernc.org/sqlite` (not `mattn/go-sqlite3`) is the deliberate choice.
- `gofmt -l .` must be empty. `go vet ./...` must pass.
- Hosts must not write their own message stores — read through the core via the node's store APIs.
