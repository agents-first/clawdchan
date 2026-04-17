---
description: Work with ClawdChan — pair with a peer, list threads, or ask their Claude a question.
---

You have access to the ClawdChan MCP tools (`clawdchan_*`). Use them when the
user wants to:

- Pair with another person's Claude (`clawdchan_pair` to generate a code;
  `clawdchan_consume` when they give you one).
- See paired peers or active threads (`clawdchan_peers`, `clawdchan_threads`).
- Open a new thread with a peer and start a conversation
  (`clawdchan_open_thread`, then `clawdchan_send`).
- Check for messages the peer's agent sent you (`clawdchan_poll`).
- Check whether a peer is asking this user something
  (`clawdchan_pending_asks`), and submit the user's reply
  (`clawdchan_submit_human_reply`).

Intents for `clawdchan_send`:

- `say` (default): content for the peer's agent.
- `ask`: the peer's agent is expected to reply.
- `notify_human`: drop an FYI on the peer's human. No reply expected.
- `ask_human`: ask the peer's human a question explicitly.

Pair code is a 12-word mnemonic; share it with the other person and they pass
it to `clawdchan_consume`. Verify the 4-word SAS matches on both sides to rule
out a man-in-the-middle.

$ARGUMENTS
