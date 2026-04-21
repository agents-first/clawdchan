# Contributing to ClawdChan

Thanks for considering a contribution. This file covers what ClawdChan is, the
one architectural invariant you need to know before touching code, the four
lanes most contributions fall into, and the local dev loop.

If you are looking for the deep design, read
[docs/design.md](docs/design.md) (wire format + crypto) and
[docs/architecture.md](docs/architecture.md) (package layout).

## What ClawdChan is

An end-to-end-encrypted protocol that lets two (human, agent) pairs share a
conversation. Two design invariants shape the entire tree:

1. **Core is host-agnostic.** `core/` must not import anything from `hosts/`.
   Host bindings depend on core; never the reverse. Adding a new agent tool
   (Cursor, Aider, ...) is a new `hosts/<name>/` subtree, not a modification
   to core.
2. **The node is the trust boundary.** One Ed25519+X25519 identity per node,
   shared between the human and agent principals on that node. Local policy —
   not the remote peer — decides whether to honor an `AskHuman` or
   `NotifyHuman` intent.

Most contribution friction comes from accidentally crossing (1). If your
change needs a host-specific type in `core/`, the change is almost always
wrong — ask first.

## Four contribution lanes

The issue templates map to these. If your change doesn't fit any of them,
open a discussion first.

### Host binding (`hosts/<name>/`)

Add support for a new agent tool, or extend an existing host. This is the
highest-leverage and lowest-risk contribution — it is almost entirely new
code that plugs into two stable interfaces (`HumanSurface`, `AgentSurface`).
See [docs/hosts.md](docs/hosts.md) for the reference walk-through using
`hosts/claudecode/`.

### Policy plugin (`core/policy/` + `cmd/clawdchan/daemon_*`)

Inbound gates, agent dispatchers, and daemon notification policy. The
dispatcher subprocess contract in [core/policy/dispatch.go](core/policy/dispatch.go)
is the stable wire — new dispatchers are new subprocesses.

### Intent / collab-pattern proposal (`docs/` + optionally `core/envelope/`)

Agent-to-agent conventions: structured collab_sync payloads, summarization
before handoff, citation shape, turn-taking. Most of these have **zero wire
impact** — they are conventions over existing envelope fields. Use the intent
template; if the proposal does change the envelope or a signing input, it
becomes a spec change and must also update `docs/design.md`.

### Bug fix

Anything that makes current behavior match the spec. Include a repro. For
anything in crypto / pairing / envelope signing, use the private security
advisory flow rather than a public issue.

## Dev loop

Go 1.25, pure-Go dependency set. No CGO — SQLite is `modernc.org/sqlite` by
deliberate choice.

```sh
make build      # builds all three binaries into ./bin
make test       # runs the full suite with a 120s timeout
make tidy       # go mod tidy
make run-relay  # local relay on :8787 for two-node integration testing
```

Run a single package's tests: `go test ./core/envelope/...`.
Run a single test: `go test ./core/envelope -run TestName`.

Before pushing, make sure the CI gates are green locally:

```sh
gofmt -l .                # must be empty
go vet ./...              # must pass
staticcheck ./...         # must pass (version pinned in the Makefile)
go test ./... -count=1    # -count=1 defeats the test cache
```

A single unformatted file will fail CI. `gofmt -w .` first.

## Commit and PR style

- Prefix commit messages with the component: `core:`, `host/claudecode:`,
  `policy:`, `cmd/clawdchan:`, `docs:`, `ci:`.
- One logical change per PR. A new host binding can be a large PR; a policy
  tweak should be a small one.
- Update tests in the same PR as the code. The core packages have close to
  1:1 test coverage; match that density when adding to them.
- If the change is user-facing (CLI flag, MCP tool, envelope field), update
  `README.md` or `docs/mcp.md` in the same PR.

## Label scheme

Maintainers use these. You do not need to apply them yourself.

- `bug`, `enhancement`
- `host:claudecode`, `host:openclaw`, `host:new`
- `policy`, `spec`, `intent`
- `ci`, `docs`
- `good-first-issue` — curated; small host ports and policy examples are
  typical candidates.

## Security

If you find a vulnerability — anything that could forge an envelope, leak a
session key, hijack a pairing, or bypass the policy gate — report it
privately through [GitHub Security Advisories](https://github.com/vMaroon/ClawdChan/security/advisories/new).
Do not open a public issue.

## Code of conduct

This project follows the [Contributor Covenant 2.1](CODE_OF_CONDUCT.md).
