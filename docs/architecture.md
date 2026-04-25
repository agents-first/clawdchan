# Architecture

ClawdChan is split into a host-agnostic **core** and **host bindings** that
adapt the core to specific runtimes (Claude Code via MCP and OpenClaw via
gateway mode today). A reference **relay** routes ciphertext frames between peers. The
relay never sees cleartext.

## Data flow

```
+--------------------+                           +--------------------+
|  Host A (Claude)   |                           |  Host B (Claude)   |
|  +--------------+  |                           |  +--------------+  |
|  | AgentSurface |  |                           |  | AgentSurface |  |
|  | HumanSurface |  |                           |  | HumanSurface |  |
|  +------+-------+  |                           |  +------+-------+  |
|         |          |                           |         |          |
|  +------v-------+  |                           |  +------v-------+  |
|  |    Node      |  |                           |  |    Node      |  |
|  | identity     |  |                           |  | identity     |  |
|  | store        |  |                           |  | store        |  |
|  | policy       |  |                           |  | policy       |  |
|  | session      |  |                           |  | session      |  |
|  +------+-------+  |                           |  +------+-------+  |
+---------|----------+                           +---------|----------+
          | transport (wss)                       transport|(wss)
          v                                                v
       +---------------------- Relay ------------------------+
       |  /link   authenticated ciphertext forwarder         |
       |  /pair   unauthenticated pairing rendezvous         |
       |  /healthz                                           |
       +-----------------------------------------------------+
```

## Repo layout

```
core/                host-agnostic protocol
  identity/          Ed25519 + X25519 node identity
  envelope/          wire format, canonical CBOR, Ed25519 signatures
  session/           per-peer XChaCha20-Poly1305 keyed from X25519 DH
  pairing/           BIP39 mnemonic pairing + rendezvous
  transport/         WebSocket client to the relay
  relaywire/         JSON types shared between client and relay
  store/             SQLite persistence
  policy/            local policy engine (revoke, AskHuman gating)
  surface/           HumanSurface, AgentSurface contracts + Nop defaults
  node/              Node wiring — the thing hosts hold

hosts/
  claudecode/        Claude Code MCP server + plugin manifest
  openclaw/          OpenClaw gateway binding

internal/
  relayserver/       reference relay implementation

cmd/
  clawdchan/         CLI
  clawdchan-mcp/     MCP server for the Claude Code host
  clawdchan-relay/   relay binary

docs/                design, roadmap, deployment, integration guides
```

## Key design choices

- **Core imports nothing host-specific.** `core/` has no references to
  Claude Code, MCP, or OpenClaw. Host bindings live under `hosts/` and
  depend on the core, never the other way around. Adding a new host is
  adding a new binding, not changing the core.
- **The node is the trust boundary.** A node owns its Ed25519 + X25519
  keypairs and signs every envelope. Principals on the node (agent,
  human) share the node's signing key; the `role` field in each envelope
  identifies which spoke.
- **Policy enforcement is local.** A remote peer can *request*
  `AskHuman`; the local node's policy engine decides whether to honor,
  downgrade, or deny it. No remote can dictate how the local human is
  interrupted.
- **Pairings are local, not relay state.** Changing the relay URL does
  not invalidate paired peers — each side keeps the other's NodeID,
  X25519 key, and SAS in its own SQLite store.

See [design.md](design.md) for the wire format and protocol spec.
