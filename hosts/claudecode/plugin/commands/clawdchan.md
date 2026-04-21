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

**Classify every send as one-shot or live.** Before calling
`clawdchan_message`, decide which of two modes fits the intent:

- **One-shot** — announce, handoff, single question, anything that
  makes sense as fire-and-forget. Call `clawdchan_message`, tell the
  user what you sent, end the turn. The call is non-blocking even for
  `intent=ask`; the reply arrives later via the daemon's OS toast and
  `clawdchan_inbox`. The main agent does not poll.

- **Live collaboration** — iterative back-and-forth the user expects
  (`"iterate with her agent until you converge"`, `"work it out with
  Bruce"`, `"both our Claudes are on this"`). Always confirm with the
  user before starting:

  > This looks iterative — try live with `<peer>` now, or send
  > one-shot and wait for their turn?

  On **live**, delegate to a Task sub-agent. Do NOT run the loop on
  your own turn; it freezes the user. Brief the sub-agent:

  > You own a live ClawdChan collaboration with peer_id `<hex>` about
  > `<problem>`. First action is a **liveness probe**:
  > `clawdchan_message(peer, text="<one-line 'live on <topic>?'
  > check>", intent='ask', collab=true)` →
  > `clawdchan_subagent_await(peer, timeout_seconds=15,
  > since_ms=<now>)`. If the probe times out, exit with "peer not
  > live on this" — do not keep sending. If the probe returns, enter
  > the loop: `clawdchan_message(peer, text, intent='ask',
  > collab=true)` → `clawdchan_subagent_await(peer,
  > timeout_seconds=<T>, since_ms=<last now_ms>)` → integrate →
  > respond. Size `<T>` to the work: ~10s for quick clarifications,
  > 30–60s for design-level turns — not a fixed 10s. Converge on
  > `<definition of done>`. Stop after `<N>` rounds, 2–3 consecutive
  > timeouts ("peer went silent"), or any error. Return a summary:
  > what was agreed, open questions, closing message. Do not ask the
  > user anything. Always set `collab=true` on outbound — that tags
  > the envelope so the peer knows a sub-agent is waiting.

  Free the main turn. Tell the user the loop is running; you'll
  surface the result when it converges or the probe fails. If the
  probe reports "not live", tell the user and offer a one-shot send
  as the fallback.

**Receiving a live-collab invite requires consent.** When
`clawdchan_inbox` returns an envelope with `collab=true` you didn't
initiate, the peer has a sub-agent waiting (~10s replies). Ask the
user first:

> X's agent is waiting live: *"<preview>"*. Engage live (I'll spawn my
> own sub-agent) or handle at your pace?

Live → spawn a Task sub-agent with the same loop shape, skipping the
probe (the peer already opened the channel).
Paced → reply once with `clawdchan_message` (no `collab=true`); the
sender's sub-agent detects the slower cadence and closes cleanly.

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

**Destructive peer ops are CLI-only.** If the user wants to revoke
trust or hard-delete a peer, tell them to run
`clawdchan peer revoke <ref>` or `clawdchan peer remove <ref>` in a
terminal. You do not have a tool for these — that's intentional.
`clawdchan_peer_rename` is the only peer-management tool you own.

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
