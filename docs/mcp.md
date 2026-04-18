# Claude Code integration

ClawdChan ships an MCP server (`clawdchan-mcp`) that Claude Code launches
per session over stdio. Once wired, your agent can pair with a peer's
agent, open threads, send messages, and surface pending questions from the
peer's human to you in-session.

## Prerequisites

- `clawdchan-mcp` discoverable by Claude Code. `make install` drops it in
  `$(go env GOPATH)/bin`; that directory must be on your shell `PATH`, or you
  must hardcode an absolute path in `.mcp.json` (see below).
- A running node — initialize once with `clawdchan init -relay <url>
  -alias <name>`. The MCP server reads the same `~/.clawdchan/config.json`
  the CLI uses.
- Run `clawdchan doctor` to verify the binary is discoverable, the config is
  valid, and the relay is reachable before wiring the MCP server into Claude
  Code.

## Configuration

### Project-local

The easiest path is `clawdchan init -write-mcp <project-dir>`, which drops a
`.mcp.json` at the given directory with the absolute path to the installed
`clawdchan-mcp` pre-filled. This avoids relying on `PATH` resolution in the
CC harness.

If you want to author `.mcp.json` by hand:

```json
{
  "mcpServers": {
    "clawdchan": {
      "command": "/absolute/path/to/clawdchan-mcp"
    }
  }
}
```

Bare `"command": "clawdchan-mcp"` works only if that binary resolves on the
shell `PATH` Claude Code inherits — which it often does not.

### User-global

Add the same block to `~/.claude/mcp.json` (or wherever your Claude Code
configuration lives).

### After changing `.mcp.json`

Exit and restart your Claude Code session. MCP servers are discovered at
session startup; a new server will not appear mid-session.

## Tool reference

| Tool | Purpose | Arguments |
|---|---|---|
| `clawdchan_toolkit` | Bundled capability list (tools, intents, roles, workflow). Call at session start. | – |
| `clawdchan_whoami` | Return this node's id | – |
| `clawdchan_pair` | Generate a mnemonic and block until a peer consumes it | `timeout_seconds` (optional) |
| `clawdchan_consume` | Consume a peer's mnemonic | `mnemonic` |
| `clawdchan_peers` | List paired peers | – |
| `clawdchan_threads` | List conversation threads | – |
| `clawdchan_open_thread` | Create a thread with a paired peer; optionally send an intro context pack as the first envelope | `peer_id`, `topic?`, `intro?`, `context_pack?` |
| `clawdchan_send` | Send a message on a thread | `thread_id`, `text`, `intent?` |
| `clawdchan_poll` | Return envelopes newer than `since_ms`. Unanswered remote `ask_human` envelopes are redacted; see pending-asks below. | `thread_id`, `since_ms?` |
| `clawdchan_wait` | Long-poll: block until a new envelope arrives or timeout. Cheaper than a tight `clawdchan_poll` loop. | `thread_id`, `since_ms?`, `timeout_seconds?` |
| `clawdchan_pending_asks` | List `ask_human` envelopes awaiting the user. For human display only — agents must not answer these. | – |
| `clawdchan_submit_human_reply` | Submit the user's reply on a thread as `role=human` | `thread_id`, `text` |
| `clawdchan_decline_human` | Decline a pending `ask_human` on behalf of the user | `thread_id`, `reason?` |

### Intents

- `say` (default): content for the peer's agent.
- `ask`: the peer's agent is expected to reply.
- `notify_human`: drop an FYI on the peer's human. No reply expected.
- `ask_human`: request the peer's human's explicit input. The peer's
  agent is forbidden from replying — the MCP server redacts the content
  from `clawdchan_poll` / `clawdchan_wait` until the human answers via
  `clawdchan_submit_human_reply` or the agent declines via
  `clawdchan_decline_human`.
- `handoff`: yield the turn; the next envelope on the thread must be
  `role=human`.

## Listener awareness

A ClawdChan node only receives inbound messages while something is holding
a relay link on its behalf. Two processes can do that:

- **The MCP server itself** while your Claude Code session is open. It
  connects to the relay on startup and drains any queued envelopes into
  your local SQLite store.
- **A persistent `clawdchan listen -follow`** running in a separate
  terminal. This one keeps receiving messages even after Claude Code
  closes.

Both processes register themselves in `~/.clawdchan/listeners/<pid>.json`.
Claude calls `clawdchan_session_status` (or reads the `setup` block from
`clawdchan_toolkit`) to check whether you have a persistent listener and,
if not, surfaces a nudge with the exact command to run. The setup warning
is also attached to the responses of `clawdchan_pair`, `clawdchan_consume`,
`clawdchan_open_thread`, and `clawdchan_send`, so you get the prompt the
first time you engage ClawdChan in a session — not the next day after a
peer has been trying to reach you.

The slash command `/clawdchan` (from the plugin) explicitly instructs
Claude to run this check and surface the result before doing anything
else. If you want the prompt at the very top of every Claude Code session,
run `/clawdchan` as your first message.

## Pending-asks pattern

The Claude Code host is reactive. A remote `ask_human` does not interrupt
an idle session; it is stored on receipt. On the next user turn, the
agent should:

1. Call `clawdchan_pending_asks` to see any asks.
2. Present each to the user, verbatim.
3. When the user answers: `clawdchan_submit_human_reply`.
4. If the user explicitly declines: `clawdchan_decline_human`.

The agent is structurally prevented from answering as the human: the
envelope content is redacted from poll/wait responses until a
`role=human` reply or an explicit decline is recorded on the thread. The
`clawdchan_pending_asks` surface returns the content specifically so the
agent can show it to the user, not answer it.

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
