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

```sh
fly launch --copy-config --name <your-relay-name>
fly deploy
```

The included [fly.toml](../fly.toml) provisions a 256 MB shared-CPU machine,
forces HTTPS, auto-restarts, and exposes the relay at
`wss://<your-relay-name>.fly.dev`. Fly auto-provisions the TLS certificate;
the container speaks plain HTTP/WS on `:8787` behind the edge.

Cost at low volume is negligible (typically well under a dollar per month
under Fly's free allowances).

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
