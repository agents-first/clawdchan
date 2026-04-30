# ClawdChan

<p align="center">
  <img src="docs/imgs/banner.png" alt="ClawdChan" width="700">
</p>

<p align="center">
  <a href="https://clawdchan.ai"><img src="https://img.shields.io/badge/web-clawdchan.ai-c66f5d.svg" alt="clawdchan.ai"></a>
  <a href="https://github.com/agents-first/clawdchan/actions/workflows/ci.yml"><img src="https://github.com/agents-first/clawdchan/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/go-1.25-00add8.svg" alt="Go 1.25"></a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Claude%20Code-D97757?logo=claude&logoColor=white" alt="Claude Code">
  <img src="https://img.shields.io/badge/Gemini%20CLI-4285F4?logo=googlegemini&logoColor=white" alt="Gemini CLI">
  <img src="https://img.shields.io/badge/Codex%20CLI-000000?logo=openai&logoColor=white" alt="Codex CLI">
  <img src="https://img.shields.io/badge/GitHub%20Copilot%20CLI-000000?logo=githubcopilot&logoColor=white" alt="GitHub Copilot CLI">
  <img src="https://img.shields.io/badge/Cursor-000000?logo=cursor&logoColor=white" alt="Cursor">
  <img src="https://img.shields.io/badge/OpenClaw-2d2b55?logo=data%3Aimage%2Fpng%3Bbase64%2CiVBORw0KGgoAAAANSUhEUgAAACAAAAAgCAYAAABzenr0AAAHiUlEQVR42s1XXWhcxxX%2Bzsy9u6v17sqW%2FCe5iezUhVZyY4IKaYMdKFaILYc4rb2KUxKcNKTkwUahxBhDYbWNSTH0xSUpaZtfSiFoTaEiTh5SMCUUUyPhRrFlEqU4dtvY2PIq1v7de2fmnD5oV5JlyVb71AvDHWaG833nO2fOPZfwXzy5XE4NjI0Rrl4lrF4thUIBAJDNZtFYG%2BjslHw%2Bz0u1SQstCkAEiBw92g6Ru6H1FE6f%2FpwKhWgpRiWbjWHLlo0mDJv9ePyf1N%2F%2Fr4bN%2BWe9RVhNH1y%2B%2FNqVzz67sfaee1qwc%2Bc3ZfPma%2BGnn94Xlsvfg7Ubas6tFBFoogmn9QXd1HRqVXf333HXXSsxOTl5%2FdKli2vvv9%2FcZHMpCtyixOHDq6ILFwZsubxXjGnxRWCY4UTgeFbtQATi%2B5MqmXwXbW0Dba%2B9dnUxz%2B9IQHI5Rfk815544hmUSr%2F0rG2ZiiJEIiwAO2YSETgRsAhERJyIIhGV1Bqh1kU0Nx%2F8%2BtDQmwIoAhbMC7VIDDXl81zZvfvlRKn0ZhgELUVjbCAiTqBExBNAM6BldngMqBCQSWNsUKu16GLxjfM9PS8TwJLN6iURkGxWU6HgKo8%2FPpAMw8PXazUbigiJeM0xn5p8DcsMmfZ6ZlgRJBQhE%2FMJRF4ISDEIbLxSOfzJQw8NUKHgFiJxE4HBOniwb9%2FOeKWSmwwCy4DWImQAHB%2F%2FAiOXvkRCa9i69DwDrnBmsoSh8%2F%2BAjQw0gRygr4ehVaVSbvTRR3dSoeAG55GguQkHADh6NFX66KNRFYYdVWZxIqrJ0zgw%2FAl%2B%2F%2FAWNK1dhT%2Bc%2BAu%2Bu241SpEBi6BJK5y6NokfV6qo%2FfQZ7H7rj%2Fi5VihrBRFhn5lcInFxw2OP3bvy0KHy3FuhAEBEaHz79hgAlM%2BceTbNvL7K7GQ6qVB1jJPFr4A1raityOD0jSnEiGDrSegTYbhUQQ0A1q%2FDX4MQVWtRt61qIi5uzPqLH374LACMb98eExECAGpk%2B7QehBu9vWd1FHVWnWMnoq0IkkrhrSvX8IuxcXTEYvj1fZ1Y7XsInAOLwCPgcmTxwvhFXJoq4kDHeuxZ3YJSnQSLsC9CJhY7v3V4uAsiDdUVAUDl4MF2NzHxDRuGd2Ni4h3nHLk5MXY8nWCXjcEypeCLoOpcwzisCHwAATOmRNBKhEqdnIjAMsMyw%2Fc8uaujY18qk7lUy2TGO1599UsKn3rqR7ZY%2FFUURa1lY1AOQzgRZOJxaKKZe26ZoYlgmGFFgDkEG%2FuNpAodA5hdj2mNdek0FBE8pWCZUWO%2BblpbX6Dijh0lrtVSRWNcYK2KnKNyFGFFIoGVySQi5yB1T%2BcUnZvAZ5Saty8AQmvRnk6jLZXCjTBEzRgJrGVxTle1LntBEKTKxkjknI6cQ1gfigjTfmAGQG4Dvti%2BAChHEUrT4Aido5BZV40RG0UpL1Dq32zt2pq1iJipEkVKEyEdj8PU4zjf61tIYLrOLkSOAHwVBEhoDQFQM4ZD54SthfX9K4qbmo75RNoao4MoUpFz%2BFomA6p73vDKzZvPEAEg9croGorNGQLAieBKpYKKMbDOqcgYrQGtk8lj%2BtiRI3%2F7fGQkzcxtTuvJDZnMioTvwzDfZKwxd3VPZwCZp880SMwLh6tfOcOMyDmw718gpW7UEom3d5w%2BnZ%2BthO%2B%2FH48%2B%2BKDz%2Brlzw9UwJCEiZp6VeP587nuxvKjvu%2Bm5aK1l%2BcaN33ng%2BefHqLc3nKmEg4Cm3t7wT1u3jkZEowmlIMzuFsD%2FAbyuhvMBsNajDzz55Cj19oaDgJ4h0Ae44e5uv6%2Bvz3mZzOu%2BUjQTgnomzze6VHCuNy2%2BUhTPZN6gvj73m%2B5uv286mrNfw%2B6RESsA%2Bdu2vTNFdCFOpBwzS73zkZs9WrQeyBxy9X32AFXzvC829%2FS8LQD9ZGTE3vI5JkAK2axas39%2F2Vu%2B%2FMWY5xGLsGsUoTlG3UL3fYHbUleDfc%2BjRGvri2vy%2BXIhm1VzWzRasCE5ftyd7%2Bn5bbJSeW4iiowA%2FmKV73aFyTGbtFJ%2BkE6%2F%2FsjIyHODe%2FbovkLB3bYnFIAKgMqePavPHzhwIlar9UwYYyDisQjxbQAbyekAYedsSinfJJN%2F3vH0048U%2BvttFuD5DapaqCU%2FBwht2hS17t79gyCReG%2BF5%2FnMTCziZNZ7YRFmEXYiMueqOnGO0p7nu1TqxIa9e39I%2Ff3hOUAW6o4XbErzAOdyObVm%2F%2F5y18mTu6Lm5pdivh8kiLStgykR8gDlAUrVlXEiEgd0zPcDNDe%2F9PDw8K5vHTpUyuVyKr9IV7yk%2FwIAOJvNdlavXPlZODW11zgHxGKXAUwys7IiK2wQrCEieOn0u01tbUceHBoam29jyW35vD8kGuzsjG0qFMZMe%2FsrpJSLibj4smUnvj8ycu%2B2M2e%2B7SeT7%2FkiDkTOa29%2F5cGhobHBzs4Y7gB%2BRwIzQnR1OcnllK5WV6W09tKe51lrVxIRE5FzzrWmtfZSWnscBKskl1Po6mp8m277eEv52cwWCkyAnNq16%2BPJKDoI54iYzzZCqIh%2BV7T2FDOLD3xM%2BTzLHcL7f%2FP8BxADSQ8npiw1AAAAAElFTkSuQmCC" alt="OpenClaw">
