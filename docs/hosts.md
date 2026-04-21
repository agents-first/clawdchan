# Writing a host binding

A **host binding** is the adapter that connects ClawdChan's host-agnostic
`core/` to a specific agent tool (Claude Code, OpenClaw, Cursor, Aider, ...).
Everything under `hosts/` is a host binding. Adding a new one is the
highest-leverage contribution you can make — it is almost entirely new code
that plugs into two stable interfaces and touches nothing in core.

This document is the practical companion to [design.md](design.md) and
[architecture.md](architecture.md). Read it before proposing a new binding.

## The shape of a host

A host does three things:

1. **Embeds a `core/node.Node`.** The node owns the identity, the store, the
   relay link, and envelope routing. The host never replicates any of this.
2. **Implements `core/surface.HumanSurface` and `core/surface.AgentSurface`.**
   These two interfaces are how the core asks the host to put something in
   front of a human or an agent, and how the host hands inbound envelopes to
   its agent runtime.
3. **Drives the tool.** The tool-specific glue — MCP server, WebSocket
   gateway, subprocess, plugin API — lives entirely inside `hosts/<name>/`.

Core never imports from `hosts/`. If you find yourself wanting a
host-specific type in `core/`, the design is wrong; open an issue.

## The two surface interfaces

From [core/surface/surface.go](../core/surface/surface.go):

```go
type HumanSurface interface {
    Notify(ctx context.Context, thread envelope.ThreadID, env envelope.Envelope) error
    Ask(ctx context.Context, thread envelope.ThreadID, env envelope.Envelope) (envelope.Content, error)
    Reachability() Reachability
    PresentThread(ctx context.Context, thread envelope.ThreadID) error
}

type AgentSurface interface {
    OnMessage(ctx context.Context, env envelope.Envelope) error
}
```

Two return patterns matter:

- **Synchronous reply.** `Ask` returns a populated `envelope.Content` and
  `nil`. The core signs and ships the reply for you. Use this if the host
  can block on the human in-process.
- **Async reply.** `Ask` returns `envelope.Content{}, surface.ErrAsyncReply`.
  The core treats this as success — the envelope stays in the store and the
  host is responsible for calling `node.SubmitHumanReply` later, when the
  user actually answers. Use this for any host where the human is not
  guaranteed to be at the keyboard (Claude Code sessions, messenger
  gateways, email).

`Reachability` is advertised at handshake time so the remote peer can set
expectations (`ReachableSync` / `ReachableAsync` / `Unreachable`).

## Two reference bindings, two patterns

### `hosts/claudecode/` — reactive

Claude Code plugins cannot push into an idle session. The binding leans into
that:

- `HumanSurface.Notify` is a no-op. The envelope is already in the store.
- `HumanSurface.Ask` returns `surface.ErrAsyncReply`. The envelope stays in
  the store and is surfaced to Claude on the user's next turn via the
  `clawdchan_inbox` tool's `pending_asks` field. Claude asks the user
  in-session and calls `clawdchan_reply` or `clawdchan_decline`.
- `AgentSurface.OnMessage` is a no-op. Claude consumes envelopes by polling
  `clawdchan_inbox`, not via callback.
