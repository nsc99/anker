# Anker — Anki Stats Exporter

A self-hosted Go service that downloads a separate collection from an Anki sync
server and serves study statistics for [Homepage](https://gethomepage.dev/widgets/services/customapi/).
It runs without Anki Desktop and never opens the sync server's live database.

## Develop and run

Nix flakes provide Go, gopls, PostgreSQL tools, and the C compiler environment
needed to read Anki collections.
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

Keep credentials in the Git-ignored `.env` file and restrict access with
`chmod 600 .env`. Alternatively, set `ANKER_PASSWORD_FILE` to a private password
file outside the repository with permissions `600`. Do not paste credentials
into chat, commit them, or put them directly in command-line arguments.

Sync requests use binary content type. Redirects preserve the compressed POST
for up to five hops within the same origin, or a same-host HTTP-to-HTTPS upgrade
on default ports. Metadata discovery follows official AnkiWeb server handoffs over HTTPS.
Other redirects to login pages, other hosts, or URLs with query strings are refused. If this happens, configure the direct Anki server base URL
and check reverse-proxy authentication instead of forwarding sync credentials
through a browser login flow.

```sh
nix develop --command go test -race ./...
nix develop --command ./scripts/test-postgres.sh
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
| `ANKER_SYNC_URL` | required | Anki sync server base URL |
| `ANKER_USERNAME` | required | Sync account username |
| `ANKER_PASSWORD` | required unless using a file | Sync account password |
| `ANKER_PASSWORD_FILE` | unset | File containing the password |
| `ANKER_TIMEZONE` | `UTC` | IANA timezone used for study days |
| `ANKER_LISTEN` | `127.0.0.1:8080` | HTTP bind address |
| `ANKER_DATA_DIR` | `data` | Private snapshot directory; ignored in PostgreSQL mode |
| `ANKER_DATABASE_URL` | unset | PostgreSQL connection URL; enables PostgreSQL storage |
| `ANKER_DATABASE_URL_FILE` | unset | File containing the database URL; takes precedence |
| `ANKER_SYNC_INTERVAL` | `15m` | Time between refresh attempts |
| `ANKER_SYNC_TIMEOUT` | `5m` | Total timeout for each refresh |
| `ANKER_STALE_AFTER` | `30m` | Maximum age before marking statistics stale |

The timezone database is embedded in the binary. Credentials and downloaded
collections are ignored by Git. The API binds to loopback by default; expose
it on your private network or behind your existing authenticated proxy if
Homepage runs elsewhere. The API itself has no authentication.

## PostgreSQL storage and deployment

Set `ANKER_DATABASE_URL` in your secret configuration to enable PostgreSQL mode:

```dotenv
ANKER_DATABASE_URL='postgresql://anker:password@db.example/anker?sslmode=verify-full'
ANKER_LISTEN=0.0.0.0:8080
```

Or mount a private connection-string file and set `ANKER_DATABASE_URL_FILE`.
The database must already exist. At startup Anker creates its `anker` schema;
the database role needs permission to create that schema and its tables.
Connection strings and database error details are excluded from logs.

PostgreSQL stores the review history, rollover hour, and last successful sync
in `anker.reviews` and `anker.collection_state`. Each refresh replaces history
and metadata atomically using a transaction and bulk COPY. Downloads or writes
that fail leave the previous committed snapshot intact. Restarted instances
restore it from PostgreSQL before refreshing.

No persistent filesystem volume is required in this mode. Anki still delivers
SQLite files over its sync protocol; Anker downloads them into a private
temporary directory, validates and imports them, then deletes them. Provide
writable temporary storage (for example `/tmp`, or set `TMPDIR`), sized for your
collection up to the 1 GiB download limit. Review history is also cached in RAM.
`ANKER_DATA_DIR` is unused in this mode. Without a database URL, Anker retains
its original local-file storage behavior.

For deployment, provide the Anki and database credentials as secrets, bind to
`0.0.0.0:8080`, use `/healthz` for liveness and `/readyz` for readiness, and manage
PostgreSQL backups through your database provider. One database holds one Anki
collection. Replicas sharing it must use the same account and configuration.
A transactional advisory lock and shared sync timestamp allow one refresh per
configured interval across replicas; the others restore the last committed data. Set PostgreSQL's
`idle_in_transaction_session_timeout` above `ANKER_SYNC_TIMEOUT`, since the lock
is held during synchronization.

Run the full integration suite locally with:

```sh
nix develop --command ./scripts/test-postgres.sh
```

This starts a disposable PostgreSQL cluster on a private Unix socket, runs race
tests, and removes the cluster afterward. It installs no global service and
uses synthetic Anki collections. `nix build` runs this suite too. Plain
`go test` skips the database integration test unless `ANKER_TEST_DATABASE_URL`
is set to a **disposable** database; that test resets the `anker` schema.

## Synchronization

On startup and every configured interval, Anker logs in, checks sync metadata,
resolves the account’s sync server, and downloads the full collection using Anki's sync protocol 11 (zstd). It validates SQLite integrity
and required tables before atomically replacing its stored snapshot. It never
uploads changes or downloads media. Snapshots are limited to 1 GiB after
decompression; review history is cached in memory.

This deliberately uses full downloads rather than incremental synchronization.
Each download transfers the entire collection, advances the server's sync
sequence, and may interrupt an ongoing client sync. Choose an interval suitable
for your collection size; a device can retry its sync after a collision. The
service supports the official self-hosted server and AnkiWeb metadata routing.
For AnkiWeb, use `https://sync.ankiweb.net` as the base URL.
See the [Anki server documentation](https://docs.ankiweb.net/sync-server.html)
and the [upstream download implementation](https://github.com/ankitects/anki/blob/main/rslib/src/sync/collection/download.rs).

If login, download, or validation fails, the last successful snapshot stays
available with `stale: true` and `sync_ok: false`. A restart restores the snapshot
and its last sync time (stored in PostgreSQL metadata, or the local snapshot's
modification time in file mode). A new
instance with no valid snapshot returns HTTP 503 until a download succeeds.
In file mode, only run one exporter per data directory.

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

The sync wire format uses Anki compatibility identifier `26.09.3`, verified
from [upstream `.version`](https://github.com/ankitects/anki/blob/main/.version).
The client build identifier remains `anker`; this is a protocol compatibility
baseline, not the exporter’s release version.

SQLite connections register Anki’s `unicase` collation with Unicode case folding.
The implementation uses `golang.org/x/text v0.43.0`, verified with
`go list -m golang.org/x/text@latest` through the Go module registry.

PostgreSQL integration uses `github.com/jackc/pgx/v5 v5.11.0`, verified with
`go list -m github.com/jackc/pgx/v5@latest`. Development and integration-test tools
use PostgreSQL `18.6`, verified with
`nix eval --inputs-from . --raw nixpkgs#postgresql.version` against the locked
nixpkgs. Transitive Go dependencies are resolved and pinned by `go mod tidy`.
