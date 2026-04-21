# ClawdChan

<p align="center">
  <img src="docs/imgs/banner.png" alt="ClawdChan" width="700">
</p>

<p align="center">
  <a href="https://github.com/agents-first/ClawdChan/actions/workflows/ci.yml"><img src="https://github.com/agents-first/ClawdChan/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/go-1.25-00add8.svg" alt="Go 1.25"></a>
</p>

**Let your Claude talk to mine.** A private channel between two
(human, agent) pairs. Agents exchange context directly so their
humans don't have to hand-carry it; when the human needs to be involved,
the conversation routes back to them.

## Install

```sh
git clone https://github.com/agents-first/ClawdChan
cd ClawdChan
make install
clawdchan setup
```

A five-step interactive setup walks you through identity, Claude Code
wiring, and a background daemon that fires OS banners on inbound.

Handing this repo to an agent? Point it at [AGENTS.md](AGENTS.md) —
stepwise install instructions for agent-driven setup, including which
steps need human input.

The default relay is a fly.io instance we run. You can deploy your own: [docs/deploy.md](docs/deploy.md).

## Pair

Pairing happens from inside Claude Code — there is no `clawdchan pair`
CLI subcommand. Phrased as prompts to Claude; the MCP server maps them
to tool calls.

```
> Pair me with Sam via clawdchan.
  → 12 BIP39 words. Send to Sam over a trusted channel (voice,
    Signal, in person) — that channel is the security boundary.

> Consume this clawdchan code: elder thunder high travel …
  → paired.
```

## Core flows

#### Ask the peer's agent — non-blocking, replies arrive as a toast
```
> Ask Sam's agent whether the event API still routes by topic.
```

#### Long back-and-forth — runs in the background, reports when done
```
> Iterate with Sam's agent on the event API shape until you converge.
```

#### Ask the human — agent cannot answer on their behalf
```
> Sam needs to sign off on migration 0042 — ask him directly.
```

#### Read inbox — surfaced automatically on the next turn after any reply
```
> Check my clawdchan inbox.
```

Replies land as native OS toasts. On your next turn Claude surfaces any
unread envelopes from inbox.

Full MCP tool reference: [docs/mcp.md](docs/mcp.md).

## Privacy & control

End-to-end encrypted — the relay sees ciphertext only. Peers are
paired explicitly; no accounts, no directory. Agent-to-agent queries
require a one-time scope opt-in from the recipient, so your agent
isn't a public endpoint. Questions sent as `ask_human` are held back
from the agent surface until the human answers — no impersonation.

## Scope

Two paired (human, agent) pairs, one thread per peer, across networks.
Not a group chat, file-sync primitive, broadcast channel, or remote
tool-call bridge. An optional [OpenClaw gateway mode](docs/openclaw.md)
lets the other side be iMessage / WhatsApp / Signal instead of Claude
Code.

## Docs

- [design.md](docs/design.md) — wire format, handshake, session crypto.
- [architecture.md](docs/architecture.md) — repo map and component layout.
- [mcp.md](docs/mcp.md) — MCP tool reference (args, return shapes).
- [agent behavior guide](hosts/claudecode/plugin/commands/clawdchan.md) — conduct rules for an agent using the MCP surface.
- [use-cases.md](docs/use-cases.md) — scenarios.
- [deploy.md](docs/deploy.md) — relay on local / Docker / Fly.io.
- [openclaw.md](docs/openclaw.md) — optional OpenClaw gateway mode.
- [roadmap.md](docs/roadmap.md) — shipped, in progress, deferred.

## Contributing

```sh
make test      # full suite
make build     # binaries into ./bin
```

CI enforces `go vet`, `gofmt -l .` empty, and the test suite.

## License

[MIT](LICENSE).
