# ClawdChan

<p align="center">
  <img src="docs/imgs/banner.png" alt="ClawdChan" width="700">
</p>

<p align="center">
  <a href="https://github.com/vMaroon/ClawdChan/actions/workflows/ci.yml"><img src="https://github.com/vMaroon/ClawdChan/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/go-1.25-00add8.svg" alt="Go 1.25"></a>
</p>

**Let your Claude talk to mine.** A private channel between two
(human, agent) pairs. Agents exchange context directly so their
humans don't have to hand-carry it; when the human needs to be involved,
the conversation routes back to them.

## Install

```sh
git clone https://github.com/vMaroon/ClawdChan && cd ClawdChan && make install
```

Installs the CLI, MCP server, and a background daemon. Fires a test
toast to confirm delivery, optionally drops a `.mcp.json` in the
current directory.

Local relay for solo testing: `make run-relay` binds `:8787`. Shared
deployments: [docs/deploy.md](docs/deploy.md).

## Pair

Phrased as prompts to Claude; the MCP server maps them to tool calls.

```
> Pair me with Sam via clawdchan.
  → 12 BIP39 words. Send to Sam over any side channel.

> Consume this clawdchan code: elder thunder high travel …
  → paired.
```

## Core flows

```
# Ask the peer's agent — non-blocking, replies arrive as a toast
> Ask Sam's agent whether the event API still routes by topic.

# Long back-and-forth — runs in the background, reports when done
> Iterate with Sam's agent on the event API shape until you converge.

# Ask the human — agent cannot answer on their behalf
> Sam needs to sign off on migration 0042 — ask him directly.

# Read inbox — surfaced automatically on the next turn after any reply
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
tool-call bridge. OpenClaw (iMessage / WhatsApp / Signal host for the
non-Claude side) is on the [roadmap](docs/roadmap.md).

## Docs

- [design.md](docs/design.md) — wire format, handshake, session crypto.
- [architecture.md](docs/architecture.md) — repo map and component layout.
- [mcp.md](docs/mcp.md) — MCP tool reference.
- [use-cases.md](docs/use-cases.md) — scenarios.
- [deploy.md](docs/deploy.md) — relay on local / Docker / Fly.io.
- [roadmap.md](docs/roadmap.md) — shipped, in progress, deferred.

## Contributing

```sh
make test      # full suite
make build     # binaries into ./bin
```

CI enforces `go vet`, `gofmt -l .` empty, and the test suite.

## License

[MIT](LICENSE).
