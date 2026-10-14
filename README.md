# Spottr

A clean-room Go + SvelteKit rewrite of the Spotnet/Spotweb Usenet spot indexer.
Single binary, SQLite database, built-in Newznab-compatible API for Prowlarr/Sonarr/Radarr.

## Features

- **Browse & search** spots across all categories (Video, Audio, Apps, Games, Books, ...) with SQLite FTS5
- **Format-aware icons** — Blu-ray, UHD, FLAC, DTS, Linux/Windows/Mac, and more
- **Newznab API** — plug straight into Prowlarr, Sonarr, Radarr (API-key protected)
- **SABnzbd integration** — Spottr fetches the NZB and uploads it to your queue
- **Full history backfill** — walk a group back to its first article, resuming across restarts
- **Signature verification** — RSA spot signatures and spotter identities, Spotnet-compatible
- **Spotter blacklist** — drop known-bad posters from an external feed
- **Retention & cache eviction** — bounded database growth
- **Hardened by default** — non-root, read-only rootfs, CSP, rate limiting, bounded decompression
- **Observability** — Prometheus metrics, structured request logs, health/readiness probes
- **Small footprint** — ~25 MB Docker image, no external runtime dependencies

## Quick start

```bash
git clone https://github.com/rousseauxy/spottr.git
cd spottr
cp .env.example .env
# Fill in your NNTP credentials and set APP_PASSWORD (or AUTH_MODE=proxy)
docker compose up -d
```

