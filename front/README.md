# Isoform front

The front lets cloud agents reach a person's computer over public HTTPS. It reads only the hostname in the TLS ClientHello and passes the connection, still encrypted, to that computer over iroh. TLS ends on the computer, so the front can't read requests, tokens or tasks. Design: `docs/design/mcp-public-front.md`.

- The hostname is `<label>.agents.isoform.ai`. The label is the computer's dedicated iroh endpoint id: its 32 bytes in lowercase RFC 4648 base32 without padding, 52 characters. The route is in the name, so the front keeps no table.
- The front dials the endpoint with ALPN `isoform/front/1` and replays the ClientHello it read. After that it copies bytes both ways and never looks at them.
- If a label is unknown or malformed, if the first record isn't a ClientHello, or if a limit is hit, the front closes the connection without dialing. It can't send an error inside TLS it doesn't hold.
- Limits live in memory only: new connections and open connections per label, bytes per minute per label, handshakes per source address (a /64 for IPv6), and a global cap on open connections.
- Logs are counts only, written once a minute. They never contain a label, an address or traffic.

## Run

```bash
ISOFORM_FRONT_LISTEN=0.0.0.0:443 ISOFORM_FRONT_KEY_FILE=/var/lib/isoform-front/front.key cargo run --release
```

At start it prints `front endpoint id: ...`. Computers accept connections only from the front ids they are given. `ISOFORM_FRONT_SUFFIX` changes the domain.

Behind a proxy that adds a PROXY protocol v2 header (Fly), set `ISOFORM_FRONT_PROXY_PROTOCOL=v2`. The front then reads the client's address from the header for its per-source limits, and closes any connection that arrives without one.

## Deploy

On Fly, with `fly.toml`, `Dockerfile` and `scripts/deploy.sh`. The steps and the DNS records are in `docs/front-deploy.md`.

## Toolchain

`rust-toolchain.toml` pins Rust 1.93.1 with clippy and rustfmt. The merge queue runs `cargo clippy --locked --all-targets -- -D warnings` and `cargo test --locked` here whenever a PR touches `front/`.

## Test

```bash
cargo test
```

`tests/splice.rs` runs a front and a stand-in computer over real iroh on loopback, with no relay and no internet, and checks that bytes round-trip. It also checks that wrong domains and plain HTTP are closed without a dial.

## The computer's side: `isoform-front-link`

```bash
isoform-front-link --key-file <path> --allow <front id>[,<front id>...] --forward 127.0.0.1:<tls port>
```

This runs on the person's computer. Its key is kept for this purpose only, separate from the phone link's, and stays in a 0600 file across restarts, because the key's id is the person's public hostname. When ready it prints one JSON line: `{"endpointId", "label", "hostname"}`.

It accepts connections on `isoform/front/1` only from the `--allow` front ids, and closes every other connection before forwarding a byte. Each stream is forwarded, still TLS, to the loopback TLS server named by `--forward`, which must be a loopback address. TLS ends there. The link reads nothing it forwards.

`tests/link.rs` runs a front, a link and a loopback server together over real iroh, and checks that a stranger endpoint is refused.