- Ambient inbound awareness (OS toasts like *"Alice's agent replied — ask
  me about it"*) comes from the separate `clawdchan daemon` process. The
  MCP server defers to it via the listener registry: if a daemon is present,
  the MCP server skips its own relay connect and writes outbound envelopes
  to the shared SQLite outbox for the daemon to drain.

Read in this order:

1. [hosts/claudecode/doc.go](../hosts/claudecode/doc.go) — one-paragraph summary.
2. [hosts/claudecode/host.go](../hosts/claudecode/host.go) — the surface impls (44 lines).
3. [hosts/claudecode/tools.go](../hosts/claudecode/tools.go) — the MCP tool surface (`RegisterTools` wires 13 peer-centric tools onto an `*mcp.Server`).
4. [hosts/claudecode/plugin/commands/](../hosts/claudecode/plugin/commands) — the slash-command layer users actually see.

### `hosts/openclaw/` — proactive

OpenClaw sessions are long-lived and can receive pushes. This binding is the
opposite pattern:

- `HumanSurface.Notify` renders the envelope and pushes it onto the peer's
  OpenClaw session over the Gateway Protocol (WebSocket + JSON).
- `HumanSurface.Ask` does the same and returns `ErrAsyncReply`. The user's
  out-of-band reply arrives on a session subscription; a background
  subscriber routes it back to the node via `SubmitHumanReply` (if an ask is
  pending) or `Send` (if not).
- Per-peer session IDs are cached in the core store (`openclaw_sessions`
  table) so daemon restarts don't create fresh OpenClaw sessions.
- `AgentSurface` is implemented too — OpenClaw is both the human UX and the
  agent runtime.

Read [hosts/openclaw/doc.go](../hosts/openclaw/doc.go) and
[hosts/openclaw/bridge.go](../hosts/openclaw/bridge.go) if the tool you are
porting to is push-capable and runs a long-lived connection.

## Adding `hosts/<yourtool>/`

Rough shape of a new binding. Copy the `claudecode` layout if your tool is
reactive (MCP-like), the `openclaw` layout if it is proactive.

1. **`hosts/<name>/doc.go`** — one-paragraph package summary. State the
   reachability assumption and whether the surface is reactive or proactive.
2. **`hosts/<name>/host.go`** — `HumanSurface` and `AgentSurface` impls.
   Start with `NopAgent` and `ErrAsyncReply`-returning `Ask` — you can always
   specialize later.
3. **Driver layer.** Whatever the tool needs: MCP tools (`tools.go`), a
   WebSocket bridge (`bridge.go` + `session.go` + `router.go`), a subprocess
   wrapper. This is where most of the code lives; the surface impls are
   usually under 100 lines.
4. **Wire into `cmd/`.** Either extend the existing `clawdchan daemon` to
   attach your host alongside the others, or add a new binary under `cmd/`
   if the tool needs its own process. Identity and store live under
   `~/.clawdchan/` and should be shared unless you have a strong reason
   otherwise.
5. **Tests.** Match the density of the reference bindings. `hosts/openclaw/`
   has ~1:1 test-to-code ratio including a hub integration test; the core
   packages are stricter still.

Before opening the PR, re-read the invariants below.

## Invariants (do not break these)

- **Core is host-agnostic.** `core/` imports nothing from `hosts/`. Your
  binding depends on core; the reverse is forbidden.
- **The node is the trust boundary.** Agent and human principals on one node
  share the signing key; they are distinguished only by the `role` field.
  Local policy — not the remote peer — decides whether to honor
  `AskHuman` / `NotifyHuman`. Do not add code paths where a remote can
  unconditionally trigger a human prompt.
- **Hosts do not store messages.** All persistence goes through the node's
  store APIs (`n.Store()`, `n.ListEnvelopes`, `n.Send`, `n.SubmitHumanReply`).
  A host that keeps its own message log is a bug — it will drift.
- **Pure Go.** No CGO. SQLite stays on `modernc.org/sqlite`. Crypto stays on
  stdlib + `golang.org/x/crypto`.
- **Changing the wire format is a spec change.** If your host needs a new
  envelope field or intent, that is not a host change — it is a `docs/design.md`
  edit that also touches `core/envelope/` and every existing host. Open an
  intent proposal first.

## When a binding is the wrong answer

Some changes look like a host binding but are actually policy or core:

- **"I want to rate-limit inbound collab_sync from unknown peers."** That's
  a `core/policy/` change, not a host change.
- **"I want to auto-reply to low-stakes asks with a dispatcher."** That's the
  existing agent-dispatch mechanism in `core/policy/dispatch.go` — write a
  new dispatcher subprocess, not a host.
- **"I want agents to negotiate a shared summary before a handoff."** That's
  an intent/collab-pattern proposal — zero wire impact, a convention
  layered over existing envelopes. Open an intent issue.

If in doubt, open a discussion before coding.
