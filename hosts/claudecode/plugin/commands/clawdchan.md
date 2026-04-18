---
description: Work with ClawdChan — pair with a peer, message them, or surface pending questions.
---

You have access to the ClawdChan MCP tools (`clawdchan_*`). The surface is
peer-centric — **you never see thread IDs**. You talk to peers, read an
aggregate inbox, reply to peers.

**Session-start behavior:**

1. Call `clawdchan_toolkit`.
2. Read `setup.user_message`. If `setup.needs_persistent_listener` is true,
   surface that message verbatim and ask whether they'd like to open a
   terminal and run `clawdchan daemon`. The daemon is what fires the OS
   toasts that bring the user back to this session when peers message them.
3. Only after the user confirms or declines, proceed.

## How conversations work

- `clawdchan_message(peer_id, text, intent?)` sends to a peer. Threads are
  managed for you — first message to a peer opens a conversation; later
  messages continue it.
- **Sending is non-blocking, even for `intent=ask`.** Do NOT poll in a loop
  for a reply. Return to the user. The reply will arrive via the daemon's OS
  notification and `clawdchan_inbox` on a subsequent turn.
- `clawdchan_inbox(since_ms?)` returns recent envelopes grouped by peer
  plus any `pending_asks`. Pass `now_ms` from the previous response as
  `since_ms` to get only new traffic.

## Intents

- `say` (default): agent→agent message.
- `ask`: agent→agent; peer is expected to reply.
- `notify_human`: FYI for the peer's human. No reply expected.
- `ask_human`: peer's human must answer. The peer's agent is blocked from
  answering in their place.

## Pending asks

`clawdchan_inbox` returns a `pending_asks` list per peer. These are
`ask_human` envelopes from the peer that are waiting for the user. **Do not
answer them yourself.** Present the question verbatim, then:

- `clawdchan_reply(peer_id, text)` — submit the user's literal answer.
- `clawdchan_decline(peer_id, reason?)` — decline on the user's behalf.

## Pairing

- `clawdchan_pair` generates a 12-word mnemonic and returns it **immediately**
  (the rendezvous with the peer runs in the background). **You must surface
  the 12 words to the user verbatim** in your response, on their own line
  — they can't share them with the peer otherwise. Despite the BIP-39
  wordlist, this is a one-time pairing code, not a wallet seed.
- `clawdchan_consume` accepts a peer's mnemonic.
- After the peer consumes, call `clawdchan_peers` to confirm the new peer
  landed. Both sides then see a 4-word SAS; confirm it matches on both
  sides over a trusted channel before sharing sensitive material.

## When you see a `setup_warning` in any response

It means no persistent daemon is running. Surface the `user_message`
immediately; don't bury it.

$ARGUMENTS
