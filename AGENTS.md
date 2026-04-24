# Agent install guide

If a user hands you this repo and asks you to set ClawdChan up for
them, follow this exactly. The setup is interactive in two places
and you **must** pause there — do not bypass prompts or run with
`-y` unless the user explicitly asked for unattended install.

## Prereqs

- Go 1.25 on PATH. Check with `go version`. If absent, stop and ask
  the user to install it — do not try to install it yourself.
- `$(go env GOPATH)/bin` on the user's shell PATH. Step 3 of
  `clawdchan setup` handles this, but only if they confirm it.

## Steps

1. **Build and install binaries.**

   ```sh
   make install
   ```

   This puts `clawdchan`, `clawdchan-relay`, and `clawdchan-mcp` into
   `$(go env GOPATH)/bin`. The `clawdchan-mcp` binary must be
   discoverable on the user's PATH for MCP hosts to launch it.

2. **Run interactive setup.**

   ```sh
   clawdchan setup
   ```

   Setup is a 5-step interactive flow: identity, agent wiring, PATH,
   OpenClaw gateway, background daemon. **Tell the user to walk through
   it themselves** — you should not pipe input into it.

   Up front the user picks which MCP hosts to wire: Claude Code,
   Gemini CLI, Codex CLI, GitHub Copilot CLI, Cursor, or none.
   OpenClaw is a separate yes/no step later in setup. If Claude Code is
   selected, there are two scope decisions that only the user can make:

   - **MCP server registration:** user-wide (recommended) writes
     `~/.claude.json` via `claude mcp add -s user`; project writes
     `.mcp.json` in the current directory.
   - **`clawdchan_*` permission allow-rule:** user-wide writes
     `~/.claude/settings.json`; project writes `.claude/settings.json`
     (checked in, team-wide); project-local writes
     `.claude/settings.local.json` (personal, gitignored). Skipping
     means every MCP call prompts per-tool, which blocks live-collab
     sub-agents.

   In scripted / non-interactive runs (`-y` or no TTY), setup
   defaults `-cc-mcp-scope` and `-cc-perm-scope` to `user` — the same
   choices the interactive flow recommends, and the only ones that
   produce a working install without further action. Pass `=skip` to
   opt out explicitly, or `=project` / `=project-local` to change the
   destination.

3. **Restart any selected MCP host** so its updated config loads. The
   user does this — quit and reopen.

4. **Verify.** Ask the user to run `clawdchan whoami` in a terminal.
   Expect a node_id and alias. If that works, ClawdChan is installed.

## What you should not do

- Do not run `make install` as root or with sudo. The binaries go to
  the user's `$GOPATH/bin`, not `/usr/local/bin`.
- Do not invent a relay URL. The default relay is a fly.io instance
  the project runs; `clawdchan init` defaults to it. If the user
  wants their own relay, point them at `docs/deploy.md`.
- Do not generate a pairing mnemonic during install — pairing is a
  runtime action the user drives from their chosen host after install
  completes. The MCP tool descriptions and host docs cover how that
  works.
- Do not run `clawdchan daemon run` directly as a test — if the user
  accepts step 5's daemon install, the service auto-starts at login
  and runs in the background. Running it manually holds the relay
  link in your terminal and blocks the installed service.

## If something fails

- `make install` failure: read the error; usually missing Go toolchain
  or a module network error. Ask the user, don't retry blindly.
- `clawdchan setup` step 2 (agent wiring): safe to skip; user can rerun
  `clawdchan setup` later. For Claude Code specifically, the user can
  also run `clawdchan init -write-mcp <project-dir>` to write a project
  `.mcp.json`.
- `clawdchan setup` step 5 (daemon): on macOS, the user may need to
  `brew install terminal-notifier` for notifications to reliably
  appear (the fallback `osascript` path is fragile). Mention this
  once; don't install it without their say-so.

## After install

Read `README.md` for the end-user flows. Read
`hosts/claudecode/plugin/commands/clawdchan.md` — it's the operator
manual for acting as a ClawdChan agent once the MCP tools are
available. You become the Claude Code side of a paired (human, agent)
pair; conduct rules matter.
