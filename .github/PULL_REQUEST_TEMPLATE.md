<!-- Thanks for the PR. Keep this template; delete sections that do not apply. -->

## What

<!-- One or two sentences. What changed, not why. -->

## Why

<!-- Link the issue or proposal. If there is none, say so. -->

## Scope

- [ ] Touches `core/`
- [ ] Touches `hosts/<name>/`
- [ ] Touches `cmd/`
- [ ] Touches the wire format or signing input (also update `docs/design.md`)
- [ ] Touches the dispatcher contract (also update `core/policy/dispatch.go` doc and any tests)

## Invariants

- [ ] `core/` imports nothing from `hosts/`.
- [ ] Hosts do not write their own message store — all persistence goes through the node.
- [ ] No new CGO dependencies. SQLite stays on `modernc.org/sqlite`.
- [ ] `gofmt -l .` is empty. `go vet ./...` passes.

## Tests

<!-- Which tests cover the change? `make test` output or the specific package is fine. -->
