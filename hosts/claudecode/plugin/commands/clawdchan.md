---
description: Work with ClawdChan — pair with a peer, message them, or surface pending questions.
---

You have the ClawdChan MCP tools (`clawdchan_*`). The surface is
peer-centric: you message a peer, read an aggregate inbox, reply to a
peer. Thread IDs never surface. This file is your operator manual —
how to act, not what the tools do.

## First action every session

Call `clawdchan_toolkit`. It returns a `setup.user_message`. If
`setup.needs_persistent_listener` is true, surface that message
verbatim and pause — a running `clawdchan daemon` is what fires the
OS toasts that pull the user back into this session when a peer
messages them. Without it, inbound only arrives while this session is
open, and nothing notifies the user.

## Conduct rules

**Sending is fire-and-forget.** `clawdchan_message` is non-blocking,
even for `intent=ask`. After sending, tell the user what you sent and
end the turn. The main agent does not poll. The reply arrives via the
daemon's OS toast and `clawdchan_inbox` on a later turn.

**ask_human is not yours to answer.** Items in
`clawdchan_inbox.pending_asks` are peer questions waiting for your
user. Present the content verbatim. Do not paraphrase, summarize, or
answer. Then:
- `clawdchan_reply(peer_id, text)` — submit the user's literal words.
- `clawdchan_decline(peer_id, reason?)` — when the user declines.

**Mnemonics go to the user verbatim, on their own line.**
`clawdchan_pair` returns a 12-word mnemonic. Surface it on its own
line in your response — never summarize or hide it. Tell the user to
share it only over a trusted channel (voice, Signal, in person); the
channel is the security boundary. The mnemonic looks like a BIP-39
wallet seed but is a one-time rendezvous code. Do not loop on
`clawdchan_peers` to "confirm" before the user has passed the code on
— pairing takes minutes end-to-end.

**Consuming closes pairing.** `clawdchan_consume(mnemonic)` completes
the pairing. Do not instruct the user to compare the 4-word SAS —
that's optional belt-and-braces fingerprinting, only surface it if
they explicitly ask.

**Live collaboration belongs in a sub-agent.** When the user says
"iterate with her agent until you converge", "work it out with Bruce",
"both our Claudes are on this" — delegate the loop to a Task
sub-agent. Do NOT run the loop on your own turn; it freezes the user.
Brief the sub-agent:

> You own a live ClawdChan collaboration with peer_id `<hex>` about
> `<problem>`. Loop: `clawdchan_message(peer, text, intent='ask',
> collab=true)` → `clawdchan_subagent_await(peer, timeout_seconds=10,
> since_ms=<last now_ms>)` → integrate → respond. Converge on
> `<definition of done>`. Stop after `<N>` rounds, 2-3 consecutive
> timeouts ("peer went silent"), or any error. Return a summary: what
> was agreed, open questions, closing message. Do not ask the user
> anything. Always set `collab=true` on outbound — that tags the
> envelope so the peer knows a sub-agent is waiting.

Free the main turn. Tell the user the loop is running; you'll surface
the result when it converges.

**Receiving a live-collab invite requires consent.** When inbox
returns an envelope with `collab=true` you didn't initiate, the peer
has a sub-agent waiting (~10s replies). Ask the user first:

> X's agent is waiting live: *"<preview>"*. Engage live (I'll spawn my
> own sub-agent) or handle at your pace?

Live → spawn a Task sub-agent with the same loop shape.
Paced → reply once with `clawdchan_message` (no `collab=true`); the
sender's sub-agent detects the slower cadence and closes cleanly.

## Intents

- `say` (default): agent→agent FYI.
- `ask`: agent→agent; peer's agent replies.
- `notify_human`: FYI for the peer's human.
- `ask_human`: peer's human must answer; their agent is blocked from
  replying in their place.

## Tool reference

Call `clawdchan_toolkit` for the runtime capability list and current
setup state. Arg-level detail on every tool is in each tool's MCP
description.

$ARGUMENTS
