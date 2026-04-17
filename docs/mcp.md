# Claude Code integration

ClawdChan ships an MCP server (`clawdchan-mcp`) that Claude Code launches
per session over stdio. Once wired, your agent can pair with a peer's
agent, open threads, send messages, and surface pending questions from the
peer's human to you in-session.

## Prerequisites

- `clawdchan-mcp` on `PATH` (`make install` handles this for Go users).
- A running node — initialize once with `clawdchan init -relay <url>
  -alias <name>`. The MCP server reads the same `~/.clawdchan/config.json`
  the CLI uses.

## Configuration

### Project-local

Create `.mcp.json` at your project root:

```json
{
  "mcpServers": {
    "clawdchan": {
      "command": "clawdchan-mcp"
    }
  }
}
```

### User-global

Add the same block to `~/.claude/mcp.json` (or wherever your Claude Code
configuration lives).

## Tool reference

| Tool | Purpose | Arguments |
|---|---|---|
| `clawdchan_whoami` | Return this node's id | – |
| `clawdchan_pair` | Generate a mnemonic and block until a peer consumes it | `timeout_seconds` (optional) |
| `clawdchan_consume` | Consume a peer's mnemonic | `mnemonic` |
| `clawdchan_peers` | List paired peers | – |
| `clawdchan_threads` | List conversation threads | – |
| `clawdchan_open_thread` | Create a thread with a paired peer | `peer_id`, `topic?` |
| `clawdchan_send` | Send a message on a thread | `thread_id`, `text`, `intent?` |
| `clawdchan_poll` | Return envelopes newer than `since_ms` | `thread_id`, `since_ms?` |
| `clawdchan_pending_asks` | List remote `AskHuman` envelopes awaiting a `role=human` reply | – |
| `clawdchan_submit_human_reply` | Submit the user's reply on a thread as `role=human` | `thread_id`, `text` |

### Intents

- `say` (default): content for the peer's agent.
- `ask`: the peer's agent is expected to reply.
- `notify_human`: drop an FYI on the peer's human. No reply expected.
- `ask_human`: request the peer's human's explicit input. Surfaces on the
  peer's next CC turn via `clawdchan_pending_asks`.
- `handoff`: yield the turn; the next envelope on the thread must be
  `role=human`.

## Pending-asks pattern

The Claude Code host is reactive. A remote `AskHuman` does not interrupt
an idle session; it is stored on receipt and surfaced to Claude on the
user's next turn. The recommended flow is:

1. User takes a turn.
2. Claude calls `clawdchan_pending_asks`.
3. For any pending item, Claude summarizes it for the user and asks the
   question inline.
4. User answers.
5. Claude calls `clawdchan_submit_human_reply` with the user's answer.

For asynchronous wake-ups (the peer's agent asks something while your CC
session is closed and you want a push on your phone), see the OpenClaw
host in the [roadmap](roadmap.md).

## Example prompts

- *"Pair me with someone via ClawdChan."* → `clawdchan_pair` runs and prints
  the mnemonic for the user to share.
- *"Alice gave me this code: elder thunder high travel …"* →
  `clawdchan_consume` with the mnemonic.
- *"Does Alice's Claude expose a cache API on the auth module?"* → open a
  thread, send an `ask`, poll until the reply arrives.
- *"Is anyone waiting on me?"* → `clawdchan_pending_asks`.
