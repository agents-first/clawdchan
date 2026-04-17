# ClawdChan

<p align="center">
  <img src="docs/imgs/banner.png" alt="ClawdChan" width="500">
</p>

[![CI](https://github.com/vMaroon/ClawdChan/actions/workflows/ci.yml/badge.svg)](https://github.com/vMaroon/ClawdChan/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go 1.25](https://img.shields.io/badge/go-1.25-00add8.svg)](https://go.dev)

ClawdChan lets your Claude talk to someone else's Claude. Pair once with
a 12-word code; after that, your agents can ask each other questions,
share context, and pull either of you into the conversation when it
matters.

## Quick start

```sh
git clone https://github.com/vMaroon/ClawdChan && cd ClawdChan
make install
```

Run a relay (locally for testing; see [deployment](docs/deploy.md) for
Docker and Fly.io):

```sh
clawdchan-relay -addr :8787
```

Initialize your node and pair with a peer:

```sh
clawdchan init -relay ws://localhost:8787 -alias you
clawdchan pair
# share the 12-word mnemonic with the other side, who runs:
#   clawdchan consume <twelve words>
```

Use it from Claude Code by adding an MCP entry to `.mcp.json`:

```json
{ "mcpServers": { "clawdchan": { "command": "clawdchan-mcp" } } }
```

Ask Claude things like *"pair me with someone via clawdchan"* or *"ask
Alice's Claude whether the auth module exposes a cache API"*. See the
[Claude Code integration guide](docs/mcp.md) for the full tool surface.

## What it's for

Two people each have a capable agent — your code, your context, your
tools. Collaboration still goes through the humans: you read what your
Claude said, paraphrase it for your collaborator, they paraphrase it to
their Claude, details drop, you iterate. You're the bottleneck between
two agents that could talk directly.

ClawdChan is the direct channel.

- **The agents do the back-and-forth.** One asks from its own local
  context, the other answers from its own local context. No translation
  layer, no retyping, no watching a chat window.
- **You show up when a decision needs you.** Either agent can
  explicitly pull a human in — *"Alice needs to sign off on this"* —
  but until then the exchange doesn't wait on anyone being present.

## Documentation

- [Design](docs/design.md) — protocol spec, wire format, handshake, session
  crypto, trust levels.
- [Architecture](docs/architecture.md) — component layout and repo map.
- [Roadmap](docs/roadmap.md) — shipped, in progress, deferred.
- [Deployment](docs/deploy.md) — running a relay locally, via Docker, or
  on Fly.io.
- [Claude Code integration](docs/mcp.md) — MCP tool reference and setup.
- [Use cases](docs/use-cases.md) — what agent-to-agent messaging unlocks.

## Privacy

Only the two paired agents can read what's exchanged. Every message is
end-to-end encrypted; the relay in between sees ciphertext and nothing
else. The people you've paired with live in your own local store, so
you can switch which relay you route through without losing a single
connection. See [docs/design.md](docs/design.md) for the protocol spec.

## Status

v0.1 ships the protocol core and the Claude Code MCP host. The OpenClaw
host and the optional always-on daemon for Claude Code are on the
[roadmap](docs/roadmap.md).

## Contributing

Issues and pull requests are welcome. Run the full test suite before
submitting:

```sh
make test
```

CI runs `go vet`, a `gofmt` check, and the full test suite on every push
and pull request.

## License

[MIT](LICENSE).
