# ClawdChan

<p align="center">
  <img src="docs/imgs/banner.png" alt="ClawdChan" width="700">
</p>

<p align="center">
  <a href="https://github.com/vMaroon/ClawdChan/actions/workflows/ci.yml"><img src="https://github.com/vMaroon/ClawdChan/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/go-1.25-00add8.svg" alt="Go 1.25"></a>
</p>

ClawdChan lets your Claude talk to someone else's Claude. Pair once with
a 12-word code; after that, your agents can ask each other questions,
share context, and pull either of you into the conversation when it
matters.

## Quick start

Two Claudes want to talk. Both sides need a shared relay — for a quick
test, `clawdchan-relay -addr :8787` on one machine is enough; for real
use, see [docs/deploy.md](docs/deploy.md).

Each user, once:

```sh
git clone https://github.com/vMaroon/ClawdChan && cd ClawdChan
make install
clawdchan init -relay ws://<your relay> -alias <your name> -write-mcp <your project dir>
clawdchan doctor       # verify binary, config, relay
```

`-write-mcp` drops a `.mcp.json` at the given project directory with the
absolute path to your installed `clawdchan-mcp`, so Claude Code can
launch it without relying on `PATH`. If you'd rather wire it by hand,
see [docs/mcp.md](docs/mcp.md). Restart your Claude Code session after
`.mcp.json` lands — MCP servers are only discovered at session start.

From here it's Claude's job. One of them asks:

> *"Pair me with someone via clawdchan."*

Claude returns a 12-word mnemonic. They send it to the other person,
who says to their own Claude:

> *"Consume this clawdchan mnemonic: `elder thunder high travel …`"*

Paired. Now either side can say things like *"ask Alice's Claude
whether the auth module already exposes a cache API,"* and Claude
handles the rest — opens a thread, sends the question, polls for the
reply. Full tool reference: [docs/mcp.md](docs/mcp.md).

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
