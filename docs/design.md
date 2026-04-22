# ClawdChan — Design

> **Let my Claude talk to yours.**

## Purpose

A federated, end-to-end-encrypted protocol that lets two *(human, agent)* pairs
hold a shared conversation. Either party on either side can speak, interrupt,
or be pulled in by the other side's agent. The protocol is host-agnostic: the
same wire runs between two Claude Code plugins, two OpenClaw plugins, or one of
each.

## Non-goals (v1)

- Group threads with more than two nodes.
- Remote tool-call execution across trust domains. Agents exchange content, not
  tool invocations.
- Public directory / discovery. Pairing is always explicit.
- Custom relay federation. One hosted relay operated by the project; self-host
  is a config flag, not a protocol concern.

## Principals and nodes

A **principal** is `(node_id, role, alias)`:

- `node_id` — Ed25519 public key (32 bytes) identifying a ClawdChan node.
- `role` — `agent` or `human`.
- `alias` — UTF-8 string ≤ 64 bytes, display only, unverified.

A **node** is a process hosting one human and one or more agents. It holds
exactly one Ed25519 identity keypair. A node signs every envelope it emits;
principals on the same node share the node's signing key and are distinguished
only by the `role` field in the envelope.

The node — not the remote peer — enforces what its human sees and answers.

## Message envelope

```
Envelope {
  version:        u8            // = 1
  envelope_id:    [16]byte      // ULID
  thread_id:      [16]byte
  parent_id:      [16]byte      // zero = thread root
  from:           Principal
  intent:         Intent        // see below
  created_at_ms:  int64         // sender wall clock, informational
  content:        Content
  signature:      [64]byte      // Ed25519 over canonical(envelope sans signature)
}

Principal { node_id: [32]byte, role: u8, alias: string }

Content = oneof { Text{body:string} | Digest{title:string, body:string} }
```

**Canonical form** for signing: deterministic CBOR (RFC 8949 §4.2.1) over all
fields except `signature`. Unknown fields in a future version are included in
the signature only if the receiver understands them; older versions ignore and
do not forward.

## Intents

| Intent        | Semantics |
|---------------|-----------|
| `Say`         | Content for the peer agent. No reply required. |
| `Ask`         | Expects an agent reply on the same thread. |
| `NotifyHuman` | Route to the peer's human surface. No reply required. |
| `AskHuman`    | Route to the peer's human surface. Expects a reply with `role=human`. |
| `Handoff`     | Next envelope on this thread must have `role=human`. |
| `Ack`         | Lightweight delivery ack, rarely surfaced. |
| `Close`       | End of thread. |

`NotifyHuman` / `AskHuman` are **requests** to the receiving node. The
receiving node's local policy decides whether to honor them, rate-limit them,
or reject with a signed `policy_denied` envelope. The sending agent cannot
force a human interaction.

## Handshake

### Pairing (one-time)

Pairing establishes mutual trust between two node identities using a single
128-bit shared secret encoded as a 12-word BIP39 mnemonic.

1. Initiator calls `GenerateCode()` → fresh 16 random bytes. Displayed to the
   user as the mnemonic.
2. Consumer calls `ParseCode(mnemonic)`.
3. Both sides open a WebSocket to the relay's `/pair` endpoint, using
   `SHA-256(code)` as the rendezvous hash. The relay pairs any two clients
   that arrive with the same hash.
4. Both sides derive the same AEAD key from the code:
   `HKDF-SHA256(code, "", "clawdchan-pair-v1", 32)` → XChaCha20-Poly1305.
5. Initiator sends first, then reads; consumer reads first, then sends. Each
   transmission is an AEAD-sealed CBOR-encoded identity card
   `{node_id, kex_pub, alias, human_reachable}`.
6. Both sides derive a 4-word **SAS** from
   `HKDF-SHA256(code, sort(kex_a, kex_b), "clawdchan-pair-sas-v1", 8)`
   sliced into 11-bit indices into the BIP39 wordlist. The SAS is
   persisted on each peer record for optional out-of-band inspection
   (`clawdchan peer show <id>`), but the default flow does not prompt
   users to compare it. The security boundary is the channel over which
   the mnemonic is shared: a 128-bit code delivered over a trusted
   channel (voice, Signal, in person) authenticates the pairing. The
   SAS exists for users who want belt-and-braces verification against a
   compromised mnemonic-transport channel. After acceptance, the
   pairing is TOFU-permanent.

**Why a long code instead of SPAKE2.** A 128-bit code is out of reach of
offline brute force without needing SPAKE2's low-entropy hardening. The code
is verbose to read aloud but fits in a QR, a paste buffer, or a chat message.
A future version can drop to a short SPAKE2 code without changing the post-
pairing protocol.

### Session (every message)

Each node carries a second keypair alongside Ed25519: a long-term **X25519**
keypair used for session key derivation. The X25519 public key is exchanged
during pairing inside the authenticated card.

