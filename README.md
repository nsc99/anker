# Anker — Anki Stats Exporter

A self-hosted Go service that downloads a separate collection from an Anki sync
server and serves study statistics for [Homepage](https://gethomepage.dev/widgets/services/customapi/).
It runs without Anki Desktop and never opens the sync server's live database.

## Develop and run

Nix flakes provide Go, gopls, and the C compiler environment needed by SQLite.
No Homebrew packages or global Go installation are required. Go's module and
build caches stay in the ignored `.cache/` directory in this repository.

```sh
cp .env.example .env
# Edit .env with your sync server's base URL and credentials.
nix develop
set -a
source .env
set +a
go run ./cmd/anker
```

The server URL is the root of the Anki sync service (for example,
`http://127.0.0.1:27701`), including any reverse proxy prefix, without `/sync`.
Use the same credentials as your Anki devices. The password can instead be read
from `ANKER_PASSWORD_FILE`, which takes precedence over `ANKER_PASSWORD`.
The service does not automatically load `.env`.

```sh
nix develop --command go test -race ./...
nix develop --command go vet ./...
nix build
./result/bin/anker
```

A fresh Git checkout includes the flake files. When adding these files to an
existing repository, Nix's Git flake source requires them to be added to Git's
index before using the commands above.

## Configuration

| Environment variable | Default | Meaning |
| --- | --- | --- |
| `ANKER_SYNC_URL` | required | Self-hosted Anki sync server base URL |
| `ANKER_USERNAME` | required | Sync account username |
| `ANKER_PASSWORD` | required unless using a file | Sync account password |
| `ANKER_PASSWORD_FILE` | unset | File containing the password |
| `ANKER_TIMEZONE` | `UTC` | IANA timezone used for study days |
| `ANKER_LISTEN` | `127.0.0.1:8080` | HTTP bind address |
| `ANKER_DATA_DIR` | `data` | Private snapshot directory |
| `ANKER_SYNC_INTERVAL` | `15m` | Time between refresh attempts |
| `ANKER_SYNC_TIMEOUT` | `5m` | Total timeout for each refresh |
| `ANKER_STALE_AFTER` | `30m` | Maximum age before marking statistics stale |

The timezone database is embedded in the binary. Credentials and downloaded
collections are ignored by Git. The API binds to loopback by default; expose
it on your private network or behind your existing authenticated proxy if
Homepage runs elsewhere. The API itself has no authentication.

## Synchronization

On startup and every configured interval, Anker logs in and downloads the full
collection using Anki's sync protocol 11 (zstd). It validates SQLite integrity
and required tables before atomically replacing its private snapshot. It never
uploads changes or downloads media. Snapshots are limited to 1 GiB after
decompression; review history is cached in memory.

This deliberately uses full downloads rather than incremental synchronization.
Each download transfers the entire collection, advances the server's sync
sequence, and may interrupt an ongoing client sync. Choose an interval suitable
for your collection size; a device can retry its sync after a collision. The
service is intended for the official self-hosted Anki sync server, not AnkiWeb.
See the [Anki server documentation](https://docs.ankiweb.net/sync-server.html)
and the [upstream download implementation](https://github.com/ankitects/anki/blob/main/rslib/src/sync/collection/download.rs).

If login, download, or validation fails, the last successful snapshot stays
available with `stale: true` and `sync_ok: false`. A restart restores the snapshot
and its last sync time (stored as the snapshot's modification time). A new
instance with no valid snapshot returns HTTP 503 until a download succeeds.
Only run one exporter per data directory.

## API

- `GET /stats`: statistics JSON; HTTP 503 before a valid snapshot exists.
- `GET /healthz`: process health; HTTP 200.
- `GET /readyz`: HTTP 200 with a current snapshot and successful refresh,
  otherwise HTTP 503.

Example `/stats` response:

```json
{
  "reviews_today": 42,
  "study_time_seconds": 305.5,
  "study_streak": 7,
  "retention_percent": 90,
  "study_day": "2026-10-15",
  "rollover_hour": 4,
  "timezone": "Europe/Berlin",
  "last_successful_sync": "2026-10-15T12:00:00Z",
  "stale": false,
  "sync_ok": true
}
```

`reviews_today` counts answers from learning, review, relearning, and filtered
study, excluding manual rescheduling and entries without an answer. Study time
sums their logged answer durations. Retention is the percentage of **today's
scheduled review answers** rated Hard, Good, or Easy; it is `null` when there
are no scheduled review answers. These are answer counts, not unique cards.
The current streak counts consecutive study days ending today, or yesterday
if there have been no answers today, and expires after a missed day.

Days use the collection's `rollover` hour (default 04:00 for modern collections)
and `ANKER_TIMEZONE`, with calendar dates across daylight saving changes.
Stats are calculated at request time, so day rollover works even while offline.
Anker reads both legacy JSON configuration and modern configuration tables.

## Homepage

Use the URL reachable **from Homepage's server**:

```yaml
- Study:
    - Anki:
        widget:
          type: customapi
          url: http://anker:8080/stats
          refreshInterval: 60000
          mappings:
            - field: reviews_today
              label: Reviews
              format: number
            - field: study_time_seconds
              label: Study time
              format: duration
            - field: study_streak
              label: Streak
              format: number
            - field: retention_percent
              label: Retention (%)
              format: float
            - field: last_successful_sync
              label: Last sync
              format: relativeDate
            - field: stale
              label: Stale
```

Homepage's mapping and duration format follow its
[Custom API widget documentation](https://gethomepage.dev/widgets/services/customapi/).

## Dependency pins

`flake.lock` pins nixpkgs commit `319e4cebef10cbb636c6ea392137b52ad105336a`
(resolved from `nixpkgs-unstable`). Its Go package was verified as `1.26.8`
with `nix eval` against that revision. `go.mod` and `go.sum` pin SQLite driver
`v1.14.52` and zstd library `v1.20.3`, verified using the
[Go module proxy](https://proxy.golang.org/). All development tools come from
the pinned nixpkgs revision.
