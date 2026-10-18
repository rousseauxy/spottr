# Spottr

A clean-room Go + SvelteKit rewrite of the Spotnet/Spotweb Usenet spot indexer.
Single binary, SQLite database, built-in Newznab-compatible API for Prowlarr/Sonarr/Radarr.

Documentation: [docs.xact.cloud/spottr](https://docs.xact.cloud/spottr/index.html) (running in production,
Prowlarr/Sonarr/Radarr, sign-in behind Authentik, what to open in a firewall).

## Features

- **Browse & search** with SQLite FTS5, filtered by kind: Movies, Series, Books, Comics, Audio, Games, Apps
  (older spots without a type code get one derived from their other codes, as Spotweb does)
- **Links everywhere** — a spot's kind, poster, tag and each badge open the list filtered by it
- **Format-aware icons** — Blu-ray, UHD, FLAC, DTS, Linux/Windows/Mac, and more
- **Newznab API** — plug straight into Prowlarr, Sonarr, Radarr (API-key protected), categories by kind
- **SABnzbd integration** — Spottr fetches the NZB and uploads it, with one of SABnzbd's categories and the
  archive password (filled in when the poster wrote it in the description)
- **Withdrawals honoured** — a poster's own (signed, within 5 days) and the network moderator's, also for
  the history, as Spotweb does
- **18+ only on its own tab**, and only when `ALLOW_ADULT` is on
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
| `NNTP_PORT` | no | `119` | NNTP port (`563` for TLS, as most providers want) |
| `NNTP_TLS` | no | `false` | Enable TLS |
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
| `TRUSTED_PROXIES` | no | — | CIDRs allowed to set `X-Forwarded-For` / `X-Real-Ip` / `X-Forwarded-Proto` and the proxy sign-in header |
| `PROXY_AUTH_HEADER` | no | `X-authentik-username` | With `proxy`: the header the proxy sets for a signed-in user; lets that user download NZBs |

Spottr **refuses to start** in `password` mode without `APP_PASSWORD`. Running
without in-app auth is a deliberate choice, not an accident:

- `password` — built-in login, brute-force lockout (default)
- `proxy` — an authenticating reverse proxy (Authentik, oauth2-proxy) is the only route in
- `open` — no authentication at all

`TRUSTED_PROXIES` matters: forwarding headers are honoured **only** from listed
peers. Otherwise the socket peer wins, because the login lockout and rate
limiter key on client IP and a forgeable header would bypass both. List the
address the proxy's requests **arrive from** (a VPN or router that rewrites the
source changes it), never a whole network your clients are on.

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
| `ALLOW_ADULT` | no | `false` | Allow 18+ spots: on their own tab only, never in All, other filters or searches |
| `BLACKLIST_URL` | no | — | External spotter blacklist feed (XML) |
| `WHITELIST_URL` | no | — | External spotter whitelist feed (XML) |
| `VERIFY_SIGNATURES` | no | `false` | Drop spots whose header signature fails |
| `MODERATION_MODE` | no | `off` | Honour withdrawal requests: `off`, `mark`, `delete` |

`VERIFY_SIGNATURES` is off by default because legacy spots (keyid 1) are
unsigned by design and would all be discarded.

`MODERATION_MODE` handles Spotnet withdrawal ("DISPOSE") messages. These are
control messages and are **always** kept out of the index; this setting only
decides whether the spot they target is withdrawn. Start with `mark`
(reversible) before considering `delete`. With either, the first start also
applies the withdrawals of the past once (the withdrawal sweep). See
[Moderation](#moderation).

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
| `/api?t=…` | API key | Newznab (`caps`, `search`, `tvsearch`, `movie`) |
| `/v1/spots`, `/v1/spots/{id}` | none | Browse and search (`kind=`, `poster=`, `tag=`, `sub=`) |
| `/v1/spots/{id}/nzb` | API key or signed in | Download an NZB |
| `/v1/spots/{id}/image` | none | Spot image |
| `/v1/spots/{id}/send-to-sab` | session | Push an NZB to SABnzbd (name, category, password) |
| `/v1/sab/categories` | session | SABnzbd's categories |
| `/v1/queue` | session | SABnzbd queue |
| `/v1/metrics` | session | Prometheus metrics |

Pagination supports both `offset` and a keyset cursor: responses include
`next_before`, which you pass back as `before=` to page deeply without paying
for a growing `OFFSET`.

## Moderation

Two kinds of withdrawal are honoured, as in Spotweb.

**A poster's own** is **self-signed**: the article carries the poster's own
public key and a hashcash proof-of-work. A valid signature therefore proves only
that someone holds that keypair — and anyone can generate one. It is honoured
only when **all** of these hold:

1. the signature verifies against the key in the article, and the hashcash checks out;
2. the resulting spotter ID matches the spotter ID of the targeted spot; and
3. the request arrives within 5 days of the spot.

A spot's owner is in its full headers (`X-User-Key`), which the overview does
not carry; when a withdrawal arrives for a spot whose owner is unknown, Spottr
reads that spot's headers once and stores the owner.

**The network moderator's** carries no key of its own and is signed with
Spotnet's moderator master key (keyid 2); verified, it applies to any spot.

A personal withdrawal whose signature fails is never applied. (Spotweb treats it
as a moderator command, so a hashcash alone would remove any spot.)

Spotnet's self-signed keys are 384-bit RSA, which Go's `crypto/rsa` refuses
since Go 1.24; Spottr verifies PKCS#1 v1.5 itself, with a regression test on a
real withdrawal.

## Newznab / Prowlarr

Add Spottr as a Generic Newznab indexer in Prowlarr, on its **direct** address
(not through a signing-in proxy, which Prowlarr cannot get through):

| Field | Value |
|---|---|
| URL | `http://spottr:8080` |
| API Path | `/api` |
| API Key | value from `docker compose logs spottr \| grep "API key"` |

Categories by kind: 2000 movies, 5000 series, 7000 books and comics (7020, 7030),
3000 audio, 1000/4000 games and software; several combine. 18+ (6000) only when
asked for on its own and `ALLOW_ADULT` is on. `tvsearch` and `movie` are answered
as a title search (no TVDB/IMDB identifiers).

The NZB links in the results carry the API key and use the scheme the client
used, so Prowlarr, Sonarr and SABnzbd can fetch them. More:
[Prowlarr, Sonarr and Radarr](https://docs.xact.cloud/spottr/newznab.html).

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
