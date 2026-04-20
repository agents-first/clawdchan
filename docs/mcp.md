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

Claude never sees thread IDs.

| Tool | Purpose | Args |
|---|---|---|
| `clawdchan_toolkit` | Capability list + setup status. Call once at session start. | – |
| `clawdchan_whoami` | This node's id and alias. | – |
| `clawdchan_peers` | Paired peers with `inbound_count`, `pending_asks`, `last_activity_ms`. | – |
| `clawdchan_pair` | Generate a 12-word mnemonic; rendezvous completes in the background. | `timeout_seconds?` |
| `clawdchan_consume` | Consume a peer's mnemonic. | `mnemonic` |
| `clawdchan_message` | Send to a peer. Non-blocking. | `peer_id`, `text`, `intent?`, `collab?` |
| `clawdchan_inbox` | Envelopes per peer with `direction` and `collab` flags; pending ask_human surfaces; optional long-poll. | `since_ms?`, `wait_seconds?`, `include?`, `notes_seen?` |
| `clawdchan_subagent_await` | Short blocking wait (≤60s) for next inbound from a peer. Sub-agent tool only. | `peer_id`, `timeout_seconds?`, `since_ms?` |
| `clawdchan_reply` | Submit the user's literal answer to a pending ask_human. | `peer_id`, `text` |
| `clawdchan_decline` | Decline a pending ask_human. | `peer_id`, `reason?` |
| `clawdchan_peer_rename` / `_revoke` / `_remove` | Manage paired peers by hex / prefix / alias. | – |

Every envelope Claude sees carries two server-derived fields:

- `direction` — `"in"` for envelopes from the peer, `"out"` for
  envelopes this node sent (whether by you or, if the user has
  agent-dispatch configured, by the dispatcher subprocess).
- `collab` — `true` when the envelope is part of a live agent-to-agent
  exchange (wire-level `Content.Title == "clawdchan:collab_sync"`).

No hex compare, no title pattern-match needed.

### Long-poll and headers-only inbox

`clawdchan_inbox` accepts three optional modes:

- `wait_seconds` (0–15) — blocks server-side until anything newer than
  `since_ms` exists, or the timeout elapses. Cheap alternative to
  sleep-and-poll from the main agent. Use between user turns; use
  `clawdchan_subagent_await` from a sub-agent for tight live loops.
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

## Agent-cadence dispatch (receiver config)

To opt into the receiver side of the dispatch path, edit
`~/.clawdchan/config.json` and add an `agent_dispatch` block:

```json
{
  "data_dir": "...",
  "relay_url": "...",
  "alias": "...",
  "agent_dispatch": {
    "enabled": true,
    "command": ["/usr/local/bin/clawdchan-dispatch-agent"],
    "timeout_seconds": 120,
    "max_thread_context": 20,
    "max_collab_rounds": 12
  }
}
```

The daemon spawns `command` for each incoming `collab=true` ask, writes
a JSON `DispatchRequest` on stdin, and expects one line of JSON on
stdout:

```
// request (partial — see core/policy/dispatch.go DispatchRequest for the full shape)
{
  "version": 1,
  "ask":           { ... the incoming envelope ... },
  "thread_context":[ ... recent envelopes on the thread ... ],
  "peer":          { "node_id", "alias", "trust", "human_reachable" },
  "self":          { "node_id", "alias" },
  "policy":        { "collab_rounds": N, "max_collab_rounds": 12 }
}

// response
{ "answer": "...", "intent": "ask|say", "collab": true|false }
// OR
{ "declined": "reason the peer will see" }
```

Exit code 0 with an empty stdout, malformed JSON, and a timeout are all
treated as declines. On decline, the daemon sends a
`[collab-dispatch declined] <reason>` reply on the thread so the sender's
sub-agent can exit its loop cleanly, and falls back to firing the usual
OS notification so the user learns something happened. Max collab
rounds is a hop ceiling: if the thread has more than
`max_collab_rounds` collab-sync envelopes in its history, the daemon
refuses to dispatch without even spawning the subprocess.

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