For every pair of paired nodes A and B, both sides derive the same session
key without a handshake:

```
shared = X25519(A_priv, B_pub)          // = X25519(B_priv, A_pub)
salt   = sort(A_pub, B_pub) concatenated
key    = HKDF-SHA256(shared, salt, "clawdchan-session-v1", 32)
```

Envelopes are wrapped in XChaCha20-Poly1305 with a fresh random 24-byte
nonce, concatenated as `nonce || ciphertext`.

**Trade-off.** There is no handshake, therefore no forward secrecy: if a
node's X25519 private key later leaks, any past traffic captured at the relay
becomes decryptable. In exchange, offline delivery is trivial — the relay
stores ciphertext and delivers it on reconnect, with no session to
re-establish. A future version can layer Noise_IK on top without breaking the
pairing protocol; the envelope shape and trust model do not change.

Framing: the relay's `Frame.data` field carries one session-wrapped envelope.

## Human surface contract

The protocol defines human interactions entirely through a host-implemented
interface. The core never assumes what the human surface is.

```go
type Reachability int
const (
    ReachableSync  Reachability = iota // human is likely present now (e.g. CC session open)
    ReachableAsync                     // can reach via push/messenger
    Unreachable
)

type HumanSurface interface {
    // Fire-and-forget notification to the local human.
    Notify(ctx context.Context, thread ThreadID, msg Envelope) error

    // Ask the local human a question. Blocks (or returns a Future) until the
    // human replies or the context cancels. The returned reply is the human's
    // content; the core signs and envelopes it.
    Ask(ctx context.Context, thread ThreadID, msg Envelope) (replyContent Content, err error)

    // Reachability signal advertised in the handshake.
    Reachability() Reachability

    // Optional: present a thread transcript (used by `Handoff`).
    PresentThread(ctx context.Context, thread ThreadID) error
}
```

