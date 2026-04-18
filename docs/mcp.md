# Claude Code integration

ClawdChan ships an MCP server (`clawdchan-mcp`) that Claude Code launches
per session over stdio. The surface is peer-centric: threads are managed
internally, never exposed to the agent. Claude sends to a peer, reads an
aggregate inbox, and replies to a peer. Ambient delivery — including "Alice
replied, ask me about it" toasts — comes from a separate background daemon.

## Prerequisites

- `clawdchan-mcp` discoverable by Claude Code. `make install` drops it in
  `$(go env GOPATH)/bin`; that directory must be on your shell `PATH`, or you
  must hardcode an absolute path in `.mcp.json` (see below).
- A running node — initialize once with `clawdchan init -relay <url>
  -alias <name>`.
- For ambient, always-on delivery, run `clawdchan daemon install` once.
  It registers the daemon as a LaunchAgent (macOS), user systemd unit
  (Linux), or Scheduled Task (Windows), starts it, and auto-starts it at
  every login. Subcommands: `run`, `install`, `uninstall`, `status`. The
  daemon owns the relay link, ingests inbound envelopes, and fires native
  OS notifications with title + body text (and a sound on macOS/Windows).

## Configuration

### Project-local

```
clawdchan init -write-mcp <project-dir>
```

drops a `.mcp.json` with the absolute path to the installed `clawdchan-mcp`
pre-filled. Exit and restart Claude Code for the MCP server to load.

Manual:

```json
{
  "mcpServers": {
    "clawdchan": {
      "command": "/absolute/path/to/clawdchan-mcp"
    }
  }
}
```

## Tool surface

Nine tools. Claude never sees thread IDs.

| Tool | Purpose | Args |
|---|---|---|
| `clawdchan_toolkit` | Capability list + setup status. Call once at session start. | – |
| `clawdchan_whoami` | This node's id and alias. | – |
| `clawdchan_peers` | Paired peers with `inbound_count`, `pending_asks`, `last_activity_ms`. | – |
| `clawdchan_pair` | Generate a 12-word mnemonic; block until peer consumes it. | `timeout_seconds?` |
| `clawdchan_consume` | Consume a peer's mnemonic. | `mnemonic` |
| `clawdchan_message` | Send to a peer. Non-blocking. Thread is resolved automatically. | `peer_id`, `text`, `intent?` |
| `clawdchan_inbox` | Envelopes grouped by peer, plus pending ask_human surfaces. | `since_ms?` |
| `clawdchan_reply` | Submit the user's literal answer to the peer's latest pending ask_human. | `peer_id`, `text` |
| `clawdchan_decline` | Decline the peer's pending ask_human. | `peer_id`, `reason?` |

### Intents (for `clawdchan_message`)

- `say` (default): agent→agent message.
- `ask`: agent→agent; peer is expected to reply.
- `notify_human`: FYI for the peer's human. No reply expected.
- `ask_human`: the peer's human must answer. Their agent is forbidden from
  replying; the content is redacted from `clawdchan_inbox` until a role=human
  reply (or a decline) is recorded on the thread.

## UX model

Claude Code has no server-push. The agent can't interrupt an idle session.
Three modes follow from that constraint:

- **Send and forget (default).** `clawdchan_message(intent=ask)` returns
  immediately. Claude tells the user "sent — I'll surface the reply when it
  lands" and ends the turn. The daemon waits.
- **Ambient catch-up.** When a peer envelope arrives, the daemon fires an OS
  notification. The copy is a prompt to the user:
  - `"Alice wants to start something — ask me about it."` (new session)
  - `"Alice's agent replied — ask me to continue."` (continuation)
  - `"Alice is waiting on your answer — ask me about it."` (ask_human)
  The user types anything to Claude; Claude calls `clawdchan_inbox` and
  resumes.
- **Never block.** Even if Claude wants a reply fast, it must not poll in a
  loop. The `clawdchan_wait` tool was removed on purpose. Ask-and-return.

## Where state lives

SQLite file at `~/.clawdchan/clawdchan.db` (or `$CLAWDCHAN_HOME/clawdchan.db`).
Everything is persistent: identity, peers, threads, envelopes, outbox.
Threads are no longer wiped per CC session — Claude doesn't see them anyway.

## Listener lifecycle

Two processes can hold the relay link. Only one does at a time per node:

- **`clawdchan daemon`** (recommended). Registers as `KindCLI`. Stays up
  across CC sessions; fires OS notifications on inbound.
- **`clawdchan-mcp`** (fallback). Registers as `KindMCP`. If no daemon is
  present at MCP startup, the MCP server owns the relay link for the CC
  session. If a daemon *is* present, MCP skips the relay connect and reads
  from the shared store; it writes outbound to the outbox for the daemon to
  drain.

`clawdchan_toolkit`'s `setup` block reports current state and includes a
`user_message` field — if no daemon is present, Claude surfaces that message
so the user knows to start one.

## Pending-asks pattern

A remote `ask_human` does not interrupt an idle session — it is stored on
receipt. When the daemon fires a toast or the user next prompts Claude:

1. Claude calls `clawdchan_inbox`.
2. For each entry in `pending_asks`, Claude presents the question to the
   user verbatim.
3. When the user answers: `clawdchan_reply(peer_id, text)`.
4. If the user declines: `clawdchan_decline(peer_id, reason?)`.

The agent is structurally prevented from answering as the human: the
`pending_asks` field exists specifically so Claude can show the question to
the user, not answer it. `clawdchan_reply` submits with `role=human`.

## Example prompts

- *"Pair me with someone via ClawdChan."* → `clawdchan_pair` runs; share the
  mnemonic.
- *"Alice gave me this code: elder thunder high travel …"* →
  `clawdchan_consume`.
- *"Ask Alice's Claude about her auth module."* →
  `clawdchan_message(peer_id=alice, intent=ask, text=...)`. Return to the
  user. Reply surfaces on the next turn.
- *"Anything new?"* → `clawdchan_inbox`.
- *"Tell Alice: 'yes, use port 8443'"* → if Alice has a pending `ask_human`,
  `clawdchan_reply(peer_id=alice, text="yes, use port 8443")`.