Open [http://localhost:8080](http://localhost:8080).

On first run Spottr syncs the last `SYNC_LOOKBACK` articles. Enable `BACKFILL=true`
to walk the rest of the group's history backwards in the background.

The Newznab API key is generated once, stored in the database, and printed to the
log on the run that created it:

```bash
docker compose logs spottr | grep "API key"
```

It is **stable across restarts and container recreation**. It only changes if you
clear it from the database or set `API_KEY` explicitly.

## Configuration

All configuration is via environment variables (or a `.env` file).

### Usenet

| Variable | Required | Default | Description |
|---|---|---|---|
| `NNTP_HOST` | **yes** | — | Usenet server hostname |
| `NNTP_PORT` | no | `563` | NNTP port |
| `NNTP_TLS` | no | `true` | Enable TLS |
| `NNTP_USER` | no | — | NNTP username |
| `NNTP_PASS` | no | — | NNTP password |
| `NNTP_MAX_CONNS` | no | `4` | **Hard ceiling** on concurrent upstream connections |

### Sync & backfill

| Variable | Required | Default | Description |
|---|---|---|---|
| `SYNC_INTERVAL` | no | `15m` | How often to poll for new spots |
| `SYNC_LOOKBACK` | no | `500000` | Articles to reach back on the **first run only** |
| `BACKFILL` | no | `false` | Walk history backwards towards the group's first article |

`SYNC_LOOKBACK` is one-way: forward sync only ever moves ahead of the highest
article seen, so anything older than the initial window is unreachable without
`BACKFILL`. The backfill worker checkpoints after every chunk (resumes across
restarts) and paces itself, since it shares the NNTP pool with live traffic.

### Authentication

| Variable | Required | Default | Description |
|---|---|---|---|
| `AUTH_MODE` | no | `password` | `password`, `proxy`, or `open` |
| `APP_PASSWORD` | if `password` | — | Password for the built-in login |
| `SESSION_DURATION` | no | `24h` | Session cookie lifetime |
| `SECURE_COOKIE` | no | `false` | Set the `Secure` flag (enable behind HTTPS) |
| `TRUSTED_PROXIES` | no | — | CIDRs allowed to set `X-Forwarded-For` / `X-Real-Ip` |

Spottr **refuses to start** in `password` mode without `APP_PASSWORD`. Running
without in-app auth is a deliberate choice, not an accident:

- `password` — built-in login, brute-force lockout (default)
- `proxy` — an authenticating reverse proxy (Authentik, oauth2-proxy) is the only route in
- `open` — no authentication at all

`TRUSTED_PROXIES` matters: forwarding headers are honoured **only** from listed
peers. Otherwise the socket peer wins, because the login lockout and rate
limiter key on client IP and a forgeable header would bypass both.

### Newznab API

| Variable | Required | Default | Description |
|---|---|---|---|
| `API_KEY` | no | from database | Override the stored key (not persisted) |

Leave unset to let the database own the key. Set it to pin the key declaratively
(useful with secret managers). The API requires a valid key — `?apikey=` or an
`X-Api-Key` header.

### Limits

| Variable | Required | Default | Description |
|---|---|---|---|
| `MAX_PAGE_SIZE` | no | `200` | Cap on `limit=` |
| `MAX_OFFSET` | no | `100000` | Cap on `offset=` |
| `MAX_ARTICLE_BYTES` | no | `33554432` | Cap on a single NNTP article read (32 MiB) |
| `MAX_NZB_BYTES` | no | `67108864` | Cap on **decompressed** NZB size (64 MiB) |
| `RATE_LIMIT_RPS` | no | `5` | Per-IP request rate |
| `RATE_LIMIT_BURST` | no | `30` | Per-IP burst allowance |

NZBs are deflate streams fetched from a newsgroup anyone can post to, so the
decompressed size is attacker-controlled — hence `MAX_NZB_BYTES`. Pair it with a
container memory limit.

### Retention & cache

| Variable | Required | Default | Description |
|---|---|---|---|
| `RETENTION_DAYS` | no | `0` | Delete spots older than N days (`0` = keep forever) |
| `CACHE_MAX_BYTES` | no | `536870912` | Evict cached NZB/image blobs beyond this (512 MiB) |

### Content & moderation

| Variable | Required | Default | Description |
|---|---|---|---|
| `ALLOW_ADULT` | no | `false` | Show 18+ categories |
| `BLACKLIST_URL` | no | — | External spotter blacklist feed (XML) |
| `WHITELIST_URL` | no | — | External spotter whitelist feed (XML) |
| `VERIFY_SIGNATURES` | no | `false` | Drop spots whose header signature fails |
| `MODERATION_MODE` | no | `off` | Honour withdrawal requests: `off`, `mark`, `delete` |

`VERIFY_SIGNATURES` is off by default because legacy spots (keyid 1) are
unsigned by design and would all be discarded.

`MODERATION_MODE` handles Spotnet withdrawal ("DISPOSE") messages. These are
control messages and are **always** kept out of the index; this setting only
decides whether the spot they target is withdrawn. Start with `mark`
(reversible) before considering `delete`. See [Moderation](#moderation).

### Misc

| Variable | Required | Default | Description |
|---|---|---|---|
| `TZ` | no | `UTC` | Timezone |
| `DB_PATH` | no | `/data/spottr.db` | SQLite database path |
| `LISTEN_ADDR` | no | `:8080` | Listen address |

See [.env.example](.env.example) for a commented template.

## Endpoints

| Path | Auth | Purpose |
|---|---|---|
| `/healthz` | none | Liveness probe |
| `/readyz` | none | Readiness (checks the database) |
| `/api?t=…` | API key | Newznab (`caps`, `search`) |
| `/v1/spots`, `/v1/spots/{id}` | none | Browse and search |
| `/v1/spots/{id}/nzb` | none | Download an NZB |
| `/v1/spots/{id}/image` | none | Spot image |
| `/v1/spots/{id}/send-to-sab` | session | Push an NZB to SABnzbd |
| `/v1/queue` | session | SABnzbd queue |
| `/v1/metrics` | session | Prometheus metrics |

Pagination supports both `offset` and a keyset cursor: responses include
`next_before`, which you pass back as `before=` to page deeply without paying
for a growing `OFFSET`.

## Moderation

Spotnet withdrawals are **self-signed**: the article carries the poster's own
public key and a hashcash proof-of-work. A valid signature therefore proves only
that someone holds that keypair — and anyone can generate one.

Spottr honours a withdrawal only when **all** of these hold:

1. the signature verifies against the key in the article, and the hashcash checks out;
2. the resulting spotter ID matches the spotter ID of the targeted spot; and
3. the request arrives within 5 days of the spot.

Ownership is only known for spots that have been enriched, so an unknown owner
means the withdrawal is skipped. Failing closed costs an un-applied withdrawal;
failing open would let a stranger delete arbitrary spots.

## Newznab / Prowlarr

Add Spottr as a custom Newznab indexer in Prowlarr:

| Field | Value |
|---|---|
| URL | `http://spottr:8080` |
| API Key | value from `docker compose logs spottr \| grep "API key"` |

`tv-search` and `movie-search` are advertised as unavailable — Spottr indexes
Spotnet categories, not TVDB/IMDB identifiers, so plain `search` is the
supported mode.

## Deployment notes

The shipped image runs as uid `65532` and needs no privileges:

```yaml
user: "65532:65532"
read_only: true
security_opt: [no-new-privileges:true]
cap_drop: [ALL]
tmpfs: ["/tmp:rw,noexec,nosuid,size=64m"]
mem_limit: 1g
pids_limit: 256
```

An existing `/data` volume created by an older root-running image needs
`chown -R 65532:65532` once.

Do not publish the container port when a reverse proxy fronts Spottr — a
published port is a second route in that bypasses the proxy and its
authentication.

## Building from source

```bash
# Frontend (Node 22.17+; the image builds with Node 24)
cd web && npm ci && npm run build && cd ..

# Backend (requires Go 1.27.2+)
go build -o spottr ./cmd/spottr

# Or use Docker
docker build -t spottr .

# Tests
go test ./...
```

## Project layout

```
cmd/spottr/            — entrypoint
internal/
  api/router.go        — chi router: Newznab + JSON API + auth endpoints
  api/middleware.go    — client IP, rate limiting, security headers, request log
  api/csp.go           — CSP built from the shipped frontend's inline scripts
  api/apikey.go        — persisted Newznab API key
  api/metrics.go       — Prometheus metrics, health probes
  auth/session.go      — session store, brute-force lockout, cookies
  config/config.go     — env-var configuration and validation
  db/                  — SQLite schema, migrations, queries (FTS5), maintenance
  nntp/client.go       — NNTP client (TLS, chunked OVER, bounded reads)
  nntp/pool.go         — bounded connection pool
  nntp/msgid.go        — message-id validation (command-injection boundary)
  spotnet/parser.go    — Spotnet header/XML parser + image/NZB decoder
  spotnet/signing.go   — RSA signature verification, spotter IDs
  spotnet/moderation.go— withdrawal requests
  sync/syncer.go       — forward sync engine
  sync/backfill.go     — historical backfill worker
  sync/maintenance.go  — retention, cache eviction, blacklist refresh
  sabnzbd/client.go    — SABnzbd HTTP client
web/src/               — SvelteKit frontend (embedded into the binary)
Dockerfile             — multi-stage build (~25 MB final image)
docker-compose.yml     — deployment compose
```

## License

MIT
