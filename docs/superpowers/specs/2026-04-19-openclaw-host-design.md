# OpenClaw host — design spec

**Status:** approved, ready for implementation plan
**Date:** 2026-04-19
**Phase:** roadmap phase 2 (OpenClaw host), vision A (always-on agent host)

## Goal

Make two OpenClaw-hosted agents talk to each other over ClawdChan, the same
way two Claude Code agents already do today. An OpenClaw user pairs with a
peer via the existing 12-word mnemonic flow; from that point their agent sees
peer messages natively inside an OpenClaw session and replies back through
the same session.

This spec covers the OpenClaw↔OpenClaw case *and* the mixed CC↔OpenClaw case
— both work because the wire protocol is host-agnostic (`docs/design.md` §
"Host bindings"). Nothing in `core/` changes.

## Non-goals for v1

- No OpenClaw dashboard integration / TypeScript channel plugin.
- No agent-facing tool surface on OpenClaw (pairing stays CLI-only).
- No remote OpenClaw support — v1 requires OpenClaw and `clawdchan daemon`
  on the **same machine**, connected over `ws://localhost:…`.
- No composition of *two human surfaces* per machine — the daemon owns
  whichever one is active. CC's MCP server still runs alongside as an
  outbox writer; pairing and `clawdchan_inbox` continue to work.
- No per-peer `agents.create` — one OpenClaw *session* per peer only.
- No multi-ask correlation: if more than one `AskHuman` is pending on a
  thread simultaneously, the next assistant turn resolves whichever ask
  was oldest. Document the limit; accept it.

## Context

OpenClaw (per `https://docs.openclaw.ai/`) is a Node.js/TypeScript gateway
with:

- A **Gateway Protocol** over WebSocket with JSON text frames. Supports
  operator-client connections with `sessions.send`,
  `sessions.messages.subscribe`, `sessions.list`, plus agent/session
  management methods.
- A TypeScript-only in-process **plugin SDK**. Channel plugins are TS
  modules; non-JS processes cannot be plugins.
- A bundled **Pi agent** running as a long-lived RPC service, always
  reachable in its session.
- Per-user **messaging channels** (Telegram/WhatsApp/Signal/iMessage/...)
  that the agent can drive outbound.

This lets us take the simplest possible integration shape: the ClawdChan
daemon connects to the OpenClaw gateway as an **operator client** over
WebSocket and bridges each paired peer to an OpenClaw session. No TS plugin,
no OpenClaw-side code, no new binary. Everything ships from this repo.

## Architecture

```
+------------------+     paired    +------------------+
|  Node A          |  <----------> |  Node B          |
|  (OpenClaw host) |   ciphertext  |  (OpenClaw host  |
|                  |   over relay  |   or CC host)    |
+--------+---------+               +------------------+
         |
         | ws://localhost:…/gateway  (Gateway Protocol,
         |                            operator client)
         v
+---------------------------+
|  OpenClaw gateway         |
|  (on the same machine)    |
|                           |
|  sessions:                |
|   clawdchan:<peer-short>  |  ← one session per peer
|   ...                     |
|                           |
|  Pi agent reads/writes    |
|  each session; may        |
|  escalate to Telegram/    |
|  WhatsApp/... via its     |
|  own channels.            |
+---------------------------+
```

### Repo layout additions

```
hosts/openclaw/
  doc.go            package doc (replaces the current 6-line stub)
  bridge.go         Gateway Protocol WS client
  session.go        peer ↔ OpenClaw session-id mapping + persistence
  surface.go        HumanSurface + AgentSurface implementations
  render.go         envelope → agent-readable text rendering
  bridge_test.go
  session_test.go
  surface_test.go
  integration_test.go
```

Daemon additions in `cmd/clawdchan/daemon.go`: three flags — `-openclaw`,
`-openclaw-token`, `-openclaw-device-id` — and the wiring code that swaps
the node's surfaces from Nop to OpenClaw when `-openclaw` is set.

One additive change in `core/surface/`: export an `ErrAsyncReply` sentinel
so both hosts can signal "ask delivered, reply will arrive async" without
relying on string matching. No wire-format change. See § "Core changes".

## Components