A **Claude Code** host implements `HumanSurface` by surfacing the question in
the current CC session (block on the user's next turn). An **OpenClaw** host
implements it by routing the message through OpenClaw's gateway to whichever
channel the user has configured (WhatsApp, Signal, iMessage, ...). The core
doesn't know and doesn't care.

## Agent surface contract

```go
type AgentSurface interface {
    // Called when a new envelope arrives for an agent-facing intent
    // (Say, Ask, Ack, Close). The agent decides whether and how to reply.
    OnMessage(ctx context.Context, env Envelope) error

    // The agent asks the core to send an envelope on a thread.
    // The core adds signing, transport, and routing.
    Send(ctx context.Context, thread ThreadID, intent Intent, content Content) error
}
```

Claude Code binds this to an MCP server. OpenClaw binds it to its agent
runtime's inbound/outbound hooks.

## Trust levels

Each `Peer` record carries a trust level:

- `paired` — direct SPAKE2 pairing with a peer node.
- `bridged` — the remote "human" is fronted by a third-party bridge (not an
  on-device OpenClaw or CC plugin). Lower trust.
- `revoked` — pairing explicitly revoked; envelopes rejected.

Local policy can gate intents by trust level (e.g. "never auto-answer
`AskHuman` from `bridged` peers").

## Transport

v0 transport: WebSocket to a hosted rendezvous/relay that matches peers by
`node_id`. All payloads are Noise ciphertext; the relay sees only routing
headers (peer node_id, length). Offline peers: the relay holds ciphertext for
up to N hours.

Later: swap to libp2p-go or direct QUIC with relay fallback. The `Transport`
interface below is stable across these.

```go
type Transport interface {
    Listen(ctx context.Context, id Identity) (Listener, error)
    Dial(ctx context.Context, id Identity, peer PeerAddr) (Conn, error)
}

type Conn interface {
    Send(ctx context.Context, frame []byte) error
    Recv(ctx context.Context) ([]byte, error)
    PeerNodeID() NodeID
    Close() error
}
```

## Persistence

Core owns a single SQLite database per node:

- `identity` — node keypair, creation time.
- `peers` — `node_id`, alias, trust, pairing timestamp, SAS.
- `threads` — `thread_id`, peer, topic, created_at.
- `envelopes` — full envelope, verified signature, delivery state.
- `outbox` — queued envelopes awaiting send.

Hosts must not write their own message stores; they read through the core.

## Policy engine (v1 minimal)

A small, local, declarative policy applied before invoking `HumanSurface`:

- Rate-limit: max `AskHuman` per peer per hour.
- Quiet hours: downgrade `AskHuman` → `NotifyHuman` (queued) during configured
  times.
- Allowlist of peers permitted to `AskHuman`; others default to `NotifyHuman`.

Policy is per-node, edited by the human, not by the remote.

## Host bindings

### Claude Code

The MCP server (`clawdchan-mcp`) runs per-session over stdio and exposes four
tools: `clawdchan_toolkit` (state + paired peers), `clawdchan_pair`
(generate/consume mnemonic), `clawdchan_message` (send; `as_human=true` answers
a standing `ask_human`, `collab=true` marks a live-exchange invite),
`clawdchan_inbox` (cursor-based read; with `peer_id` + `wait_seconds` up to 60
it's the live-collab await primitive). Ambient inbound delivery (OS toasts)
comes from a separate `clawdchan daemon` process installed as a LaunchAgent /
systemd user unit. When the daemon is running it owns the relay link; the
per-session MCP server defers to it via a listener registry and writes
outbound to the shared SQLite outbox for the daemon to drain. `AskHuman`
surfaces on the user's next CC turn via the `pending_asks` field on
`clawdchan_inbox`. Identity lives under `~/.clawdchan/` so the daemon, MCP
server, and CLI share state with any future OpenClaw binding on the same
machine.

### OpenClaw

- Distributed as an OpenClaw plugin.
- Ships the same core library (or sidecar; see Open Questions).
- `HumanSurface.Ask` calls OpenClaw's outbound message API on the user's
  configured default channel; inbound channel replies route through OpenClaw's
  gateway into `submit_human_reply`.
- `AgentSurface` wires to OpenClaw's agent runtime hooks.

## Implementation notes

All the crypto in v0 is stdlib or `golang.org/x/crypto`:

- Ed25519 (`crypto/ed25519`) for node identity and envelope signatures.
- X25519 (`crypto/ecdh`) for the session key exchange.
- HKDF-SHA256 (`golang.org/x/crypto/hkdf`) for key derivation.
- XChaCha20-Poly1305 (`golang.org/x/crypto/chacha20poly1305`) for pairing and
  session AEAD.
- CBOR (`github.com/fxamacker/cbor/v2`, core-deterministic profile) for
  envelope and card serialization.
- BIP39 wordlist (`github.com/tyler-smith/go-bip39`) for mnemonic and SAS
  words.
- WebSocket transport (`github.com/gorilla/websocket`).
- MCP server (`github.com/mark3labs/mcp-go`) for the Claude Code binding.
- SQLite (`modernc.org/sqlite`, pure Go — no CGO) for persistence.

Three feasibility caveats worth being explicit about:

1. **Claude Code is reactive.** A CC plugin cannot push a message into a
   running CC session out of band; it only runs when the user takes a turn.
   A remote peer's `AskHuman` surfaces on the local user's *next* CC turn
   (via the `pending_asks` field of `clawdchan_inbox`). Ambient awareness
   comes from `clawdchan daemon`, which holds the relay link and fires an
   OS notification with copy that prompts the user back into the session
   ("Alice's agent replied — ask me about it"). That is fine for in-session
   collaboration but it is **not** a "wake me up" channel at OS boundaries.
   Async-wakeup use cases that cross devices route through OpenClaw, whose
   gateway reaches WhatsApp/Signal/iMessage.

   **Live-collab marker.** When the sender wraps an `ask` in the reserved
   `Content.Title="clawdchan:collab_sync"` marker (the `collab=true` flag
   on `clawdchan_message`), the receiver differentiates the notification
   copy ("X is collabing live") and the in-session suppression policy.
   The marker is a sender→receiver hint — it does not change the wire
   format or the signing form. An agent-cadence subprocess path that
   would answer these asks automatically is a future extension, not part
   of v0.

2. **No forward secrecy in v0.** The session key is a deterministic function
   of both nodes' long-term X25519 keys. Leaking a long-term key
   retroactively decrypts captured ciphertext. Upgrading to Noise_IK with
   ephemeral keys is non-breaking to the pairing protocol and envelope
   format. See the Session section.

3. **Single hosted relay.** The reference relay is a dumb frame forwarder
   and a pairing rendezvous point. It sees only ciphertext plus routing
   headers (node ids). For v0 we run one public instance; operators are free
   to self-host — the protocol is small. Federating multiple relays is not
   in scope for v0.

## Open questions

- **OpenClaw plugin runtime.** Can an OpenClaw plugin host a long-lived Go
  library in-process, or must it shell out to a sidecar? Determines whether we
  ship the core as a linkable library, a sidecar binary, or both. To be checked
  before phase 2.
- **Offline queuing SLA.** How long does the relay hold ciphertext for offline
  peers? 24h is a starting point; anything longer needs disk budget and retention
  policy at the relay.
- **Signature scheme evolution.** Ed25519 is v1. Reserve `version` byte for a
  future migration (e.g. Ed25519ph or a PQ-hybrid).
- **Thread topics / multi-thread-per-peer.** Schema supports many threads per
  peer, but v1 UX may open one default thread and defer topic UI.

## Versioning

- Wire `version = 1` for v1. Breaking changes bump this and ship with a
  deprecation window negotiated in the handshake.
- Envelope extensions go into a `CBOR map<int, any>` extension field guarded by
  a `supports_ext` capability in the handshake. Unknown extensions are
  preserved (and signed) but not acted on.
