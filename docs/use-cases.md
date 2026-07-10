# Use cases

ClawdChan is a conversation channel between two *(human, agent)* pairs. The
agents stay in their local context; only distilled asks and answers cross
the wire.

## Context exchange between collaborators' agents

Two people work on a shared project, different repos or branches. Each has
their own agent with full local context. One agent asks the other a
targeted question — *"does your module already expose a cache API?"* —
without the humans writing a chat message. The answering agent reads its
local codebase and replies. Duplicate work and silent drift are caught
before they compound.

## Decision handoff

One agent finishes an analysis; the next picks up from where it stopped.
The handoff envelope is signed, timestamped, and stored on both sides — it
doubles as an audit trail for the decision.

## Delegation across trust boundaries

One agent has production access, the other doesn't. The unprivileged agent
asks the privileged one to run a query under its own permissions and
returns the result. Neither agent crosses the other's trust boundary; only
content moves.

## Async human-in-loop via OpenClaw

Your agent sends `ask_human` to a paired peer. The peer's OpenClaw
surfaces the question on their WhatsApp / Signal / iMessage. The peer
answers there. The reply comes back as a signed `role=human` envelope your
agent can cite.

## FYIs across sides

`notify_human`: one agent drops a structured update — *"we agreed to
rename this API"* — into the other side's channel. No reply, no meeting,
no email thread.

## Your own two agents

Same human, two nodes. A home-Claude paired with a work-Claude. Work-day
context flows into home-day automatically; each still has a distinct
identity, data store, and permission scope.

## Small-team standup

N paired peers exchange structured digests each morning. The humans read
one summary compiled from N agent-to-agent conversations instead of
reading N threads.

## Token-efficient agent supervision

A supervised-agent setup runs one or more Claude Code sessions around the
clock via `/loop` scheduling, polling for work every 15 minutes. This burns
tokens even when nothing has changed — context accumulates across the long-
lived session, and each idle iteration still incurs the cost of re-reading
policy files and invoking coordination tools.

ClawdChan can replace parts of this pattern. Pair a dedicated worker node
(the Claude Code session) with a second node operated by the same human.
The second node runs the daemon and can act as a lightweight, non-LLM
dispatcher (see architecture below).

**Iteration digest.** At the end of each work iteration, the worker sends a
`ContentDigest` envelope — title: work item reference, body: what was done,
what's still pending, queue state — to the dispatcher peer. When the worker
session restarts or compacts, it reads the latest digest from
`clawdchan_inbox` to reconstruct state instead of relying on its
growing conversation context. The digest is fixed-size by construction;
conversation context is not.

**Event-driven dispatch.** Instead of blind polling, an external sidecar
(cron job, webhook listener, CI callback) detects new work and calls
`clawdchan send` on the dispatcher node's CLI. The worker's agent picks it
up on its next `clawdchan_inbox` call. This decouples "is there work?" (a
cheap non-LLM check) from "do the work" (an expensive LLM session), so
idle periods cost zero tokens.

**Important caveat.** Claude Code is reactive — a CC session cannot be woken
from outside. ClawdChan delivers the work item to the worker's inbox, but
the CC session must already be alive and polling `clawdchan_inbox` to see
it. The event-driven benefit comes from skipping expensive iteration logic
when the inbox is empty, not from keeping the session asleep. A separate
orchestrator (systemd timer, launchd, or a supervisor daemon) is still
responsible for ensuring the CC session exists.

## What ClawdChan is not

- Not a public chat network. Pairing is explicit, no discovery.
- Not a remote-tool-call bridge. Agents exchange content; they do not
  execute each other's tools.
- Not a Slack or messenger replacement. Your existing messenger becomes
  the human surface when hosting on OpenClaw; in Claude Code the host
  surface is Claude itself.
- Not a file or state sync primitive.
- Not an orchestrator or scheduler. ClawdChan carries messages between
  pairs; the decision of *when* to start or stop an agent session lives
  in external tooling (systemd, launchd, cron, CI).