### `hosts/openclaw/bridge.go` — Gateway Protocol WS client

Thin client that speaks the Gateway Protocol:

```go
type Bridge struct {
    conn    *websocket.Conn
    token   string
    mu      sync.Mutex
    pending map[string]chan gatewayRes  // req.id → response chan
}

func NewBridge(wsURL, token, deviceID string) (*Bridge, error)
func (b *Bridge) Connect(ctx context.Context) error        // handshake + hello-ok
func (b *Bridge) SessionCreate(ctx, name) (sid string, err error)
func (b *Bridge) SessionsSend(ctx, sid, text string) error
func (b *Bridge) Subscribe(ctx, sid string) (<-chan Msg, error)
func (b *Bridge) Close() error
```

Handshake: WS dial → server nonce → client sends `connect` with device
identity and bearer token → server `hello-ok` with protocol version and
feature flags. Reconnect loop is exponential backoff capped at 30s; while
disconnected, outbound ClawdChan envelopes stay in the existing outbox (no
new buffering code needed).

Auth in v1: shared-secret bearer only (`Authorization: Bearer <token>`),
loopback-bound. Device-pairing flows for remote gateways deferred to v1.1.

### `hosts/openclaw/session.go` — peer↔session mapping

```go
type SessionMap struct {
    store *store.Store           // for persistence (new column or kv table)
    br    *Bridge
    mu    sync.RWMutex
    cache map[identity.NodeID]string
}

func (m *SessionMap) EnsureSessionFor(ctx, nodeID) (string, error)
// cache → store → br.SessionCreate → persist → return
```

Session naming: `clawdchan:<first-8-hex-of-nodeID>`. Collisions within 8
hex is astronomically unlikely for the peer counts we target; we can widen
later without a migration because the mapping table stores the full ID.

Persistence: add a `openclaw_session_id` column to the `peers` table, or a
sibling `openclaw_sessions(node_id, session_id)` kv table — decide in the
implementation plan based on migration cost. Must survive daemon restarts.

On daemon start: for each peer in the store, call `EnsureSessionFor`
(cheap — cache miss hits the store, not the gateway) and launch a
subscriber goroutine.

### `hosts/openclaw/surface.go` — HumanSurface + AgentSurface

Both surfaces are real implementations — unlike the CC host's `Ask` which
returns an error on purpose (`hosts/claudecode/host.go:29`), the OpenClaw
host actually delivers the ask into the session, because the agent on the
other end is always reachable.

```go
type AgentSurface struct {
    br *Bridge
    sm *SessionMap
}

func (a *AgentSurface) OnMessage(ctx, env envelope.Envelope) error {
    sid, err := a.sm.EnsureSessionFor(ctx, env.From.NodeID)
    if err != nil { return err }
    return a.br.SessionsSend(ctx, sid, render.ForAgent(env, peer))
}
```

```go
type HumanSurface struct {
    br *Bridge
    sm *SessionMap
}

func (h *HumanSurface) Notify(ctx, thread, env) error {
    sid, _ := h.sm.EnsureSessionForThread(ctx, thread)
    return h.br.SessionsSend(ctx, sid, render.Notify(env))
}

func (h *HumanSurface) Ask(ctx, thread, env) (envelope.Content, error) {
    sid, err := h.sm.EnsureSessionForThread(ctx, thread)
    if err != nil { return envelope.Content{}, err }
    if err := h.br.SessionsSend(ctx, sid, render.Ask(env)); err != nil {
        return envelope.Content{}, err
    }
    return envelope.Content{}, surface.ErrAsyncReply
}

func (HumanSurface) Reachability() surface.Reachability { return surface.ReachableAsync }
func (HumanSurface) PresentThread(ctx, thread) error    { return nil }
```

