# Collaboration UX feedback — maroon-1 side

Session: 2026-04-19. Author: agent on maroon node `19466133…`. Counterpart: agent on maroon-2 node `2948e499…`. Single relay, both sides via Claude Code MCP.

This report is grounded in what actually happened in this session — not a generic checklist.

## What worked

- **Addressing.** Alias, exact alias post-rename, 8-char hex prefix, full hex — all four resolved to the same peer with no surprise. `clawdchan_message` `peer_id` is the right level of polymorphism.
- **`peer_rename` semantics.** Rename is local-only; the peer's self-declared alias is unaffected. Round-trip clean. Naming distinction (rename / revoke / remove) is correct — agreed with maroon-2 that keeping revoke and remove split is the right call (rm vs shred).
- **Intent vocabulary.** say / ask / notify_human / ask_human is a small, semantically distinct set. ask_human round-trip closed cleanly: the human typed "ack" and it came back as `from_role=human`.
- **`pending_asks` lingering across `since_ms`.** Smart default — unanswered asks don't fall off the bottom of the feed when the agent advances its watermark. Noticed this in passing; it's a thoughtful asymmetry.
- **Peer-centric tool surface.** Threads hidden from the agent. Mental model is "talk to a peer," not "manage thread state." Fewer foot-guns.
- **`toolkit` as session-start onboarding.** Single call returns the full surface plus recommended workflow. Worth keeping.

## Friction (in rough order of impact)

### 1. Agent ↔ agent flows degrade to human cadence

Biggest issue, surfaced live in this session. Each `ask` I sent to maroon-2's agent sat in their inbox until their human prompted them to look. From my POV the peer was idle for minutes; from theirs, no signal that I was actively blocked vs. moved on. `collab=true` only helps inside a `Task` sub-agent loop, and there's no a2a equivalent of `pending_asks` / auto-engagement. Net effect: the protocol silently degrades to "two humans chat-piloting their agents," which defeats the agent-to-agent point.

Proposal converged with maroon-2 (see their report for the symmetric writeup):

- Reuse the existing `collab=true` flag — do **not** add a new `blocking=true` wire field (avoids deterministic-CBOR signing-form change + design.md edit).
- New responsibility for `clawdchan daemon`: when an inbound ask arrives with `collab=true` and local policy permits, daemon dispatches a configured agent subprocess with the ask + minimal thread context, routes its answer back as a normal envelope.
- Subprocess over in-process: keeps daemon host-agnostic per the `core/ imports nothing from hosts/` invariant; crash-isolated; policy is cleaner at the process boundary.
- Contract: `stdin = JSON {ask, thread_context}`, `stdout = JSON {answer | declined: reason}`, exit code = ok|fail. Config in `~/.clawdchan/config.toml` as `agent_command`.
- Lives in `core/policy/dispatch.go` (new file) + a daemon hook. Update design.md §295–304 in the same change — the "CC is reactive only" framing needs the new mode called out, and §303–304 (cross-device async via OpenClaw) should note the local-daemon collapse.

### 2. Inbox echoes outbound envelopes

When I poll `clawdchan_inbox` with `since_ms` after sending, my own envelopes come back tagged with `from_node` = my own. I have to filter `from_node != self` on every read. Doubles the visible "new" volume in active threads.

Two fixes considered:
- **A:** inbox returns peer-originated only; outbound queryable via a separate tool.
- **B:** keep the full thread but tag each envelope `direction: "in" | "out"`. Server-side derivation, no information loss.

Lean B. Full thread is genuinely useful for in-context reasoning; the agent shouldn't have to compare hex strings to derive direction.

### 3. Main agent has no clean wait primitive

`clawdchan_await` is explicitly sub-agent only ("Do NOT call from the main agent — it freezes the user-facing turn"). Correct. But that leaves the main agent with `Bash sleep` + repeated `clawdchan_inbox` polling, which the harness itself discourages (it blocks standalone sleeps and pushes toward Monitor / until-loops). Result: the main agent ping-pongs between sleep and inbox calls, each one burning a turn and prompt cache.

Options worth considering:
- A `clawdchan_inbox` mode that returns immediately if there's new traffic since `since_ms`, otherwise blocks server-side up to a short bounded timeout (say 5–10s). Cheaper than polling, doesn't require sub-agent ceremony.
- Or: lean harder on the daemon-dispatch path (item 1) so the main agent doesn't need to wait at all — outcomes flow to it via toast/inbox between user turns.

### 4. No "is the peer's session live?" signal

`peers` returns `human_reachable: true` but doesn't say whether the MCP server / daemon on the other side is currently processing. Sending into an idle session is indistinguishable from sending to an active one until you wait and see. For collab pacing this matters — am I waiting on relay (fast), on the peer's daemon (medium), or on the peer's human (slow)?

Lightweight fix: relay-side liveness ("peer connected within last N seconds") surfaced in `peers` as `link_active_within_ms` or similar. Doesn't require a new wire envelope.

### 5. Inbox payload weight

Each inbox call returns the full envelope `content.text` for every envelope in the window. For a long collab thread that's bytes-per-poll proportional to thread length × poll frequency. No header-only mode.

Minor priority — only matters at scale — but consider an `include=headers|full` flag, default `full` for back-compat.

### 6. Send-side opacity

`clawdchan_message` returns `ok: true, sent_at_ms` on relay ack. That's "relay forwarded" not "peer's inbox surfaced it" and certainly not "peer's agent read it." In a collab loop that ambiguity matters — see item 4.

Possible: include `peer_link_status: "online" | "offline_queued"` in the send response, derived from relay knowledge of the recipient's connection.

## Smaller notes

- The `notes` field in `clawdchan_inbox` is helpful onboarding-on-every-call, but after the first session the agent has internalized it. A `notes_seen=true` opt-out would save tokens.
- Envelope IDs (`019da5a1d822…`) are not human-typable. Fine for machines, but the agent ends up paraphrasing rather than referencing IDs in conversation. Probably correct as-is.
- The `digest` content kind (saw it in earlier inbox traffic with `kind: "digest", title: "clawdchan:collab_sync"`) is interesting — a structured-content path that doesn't get surfaced in the docs I've seen. Worth either documenting or pruning.

## Summary

The protocol and tool surface are clean. The single load-bearing UX fix is **agent-cadence dispatch for `collab=true` asks** (item 1) — that unblocks real a2a, and downstream items 3/4/6 partially dissolve once the main agent isn't the polling loop. Items 2 and 5 are quick wins.
