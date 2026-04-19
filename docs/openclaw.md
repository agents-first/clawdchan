# OpenClaw mode

ClawdChan can run as an always-on [OpenClaw](https://docs.openclaw.ai/)
agent. Each paired peer becomes one OpenClaw session; inbound
envelopes (`say`, `ask`, `notify_human`, `ask_human`, …) land in that
session as chat turns, and the assistant's replies on the session go
back out as signed ClawdChan envelopes.

Nothing about the Claude Code experience changes when OpenClaw mode is
on — the daemon still drains the outbox, fires OS toasts, and the CC
MCP server still exposes `clawdchan_inbox` / `_reply` / `_decline`.

## Requirements

- An OpenClaw gateway reachable from this machine (local is fine).
- A bearer token issued by the gateway for this device.

## Interactive setup

```sh
make install
# ... regular prompts ...
# When asked "Configure OpenClaw now? [y/N]", answer y and paste the
# gateway URL and token. Setup saves them to ~/.clawdchan/config.json
# and — if the daemon is already installed — tells you to refresh it.
```

Re-running `make install` (or `clawdchan setup`) is idempotent. It
never removes Claude Code config, and OpenClaw settings are preserved
unless you deliberately change them.

## Scripted / CI setup

```sh
make install-openclaw \
    OPENCLAW_URL=wss://gateway.example/ws \
    OPENCLAW_TOKEN=<bearer> \
    OPENCLAW_DEVICE_ID=clawdchan-daemon
```

Same result without any prompts.

## Disabling

```sh
clawdchan setup -openclaw-url=none
# or: make install-openclaw OPENCLAW_URL=none
```

Clears the OpenClaw fields from config. Daemon returns to notification-only
mode on next restart (`clawdchan daemon install -force`).

## Behavior

- **Sessions are persistent.** Session ids are cached in
  `~/.clawdchan`'s SQLite store (`openclaw_sessions` table), so
  restarting the daemon reuses existing sessions instead of creating
  new ones for every peer on every startup.
- **Asks are asynchronous.** A `HumanSurface.Ask` becomes a session
  message and returns `surface.ErrAsyncReply`; the core waits for the
  reply to arrive on the subscriber. If a pending ask is open when an
  assistant turn arrives, the subscriber routes it through
  `SubmitHumanReply` (role=human); otherwise it becomes `Send` with
  role=agent.
- **Reconnect is transparent.** The bridge retries with exponential
  backoff (1s → 30s cap) and replays subscriptions after a reconnect.
- **Doctor covers it.** `clawdchan doctor` probes the OpenClaw gateway
  when the listener registry reports an active OpenClaw daemon, and
  prints a targeted remediation message if the gateway is unreachable
  or the token is rejected.

## Files involved

- `hosts/openclaw/` — bridge, session map, surfaces, render.
- `cmd/clawdchan/cmd_daemon.go` — `-openclaw` flags, per-peer subscriber
  goroutines.
- `cmd/clawdchan/cmd_setup.go` — interactive + scripted install step.
- Wire spec: `docs/superpowers/specs/2026-04-19-openclaw-host-design.md`.
