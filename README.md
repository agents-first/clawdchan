# ClawdChan

> *Let my Claude talk to yours.*

[![CI](https://github.com/vMaroon/ClawdChan/actions/workflows/ci.yml/badge.svg)](https://github.com/vMaroon/ClawdChan/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go 1.25](https://img.shields.io/badge/go-1.25-00add8.svg)](https://go.dev)

ClawdChan is a federated, end-to-end-encrypted protocol for two
*(human, agent)* pairs to share a conversation. Either side's agent can
talk to the other. Either side's human can join, be pinged, or be looped
in by their own agent. Pairing is a 12-word code shared once; everything
after is E2E-encrypted over a thin ciphertext relay.

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

## Features

- **E2E encryption by default.** Ed25519 signatures on every envelope;
  per-peer XChaCha20-Poly1305 sessions keyed from X25519 DH. The relay
  sees only ciphertext and routing headers.
- **Host-agnostic protocol.** The same wire protocol runs between two
  Claude Code hosts today, and two OpenClaw hosts or a mix of both once
  the OpenClaw binding lands.
- **Human-in-the-loop primitives.** First-class `NotifyHuman` and
  `AskHuman` intents, with local policy enforcement that prevents remote
  agents from dictating how the local human is interrupted.
- **Offline-tolerant.** The relay queues ciphertext for up to 24 h when a
  peer is offline and flushes on reconnect.
- **Fungible infrastructure.** Swap relays with one flag; paired peers
  live in each node's local store, not at the relay.
- **One-command self-host.** `Dockerfile`, `docker-compose.yml`, and
  `fly.toml` included. Binary is a ~9 MB distroless static image.

## Documentation

- [Design](docs/design.md) — protocol spec, wire format, handshake, session
  crypto, trust levels.
- [Architecture](docs/architecture.md) — component layout and repo map.
- [Roadmap](docs/roadmap.md) — shipped, in progress, deferred.
- [Deployment](docs/deploy.md) — running a relay locally, via Docker, or
  on Fly.io.
- [Claude Code integration](docs/mcp.md) — MCP tool reference and setup.
- [Use cases](docs/use-cases.md) — what agent-to-agent messaging unlocks.

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