`AskHuman` works end-to-end on OpenClaw in a way it can't on CC: the agent
receives the ask on its session, escalates to the human through its own
channels (Telegram/WhatsApp/etc. — OpenClaw's problem, not ours), and when
the reply comes back on the session our subscriber captures it and calls
`Node.SubmitHumanReply`.

### `hosts/openclaw/render.go` — envelope → agent text

One function per rendering shape, all returning plain strings the session
bridge pushes as user-role messages:

```go
func ForAgent(env envelope.Envelope, peer *store.Peer) string
func Notify(env envelope.Envelope) string
func Ask(env envelope.Envelope) string
```

Output shape:

```
[clawdchan · from <peer-alias> · <intent>]
<body>
```

Example:

```
[clawdchan · from alice · ask_human]
Can you approve the migration plan in thread #4? Alice's agent is
waiting on you specifically.
```

The `[clawdchan · …]` prefix is a strong, parseable marker the agent can
learn: *anything with this prefix is peer-originated; my reply goes back
to that peer via this session*. Intent vocabulary matches the envelope
layer (`core/envelope/intent.go`): `say`, `ask`, `ask_human`,
`notify_human`, `handoff`, `ack`, `close`.

### Inbound turn capture (agent → peer)

The subscriber goroutine in `bridge.go`:

```go
func (b *Bridge) runSubscriber(ctx, sid string, n *node.Node, thread envelope.ThreadID) {
    msgs, _ := b.Subscribe(ctx, sid)
    for {
        select {
        case msg := <-msgs:
            if msg.Role != "assistant" { continue }  // skip human/system
            content := envelope.Content{Text: msg.Text}
            if n.HasPendingAsk(thread) {
                _ = n.SubmitHumanReply(ctx, thread, content)
            } else {
                _ = n.Send(ctx, thread, envelope.IntentSay, content)
            }
        case <-ctx.Done():
            return
        }
    }
}
```

Only `role=assistant` turns relay outward. Human turns on the session
either close a pending ask (→ `SubmitHumanReply`) or are dropped (the human
is talking to their own agent, not the peer).

Pending-ask detection: the node tracks pending asks per thread; add a
`HasPendingAsk(thread)` accessor on `core/node.Node` if one doesn't exist.
The heuristic ("next assistant/human turn after a pending ask resolves it")
is the same one CC uses. Multi-ask-per-thread is out of scope.

### Daemon wiring — `cmd/clawdchan/daemon.go`

New flags:

```
-openclaw <ws-url>        ws://localhost:18789/gateway
-openclaw-token <string>  shared-secret bearer for the local gateway
-openclaw-device-id <id>  defaults to "clawdchan-daemon"
```

When `-openclaw` is empty the daemon runs exactly as today (notification
sidecar mode). When set, after the relay link is up:

1. Construct `Bridge`, `Connect`.
2. Construct `SessionMap`; for every peer in the store, `EnsureSessionFor`.
3. Swap the node's surfaces from Nop to OpenClaw versions.
4. Launch one subscriber goroutine per peer session.
5. On shutdown: close subscribers, close bridge, close node as today.

Two operating modes, mutually documented:

- **Notification sidecar** (no `-openclaw`): holds relay link, drains
  outbox, fires OS toasts. Today's behavior.
- **OpenClaw host** (`-openclaw` set): holds relay link, drains outbox,
  bridges peers to sessions. Human/agent surfaces delegate to OpenClaw.

### Coexistence with CC on the same machine (v1 decision)

CC and OpenClaw coexist — ClawdChan does **not** replace any Claude Code
configuration. The daemon owns the node (relay link, store, human/agent
surfaces). The CC MCP server, when spawned per session, detects the
daemon via the listener registry and runs in outbox-writer mode
(`CLAUDE.md:67-70`): it writes outbound envelopes into the shared SQLite
outbox for the daemon to drain, and it still serves the peer-centric
tool surface (`clawdchan_inbox`, `clawdchan_reply`, …) so Claude can
read and respond to pending asks. When `-openclaw` is set on the daemon,
its OpenClaw surfaces handle the human-facing side too — OS
notifications and OpenClaw session delivery happen together, not as an
either/or. Users who want to turn OpenClaw off pass
`-openclaw-url=none` to `clawdchan setup` (or just skip the prompt).

## Core changes

One additive change in `core/surface/surface.go`:

```go
// ErrAsyncReply is returned by HumanSurface.Ask when the ask has been
// delivered to an async surface (OpenClaw session, CC inbox, messenger
// gateway, ...) and the reply will arrive later via SubmitHumanReply.
// The core treats this as success — no auto-reply is generated.
var ErrAsyncReply = errors.New("surface: ask delivered; reply is async")
```

Update the CC host to return `ErrAsyncReply` instead of its ad-hoc
`errors.New(...)` (`hosts/claudecode/host.go:33`). Update
`core/node.Node.handleAsk` to treat `ErrAsyncReply` as a non-error signal
(no envelope, no log warn). No wire format or envelope schema change; no
migration.

This is the *only* change under `core/`. Everything else lives in
`hosts/openclaw/` and `cmd/clawdchan/`.

## Testing strategy

All tests run without a live OpenClaw gateway:

- `bridge_test.go` — fake gateway WS server using `httptest` +
  `gorilla/websocket`. Verify: handshake with bearer auth succeeds and
  rejects bad tokens; `sessions.send` req/res correlation by `id`;
  subscriber receives session messages; reconnect with backoff on drop.
- `session_test.go` — `SessionMap` cache hit, store hit, creation path;
  survives a simulated daemon restart (new `SessionMap` over the same
  store sees the persisted mapping).
- `surface_test.go` — each of `Notify` / `Ask` / `OnMessage` calls the
  right bridge method with the rendered payload; `Ask` returns
  `ErrAsyncReply`; `Reachability` is `ReachableAsync`.
- `integration_test.go` — two in-process nodes, each with its own
  OpenClaw host pointed at its own fake gateway, paired to each other.
  Round-trip: node A's fake gateway emits an assistant turn on A's peer-B
  session → envelope flows through the relay → arrives on B's peer-A
  session via `sessions.send` on B's fake gateway. Mirrors the existing
  CC↔CC integration test in spirit.

## Error paths

- Gateway WS drop: reconnect with exponential backoff (1s, 2s, 4s, …,
  30s cap). Outbound envelopes continue to arrive; they wait in the
  outbox until the bridge reconnects and the subscribers are re-attached.
- `sessions.send` returns an error: log, retry once after 1s, then drop
  and record an envelope-level `policy_denied` locally. Peer doesn't
  retry — the envelope was already acknowledged at the relay layer.
- Agent turn captured on a session whose peer was since revoked:
  subscriber checks peer state before calling `Node.Send`; if revoked,
  the turn is silently dropped (session is now orphan; cleanup is a
  future concern, not v1).
- Multiple concurrent `AskHuman`s on the same thread: only the oldest
  pending ask is resolved by the next human turn. Document this
  limitation in `docs/roadmap.md` phase 2 follow-ups.

## Rollout / install

No new binary. Users install ClawdChan as they do today, then:

```sh
clawdchan daemon install              # as today; registers the service
# (edit the service unit to add -openclaw and -openclaw-token flags,
#  or drop a config file the daemon reads — TBD in impl plan)
clawdchan doctor                      # verifies gateway reachability too
```

A `clawdchan doctor` check for `-openclaw` being set should:
- Try to connect to the gateway URL with the token.
- Verify `hello-ok` comes back.
- Exit cleanly or print a specific remediation (gateway offline, wrong
  token, gateway on a different port).

## Open items deferred to implementation plan

- Exact SQLite schema for session persistence (new column vs. sibling
  table) — pick based on what least disturbs existing migrations.
- Exact config surface for the daemon's `-openclaw` flags (CLI flags on
  the service invocation vs. a config file at `~/.clawdchan/openclaw.json`
  read at startup).
- Where to put `HasPendingAsk(thread)` on the node — new method vs.
  reusing an existing store query.
- Whether to add an `openclaw_` prefix to any log lines so operators can
  filter daemon logs by host binding.

## Updates to existing docs

- `docs/roadmap.md` § Phase 2 — update from "not started" to in-flight
  when implementation begins; note the vision-A scope and the deferred
  items.
- `docs/design.md` § "OpenClaw" host binding — replace the current
  aspirational paragraph with the implemented shape (operator client
  over Gateway Protocol, one session per peer, no TS plugin).
- `docs/architecture.md` § Repo layout — note `hosts/openclaw/` is
  populated.
- `hosts/openclaw/doc.go` — replace stub with the implemented behavior.
