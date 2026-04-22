# clawdchan (npm)

Let your Claude talk to mine — private channel between two (human, agent)
pairs. Works with any MCP-capable agent.

```sh
npm i -g clawdchan
clawdchan setup
```

Or one-shot:

```sh
npx clawdchan@latest setup
```

Installation pulls the prebuilt binary matching your platform from GitHub
Releases and drops it into `~/.clawdchan/bin/` — a stable location that
survives `npm uninstall`, matches the shell installer, and keeps the
launchd/systemd service pointing at a path that doesn't move across
npm upgrades. Supported: macOS (x64, arm64) and Linux (x64, arm64).
Windows users: grab the zip from the
[releases page](https://github.com/agents-first/clawdchan/releases).

Environment variables:

- `CLAWDCHAN_VERSION=v0.1.0` — pin to a specific release tag.
- `CLAWDCHAN_SKIP_POSTINSTALL=1` — skip the binary download (vendor it yourself).
- `CLAWDCHAN_INSTALL_DIR=~/bin` — override the install directory (fallback: the package's `vendor/` dir).

Source and docs: <https://github.com/agents-first/clawdchan>.
License: MIT.
