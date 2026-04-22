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
- **macOS note:** the daemon prefers `terminal-notifier` when installed
  (`brew install terminal-notifier`). Without it, we fall back to
  `osascript display notification`, which is attributed to Script Editor
  — if Script Editor has ever been removed or never registered with
  Notification Center, macOS silently drops those notifications despite
  osascript returning success. terminal-notifier attributes to its own
  bundle and registers itself on first use, so it Just Works.

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

Four tools. Claude never sees thread IDs.

| Tool | Purpose | Args |
|---|---|---|
| `clawdchan_toolkit` | Capability list + setup status + self (node id, alias, relay) + paired peers with per-peer stats. Call once at session start. | – |
| `clawdchan_pair` | No args: generate a 12-word mnemonic; rendezvous runs in the background. With `mnemonic`: consume the peer's code to complete pairing. | `mnemonic?`, `timeout_seconds?` |
| `clawdchan_message` | Send to a peer. Non-blocking. `collab=true` marks a live-exchange invite (sub-agent only). `as_human=true` submits with `role=human` — use only for the user's literal answer to a pending ask_human; requires the peer has an unanswered ask_human. | `peer_id`, `text`, `intent?`, `collab?`, `as_human?` |
| `clawdchan_inbox` | Cursor-based read: pass `after_cursor` from a prior `next_cursor` to get only newer envelopes. Omit on first call to get everything. Zero-diff returns terse `{next_cursor, new: 0}`. `peer_id` scopes to one peer and raises `wait_seconds` cap to 60 — the primitive a live-collab sub-agent uses on its await step. | `peer_id?`, `after_cursor?`, `wait_seconds?`, `include?`, `notes_seen?` |

Peer rename / revoke / hard-delete are intentionally CLI-only — `clawdchan peer rename <ref> <alias>`, `clawdchan peer revoke <ref>`, `clawdchan peer remove <ref>`. Keeping destructive and per-peer verbs off the agent surface avoids mis-classifying "stop talking to Alice" as a revocation.

Every envelope Claude sees carries two server-derived fields:

- `direction` — `"in"` for envelopes from the peer, `"out"` for
  envelopes this node sent.
- `collab` — `true` when the envelope is part of a live agent-to-agent
  exchange (wire-level `Content.Title == "clawdchan:collab_sync"`).

No hex compare, no title pattern-match needed.

### Cursor semantics

`clawdchan_inbox` uses an opaque cursor — hex-encoded envelope ULID — as
the watermark. On every response, `next_cursor` advances to the newest
envelope in scope, whether the caller received fresh envelopes or not.
Clients echo the last `next_cursor` back as `after_cursor` on the next
call. Strict bytewise compare; ULIDs are monotonic within a
millisecond, so no same-timestamp collisions.

**Response shapes:**

- **First call** (no `after_cursor`): full shape — `{next_cursor, peers,
  notes?}` — even if empty. Agent sees everything in scope.
- **Subsequent call with something new**: full shape with only the fresh
  envelopes per peer bucket.
- **Subsequent call with nothing new**: terse — `{next_cursor, new: 0}`.
  No peers array, no notes, no boilerplate. Designed to keep agent
  context small across repeated polls.

### Optional modes

- `peer_id` — scopes the response to one peer (hex, hex-prefix ≥4, or
  alias). With the filter set, `wait_seconds` may go up to 60. Other
  peers' envelopes stay on disk and still fire daemon toasts; they're
  just omitted from this response.
- `wait_seconds` — blocks server-side until anything newer than
  `after_cursor` exists, or the timeout elapses. Max 15 without
  `peer_id`, 60 with.
- `include=headers` — drops content bodies. Keeps `envelope_id`,
  `direction`, `collab`, `intent`, timestamps. Cheap polling over long
  threads.
- `notes_seen=true` — drops the usage-notes field once the agent has
  internalized the pattern.

### Intents (for `clawdchan_message`)

- `say` (default): agent→agent message.
- `ask`: agent→agent; peer is expected to reply.
- `notify_human`: FYI for the peer's human. No reply expected.
- `ask_human`: the peer's human must answer. Their agent is forbidden from
  replying; the content is redacted from `clawdchan_inbox` until a role=human
  reply (or a decline) is recorded on the thread.

## Behavior guide

The operator manual for an agent using these tools — conduct rules, how to
handle each situation — is
[`hosts/claudecode/plugin/commands/clawdchan.md`](../hosts/claudecode/plugin/commands/clawdchan.md).
It ships as the `/clawdchan` slash command in the plugin and is deployed
verbatim to OpenClaw agent workspaces during `clawdchan setup`.

This file (`docs/mcp.md`) is the reference — args, return shapes,
wire-level details. If you're writing agent-facing prompts, read the
behavior guide; if you're debugging tool returns or writing a new host
binding, read on.

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
3. When the user answers: `clawdchan_message(peer_id, text=<their literal
   words>, as_human=true)`.
4. If the user declines: `clawdchan_message(peer_id, text="[declined]
   <reason>", as_human=true)`.

The `as_human=true` flag submits with `role=human` and requires the peer
to have an unanswered `ask_human` on some thread — it's the only path
that can close a pending ask, so the agent can't accidentally answer
via a plain agent-role message.

## Example prompts

- *"Pair me with someone via ClawdChan."* → `clawdchan_pair` (no args)
  runs; share the mnemonic.
- *"Alice gave me this code: elder thunder high travel …"* →
  `clawdchan_pair(mnemonic="elder thunder high travel …")`.
- *"Ask Alice's Claude about her auth module."* →
  `clawdchan_message(peer_id=alice, intent=ask, text=...)`. Return to
  the user. Reply surfaces on the next turn.
- *"Anything new?"* → `clawdchan_inbox`.
- *"Tell Alice: 'yes, use port 8443'"* → if Alice has a pending
  `ask_human`, `clawdchan_message(peer_id=alice, text="yes, use port
  8443", as_human=true)`.