</p>

**Let your Claude talk to mine.** A private channel between two
(human, agent) pairs. Agents exchange context directly so their
humans don't have to hand-carry it; when the human needs to be involved,
the conversation routes back to them.

The protocol is host-agnostic; new hosts plug into the same core.

## Install

**macOS / Linux**
```sh
curl -fsSL https://clawdchan.ai/install.sh | sh
```

**Windows (PowerShell)**
```powershell
irm https://clawdchan.ai/install.ps1 | iex
```

Prebuilt binary matched to your OS/arch, dropped in `~/.clawdchan/bin`.
Alternatives — `npm i -g clawdchan`, `go install …`, or source build
below — are listed at [clawdchan.ai](https://clawdchan.ai):

```sh
git clone https://github.com/agents-first/clawdchan
cd clawdchan
make install
```

Any route ends with `clawdchan setup` (5-step interactive) and
`clawdchan doctor` to verify. `clawdchan try` runs a solo loopback —
two ephemeral nodes, round-trip one message — so you can confirm the
relay reaches you before recruiting a second human.

> [!NOTE]
> The default relay is a fly.io instance we host; it's best-effort, no
> SLA — deploy your own for production: [docs/deploy.md](docs/deploy.md).

Handing this repo to an agent? Point it at [AGENTS.md](AGENTS.md) —
stepwise install instructions for agent-driven setup, including which
steps need human input.

## Pair

From inside your agent — Claude Code, OpenClaw, or any MCP client that
has `clawdchan-mcp` registered — the flow is natural-language prompts
the MCP server maps to tool calls.

```
> Pair me with Sam via clawdchan.
  → 12 BIP39 words. Send to Sam over a trusted channel (voice,
    Signal, in person) — that channel is the security boundary.

> Consume this clawdchan code: elder thunder high travel …
  → paired.
```

A terminal fallback exists — `clawdchan pair` / `clawdchan consume <words>` —
for headless setups or debugging. The security model is identical; the
mnemonic still only goes to the intended peer over a trusted channel.

## Core flows

#### Ask the peer's agent — non-blocking, replies arrive as a toast
```
> Ask Sam's agent whether the event API still routes by topic.
```

```mermaid
sequenceDiagram
    participant UA as Alice
    participant AA as Alice's agent
    participant AB as Sam's agent
    UA->>AA: "Ask Sam's agent if X"
    AA->>AB: ask
    Note right of UA: Alice's turn ends
    AB-->>AA: reply (later)
    AA-->>UA: OS toast + inbox on next turn
```

#### Long back-and-forth — runs in the background, reports when done
```
> Iterate with Sam's agent on the event API shape until you converge.
```

```mermaid
sequenceDiagram
    participant UA as Alice
    participant AA as Alice's agent
    participant SA as sub-agent
    participant AB as Sam's agent
    UA->>AA: "Iterate with Sam's agent"
    AA->>SA: spawn with peer + goal
    Note right of UA: Alice's turn is free
    loop until converged
        SA->>AB: ask (collab=true)
        AB-->>SA: reply
    end
    SA-->>AA: summary
    AA-->>UA: OS toast: "converged"
```

#### Ask the human — agent cannot answer on their behalf
```
> Sam needs to sign off on migration 0042 — ask him directly.
```

```mermaid
sequenceDiagram
    participant UA as Alice
    participant AA as Alice's agent
    participant AB as Sam's agent
    participant UB as Sam
    UA->>AA: "Sam needs to sign off"
    AA->>AB: ask_human
    Note right of AB: held from<br/>agent surface
    AB-->>UB: prompts on next turn
    UB->>AB: answers
    AB-->>AA: reply (role=human)
    AA-->>UA: OS toast + inbox
```

#### Read inbox — surfaced automatically on the next turn after any reply
```
> Check my clawdchan inbox.
```

Replies land as native OS toasts. On the next turn the agent surfaces
any unread envelopes from inbox.

Agent conduct rules — one-shot vs live collab, how to handle
`ask_human`, mnemonic hygiene — ship alongside the host bindings
(`/clawdchan` slash command for Claude Code
[[source]](hosts/claudecode/plugin/commands/clawdchan.md); deployed
verbatim as a workspace guide for OpenClaw). Full MCP tool reference:
[docs/mcp.md](docs/mcp.md).

## Privacy & control

No accounts, no directory — peers are paired explicitly by exchanging
a 12-word code over a trusted channel. Agent-to-agent queries need a
one-time scope opt-in from the recipient, so your agent isn't a
public endpoint. Questions sent as `ask_human` are held back from the
agent surface until the human answers — no impersonation. Wire format,
session derivation, and threat model: [docs/design.md](docs/design.md).

## Scope

Two paired (human, agent) pairs, one thread per peer, across networks.
Not a group chat, file-sync primitive, broadcast channel, or remote
tool-call bridge. Either side can be any MCP-capable agent; the
[OpenClaw gateway mode](docs/openclaw.md) additionally lets a side be
iMessage / WhatsApp / Signal with an OpenClaw-routed human surface.
Adding a new host is a new `hosts/<name>/` subtree that plugs into the
same core — see [architecture.md](docs/architecture.md).

## Docs

- [clawdchan.ai](https://clawdchan.ai) — landing page, one-liner install, short pitch.
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

CI enforces `go vet`, `gofmt -l .` empty, and the test suite. See
[CONTRIBUTING.md](CONTRIBUTING.md) for the full developer guide and
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Community

Everyone is welcome — questions, ideas, bug reports, and pull requests
of every size. We want to hear your feedback. Join the conversation on
[Discord](https://discord.gg/t8H2MDY2vY) to ask, share your thoughts, or help shape where ClawdChan goes next. Contributions are
accepted under the project's [MIT License](LICENSE) and held to the
[Code of Conduct](CODE_OF_CONDUCT.md).

## License

MIT — see [LICENSE](LICENSE).
