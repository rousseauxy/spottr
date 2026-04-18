# Spottr

A clean-room Go + SvelteKit rewrite of the Spotnet/Spotweb Usenet spot indexer.
Single binary, SQLite database, built-in Newznab-compatible API for Prowlarr/Sonarr/Radarr.

## Features

- **Browse & search** spots across all categories (Video, Audio, Apps, Games, Books, ...)
- **Format-aware icons** — Blu-ray, UHD, FLAC, DTS, Linux/Windows/Mac, and more
- **Newznab API** — plug straight into Prowlarr, Sonarr, Radarr
- **SABnzbd integration** — send NZBs to your download queue from the UI
- **Authentication** — optional single-password login with brute-force protection; disable when using an external proxy (Authentik, etc.)
- **Security headers** — X-Frame-Options, X-Content-Type-Options, Referrer-Policy
- **Small footprint** — ~25 MB Docker image, no external dependencies

## Quick start

```bash
git clone https://github.com/rousseauxy/spottr.git
cd spottr
cp .env.example .env
# Fill in your NNTP credentials in .env
docker compose up -d
```

Open [http://localhost:8080](http://localhost:8080).

On first run Spottr syncs the last `SYNC_LOOKBACK` articles (default 500 000 ≈ ~2 weeks).
Check the logs for the auto-generated Newznab API key:

```bash
docker compose logs spottr | grep "API key"
```

## Configuration

All configuration is via environment variables (or a `.env` file).

| Variable | Required | Default | Description |
|---|---|---|---|
| `NNTP_HOST` | **yes** | — | Usenet server hostname |
| `NNTP_PORT` | no | `563` | NNTP port |
| `NNTP_TLS` | no | `true` | Enable TLS |
| `NNTP_USER` | no | — | NNTP username |
| `NNTP_PASS` | no | — | NNTP password |
| `NNTP_MAX_CONNS` | no | `4` | Parallel NNTP connections |
| `SYNC_INTERVAL` | no | `15m` | How often to poll for new spots |
| `SYNC_LOOKBACK` | no | `500000` | Articles to back-fill on first run |
| `SAB_HOST` | no | — | SABnzbd hostname |
| `SAB_PORT` | no | `8080` | SABnzbd port |
| `SAB_API_KEY` | no | — | SABnzbd API key |
| `APP_PASSWORD` | no | — | Enable built-in login (empty = disabled) |
| `SESSION_DURATION` | no | `24h` | Session cookie lifetime |
| `ALLOW_ADULT` | no | `false` | Show 18+ categories |
| `API_KEY` | no | auto | Newznab API key (auto-generated if unset) |
| `TZ` | no | `Europe/Brussels` | Timezone |
| `DB_PATH` | no | `/data/spottr.db` | SQLite database path |

See [.env.example](.env.example) for a commented template.

## Authentication

By default auth is **disabled** — suitable when running behind an external auth proxy like Authentik.

To enable the built-in login screen set `APP_PASSWORD` in your `.env`:

```env
APP_PASSWORD=your-strong-password
```

Browsing is always public. Only sending to SABnzbd and triggering a manual sync require authentication.

## Newznab / Prowlarr

Add Spottr as a custom Newznab indexer in Prowlarr:

| Field | Value |
|---|---|
| URL | `http://spottr:8080` |
| API Key | value from `docker compose logs spottr \| grep "API key"` |

## Building from source

```bash
# Frontend
cd web && npm ci && npm run build && cd ..

# Backend (requires Go 1.22+)
go build -o spottr ./cmd/spottr

# Or use Docker
docker build -t spottr .
```

## Project layout

```
cmd/spottr/          — entrypoint
internal/
  api/router.go      — chi router: Newznab + JSON API + auth endpoints
  auth/session.go    — session store, rate limiting, cookie helpers
  config/config.go   — env-var configuration
  db/                — SQLite schema + query helpers (FTS5)
  nntp/client.go     — NNTP client (TLS, chunked OVER, binary body)
  spotnet/parser.go  — Spotnet header/XML parser + image/NZB decoder
  sync/syncer.go     — background sync engine
  sabnzbd/client.go  — SABnzbd HTTP client
web/src/             — SvelteKit frontend (embedded into binary)
Dockerfile           — multi-stage build (~25 MB final image)
docker-compose.yml   — deployment compose (pulls from GHCR)
```

## License

MIT
