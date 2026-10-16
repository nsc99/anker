# Anker — Anki Stats Exporter

A self-hosted Go service that downloads a separate collection from an Anki sync
server and serves study statistics for [Homepage](https://gethomepage.dev/widgets/services/customapi/).
It runs without Anki Desktop and never opens the sync server's live database.

## Try it locally

Start a complete demo with synthetic Anki data and a disposable PostgreSQL
instance. It requires no Anki account or `.env` file:

```sh
nix run .#demo
```

Then open `http://127.0.0.1:8080/stats` or run:

```sh
curl http://127.0.0.1:8080/stats
curl http://127.0.0.1:8080/readyz
```

The demo supplies 10 answers today, 60 seconds of study time, a three-day streak,
and 80% retention. It refreshes every five seconds. The terminal prints commands
to simulate a sync outage and recover it; during an outage `/stats` retains the
last good data with `stale: true`, and `/readyz` returns 503.

For a repeatable automated smoke test:

```sh
nix run .#demo -- --check --port 18080
```

This verifies PostgreSQL import, fresh JSON statistics, and recovery after a
restart with the sync server unavailable, then exits and cleans up. Use
`--port PORT` with either local app if port 8080 is already occupied.

To use your actual Anki account with the same local PostgreSQL setup:

```sh
cp .env.example .env
chmod 600 .env
# Edit .env with your Anki server URL and credentials.
nix run .#dev
```

The `dev` app automatically loads `.env` from the current directory, or the file
specified by `ANKER_ENV_FILE`, and also accepts exported environment variables.
It synchronizes the configured Anki account. The `demo` app clears inherited
Anker configuration and uses only its synthetic server.

Both local apps use a private Unix socket for PostgreSQL and delete their local
database, collections, and temporary files when stopped with Ctrl+C. They
always override database connection settings with the disposable local database.
Use the normal `nix run .` service with `ANKER_DATABASE_URL` for a persistent or
cloud database. No global database service or container runtime is installed.

Run the complete automated regression suite with:

```sh
nix flake check
```

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

## Container images and releases

GitHub Actions builds and tests native Linux `amd64` and `arm64` images on pull
requests and before publishing, using the locked Nix dependencies. The workflow runs the PostgreSQL integration suite,
`go vet`, and a synthetic sync smoke test inside each non-root container before
publishing a multi-platform image to `ghcr.io/nsc99/anker`.

- Every push to `master` publishes `latest` and `sha-<full commit SHA>`.
- Publishing a GitHub release with a tag such as `vMAJOR.MINOR.PATCH` publishes
  that exact version tag and its commit tag. Prereleases can use a suffix such
  as `-rc.1`. Release builds leave the rolling `latest` tag unchanged.
- Manual runs publish the selected revision's commit tag only.

To release, first merge and check the default-branch image workflow, then create
and publish a GitHub release targeting that tested commit with a new version
tag and release notes describing changes, migration steps, and known limitations.
Consider enabling GitHub immutable releases in repository settings to protect
published tags. Prepare a draft and attach any GitHub release assets before
publishing it. See [release practices](docs/RELEASING.md) for the full checklist.
The release workflow rebuilds and tests both platforms
before publishing the version tag. Check its success before announcing that the
image is available. Keep existing release tags fixed; publish a new patch
release for corrections. Pin deployments to an image digest for an exact,
repeatable artifact, and retain the previous digest for rollback.

The workflow uses GitHub's short-lived `GITHUB_TOKEN`, granting package write
access only to its publishing job. Actions are pinned by full commit SHA; Nix
and Go dependencies retain this repository's lock files. No registry password
or long-lived publishing token is required. Package visibility follows GHCR
settings; a private package requires authentication to pull.

For local builds on Linux:

```sh
nix build .#container
docker load --input result
```

Run the published image with your private configuration:

```sh
docker run --rm --env-file .env -p 127.0.0.1:8080:8080 \
  ghcr.io/nsc99/anker:latest
```

In PostgreSQL mode, provide writable `/tmp`; persistent filesystem storage is
unnecessary. In local-file mode, mount a volume at `/data`, writable by UID/GID
`10001:10001`, to preserve snapshots across container replacements. The image
binds to `0.0.0.0:8080` internally and includes TLS trust certificates. If `.env`
sets `ANKER_LISTEN`, ensure it also binds to `0.0.0.0:8080` inside the container.
Secrets referenced through `*_FILE` variables must be mounted and readable by
that UID. Use `/healthz` and `/readyz` for your orchestrator's probes.

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
- `GET /activity?days=365`: daily history for an activity grid; HTTP 503
  before a valid snapshot exists. `days` defaults to 365 and accepts 1–3660;
  invalid or repeated values return HTTP 400.
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

### Activity grid data

`/activity` prepares the data for a future Anki-style study heatmap. It returns
one cell per study day, oldest first, including days with zero reviews. Each
cell has `date` (`YYYY-MM-DD`), `reviews`, and `study_time_seconds`. A UI can
group these cells into calendar weeks and shade each cell by review count;
zero-review cells show missed days. Dates are study-day labels, so use them
directly rather than converting them from UTC timestamps in the browser.

The response also includes `study_day`, `rollover_hour`, `timezone`,
`study_streak`, `last_successful_sync`, `stale`, and `sync_ok`, with the same
meaning as `/stats`. The streak considers all available history, even if it
starts before the requested window. Daily counts use the same answer filters,
timezone, and rollover as today's statistics; future answers are excluded.
History remains available after a failed refresh, marked stale. Both storage
modes retain the full review history, so no additional database migration or
collection download is required.

```sh
curl 'http://localhost:8080/activity?days=365'
```

Example cells within the response's `days` array:

```json
[
  {"date": "2026-10-13", "reviews": 12, "study_time_seconds": 85},
  {"date": "2026-10-14", "reviews": 0, "study_time_seconds": 0},
  {"date": "2026-10-15", "reviews": 42, "study_time_seconds": 305.5}
]
```

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

See [dependency maintenance](docs/DEPENDENCIES.md) for Renovate scheduling,
update groups, and the required Nix vendor-hash update on Go dependency PRs.

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
