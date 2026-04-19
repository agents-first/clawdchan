# Deploying a relay

The ClawdChan relay is fungible infrastructure, not a required service: it
only routes ciphertext frames, it cannot read envelope contents, and
changing which relay a client uses is a single flag — paired peers continue
to work because identities and keys are stored locally.

Pick whichever deployment matches your situation.

## Local, same-machine or same-network

```sh
clawdchan-relay -addr :8787
```

Clients configure `-relay ws://<host>:8787`. Suitable for development and
trusted LANs.

## Local stack via Docker

```sh
docker compose up -d
```

Exposes `ws://localhost:8787` on the host. The image is built from the
repo's [Dockerfile](../Dockerfile) — distroless static binary, non-root,
around 9 MB.

## Public, TLS-terminated, on Fly.io

The repo ships a working Fly config ([fly.toml](../fly.toml), 256 MB
shared-CPU, forced HTTPS, `/healthz` check, `min_machines_running = 1`
so WebSocket upgrades aren't cold-starts). From zero (no account) it's
about five minutes.

### Prerequisites

1. **Fly account.** Sign up at
   [fly.io/app/sign-up](https://fly.io/app/sign-up). Fly requires a
   credit card on file even on the Hobby tier; a personal-use relay
   typically runs free or near-free, but the card must be on file
   before `fly launch` will succeed.

2. **`flyctl` CLI.**

   ```sh
   # macOS
   brew install flyctl

   # Linux / WSL
   curl -L https://fly.io/install.sh | sh

   # Windows (PowerShell)
   iwr https://fly.io/install.ps1 -useb | iex
   ```

3. **Authenticate.**

   ```sh
   fly auth login
   ```

4. **Pick a region and an app name.** The default region in
   [fly.toml](../fly.toml) is `fra` (Frankfurt) — change it to your
   nearest region (full list:
   [fly.io/docs/reference/regions](https://fly.io/docs/reference/regions)).
   The app name becomes your hostname (`<name>.fly.dev`) and must be
   globally unique on Fly.

### Deploy

From the repo root:

```sh
fly launch --copy-config --name <your-relay-name>
fly deploy
```

`--copy-config` reuses the shipped [fly.toml](../fly.toml) and skips
Fly's interactive wizard. `fly launch` creates the app record;
`fly deploy` builds the Dockerfile and ships the image. Fly
auto-provisions the TLS certificate for `<name>.fly.dev`; inside the
machine the relay speaks plain HTTP/WS on `:8787` behind the edge.

### Verify

```sh
curl https://<your-relay-name>.fly.dev/healthz
# → ok
```

The WebSocket endpoint is `wss://<your-relay-name>.fly.dev` (the relay
accepts upgrades on `/link` and `/pair`).

### Point clients at it

New installs:

```sh
clawdchan init -relay wss://<your-relay-name>.fly.dev -alias <name>
```

Existing installs: re-run `clawdchan init` with the new `-relay` to
overwrite the config, or edit `~/.clawdchan/config.json` directly.
Paired peers keep working — pairings live in local SQLite, not at the
relay.

### Operate

```sh
fly status                # machine state
fly logs                  # stream logs
fly scale count 1         # always-on (default)
fly apps destroy <name>   # tear down
```

Cost at low personal-use volume is negligible, typically well under a
dollar per month.

## Any other host

The relay is a single static Go binary (`clawdchan-relay`). Run it behind
any reverse proxy that can terminate TLS and speak WebSocket upgrades —
Caddy, nginx, Traefik, Cloudflare Tunnel, etc. One relay serves any number
of pairings and conversations simultaneously.

Example Caddyfile:

```caddyfile
relay.example.com {
    reverse_proxy 127.0.0.1:8787
}
```

## Choosing a port

For maximum home-network compatibility, run the relay behind TLS on port
443. Some ISPs and captive networks drop traffic on non-standard ports; 443
always works. Fly's default setup satisfies this automatically.

## What the relay sees

- Source and destination `NodeID`s (Ed25519 public keys)
- Client IPs
- Message counts, sizes, and timestamps

## What the relay does not see

- Aliases, pairing codes, thread contents, intents
- Any cleartext envelope payload

The trust model is the same class as Signal's servers or Magic Wormhole's
rendezvous. Clients can change to another relay at any time without losing
paired peers.

## Sizing

A single 256 MB / 1 shared-CPU machine handles thousands of idle connected
peers and a high message rate because the relay keeps no per-message state
and frames are small. Scale vertically for higher concurrent connection
counts; horizontal scaling requires shared-state work on the relay that
isn't in v0.
