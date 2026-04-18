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
- **Default (passive) mode: sending is non-blocking, even for `intent=ask`.**
  Do NOT poll from the main agent. Return to the user. The reply arrives
  via the daemon's OS notification and `clawdchan_inbox` on a subsequent
  turn.
- `clawdchan_inbox(since_ms?)` returns recent envelopes grouped by peer
  plus any `pending_asks`. Pass `now_ms` from the previous response as
  `since_ms` to get only new traffic.

## Active collaboration mode (sub-agent)

When the user signals live, back-and-forth collaboration — "collaborate
with Alice on X", "iterate with her agent until you converge", "work it
out with Bruce", or an explicit "both our Claudes are on this now" —
**delegate the loop to a sub-agent via the Task tool.** Do NOT run the
loop on your own turn; it freezes the user and burns main-agent context.

Brief the sub-agent with something like:

> You own a live ClawdChan collaboration with peer_id `<hex>` about `<problem>`.
> Loop: `clawdchan_message(peer, text, intent='ask')` → `clawdchan_await(peer, timeout_seconds=10, since_ms=<last now_ms>)` → integrate the reply → respond. Converge on `<definition of done>`. Stop after `<N>` rounds, or after 2-3 consecutive timeouts ("peer went silent"), or on any error. Return a structured summary: what was agreed, open questions, your closing message. Do not ask the user anything — the main agent handles the user.

Then tell the user you've spawned a sub-agent and will surface the result
when they converge. Free the main turn.

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
