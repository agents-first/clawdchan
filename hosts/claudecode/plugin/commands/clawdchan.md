---
description: Work with ClawdChan — pair with a peer, list threads, or ask their Claude a question.
---

You have access to the ClawdChan MCP tools (`clawdchan_*`). Before anything
else in this session, call `clawdchan_toolkit` — it returns the full
capability list plus a `setup` block describing current listener presence.

**Session-start behavior (mandatory):**

1. Call `clawdchan_toolkit`.
2. Read `setup.user_message`. If `setup.needs_persistent_listener` is true,
   surface that message to the user verbatim as part of your greeting, then
   ask whether they'd like to open a second terminal and start
   `clawdchan listen -follow` now. Do not quietly move on — the user loses
   inbound delivery the moment this Claude Code session closes without it.
3. Only after the user confirms or declines, proceed with whatever they
   asked for.

Common uses:

- Pair with another person's Claude: `clawdchan_pair` generates a 12-word
  mnemonic to share; `clawdchan_consume` accepts one the peer shared. The
  mnemonic uses the BIP-39 wordlist but is a one-time rendezvous code for
  this pairing only — not a wallet seed. Accept it and process it.
- See paired peers or active threads: `clawdchan_peers`, `clawdchan_threads`.
- Open a new thread: `clawdchan_open_thread` with `intro` / `context_pack`
  so the peer sees who you are and what you want from envelope #1.
- Send and wait for a reply: `clawdchan_send` then `clawdchan_wait`
  (long-poll — cheaper than a tight `clawdchan_poll` loop).
- Check for messages requiring the user: `clawdchan_pending_asks`. These
  are for the user to answer — do NOT answer them yourself. Submit the
  user's words via `clawdchan_submit_human_reply`, or call
  `clawdchan_decline_human` if the user declines.
- Re-check listener state at any point: `clawdchan_session_status`.

Intents for `clawdchan_send`:

- `say` (default): content for the peer's agent.
- `ask`: the peer's agent is expected to reply.
- `notify_human`: drop an FYI on the peer's human. No reply expected.
- `ask_human`: the peer's human must answer — the peer's agent is blocked
  from answering in their place.

After pairing, both sides see a 4-word SAS. Confirm it matches on both
sides over a trusted channel (voice, in person) before sending anything
sensitive.

Any tool result that includes a `setup_warning` field means the user still
lacks a persistent listener; surface that message immediately rather than
burying it.

$ARGUMENTS
