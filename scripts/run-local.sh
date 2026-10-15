#!/usr/bin/env bash
set -euo pipefail

mode=$1
exporter=$2
demo_server=$3
shift 3
port=8080
check=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --port) port=${2:?provide a port}; shift 2 ;;
    --check) check=true; shift ;;
    *) echo "Usage: nix run .#$mode -- [--port PORT] [--check]" >&2; exit 2 ;;
  esac
done
if ! [[ "$port" =~ ^[0-9]+$ ]] || [ "$port" -lt 1 ] || [ "$port" -gt 65535 ]; then
  echo 'Port must be between 1 and 65535' >&2
  exit 2
fi

# Every local run uses a private disposable database, regardless of .env's URL.
cluster=$(mktemp -d /tmp/anker-local.XXXXXX)
exporter_pid=
demo_pid=
cleanup() {
  if [ -n "$exporter_pid" ]; then
    kill "$exporter_pid" 2>/dev/null || true
    wait "$exporter_pid" 2>/dev/null || true
  fi
  if [ -n "$demo_pid" ]; then
    kill "$demo_pid" 2>/dev/null || true
    wait "$demo_pid" 2>/dev/null || true
  fi
  if [ -f "$cluster/postgres/postmaster.pid" ]; then
    pg_ctl -D "$cluster/postgres" -m immediate -w stop >/dev/null 2>&1 || true
  fi
  rm -rf "$cluster"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [ "$mode" = demo ]; then
  # Never read or forward real credentials in the offline demo.
  for variable in "${!ANKER_@}"; do unset "$variable"; done
  "$demo_server" --url-file "$cluster/demo-url" &
  demo_pid=$!
  for _ in {1..100}; do
    [ -f "$cluster/demo-url" ] && break
    kill -0 "$demo_pid" 2>/dev/null || { echo 'Demo server failed to start' >&2; exit 1; }
    sleep 0.05
  done
  export ANKER_SYNC_URL
  ANKER_SYNC_URL=$(cat "$cluster/demo-url")
  export ANKER_USERNAME=demo ANKER_PASSWORD=demo ANKER_TIMEZONE=UTC
  export ANKER_SYNC_INTERVAL=5s ANKER_STALE_AFTER=15s
fi

# Disable inherited PostgreSQL connection defaults; all connections stay local.
unset PGHOST PGPORT PGDATABASE PGUSER PGPASSWORD PGSERVICE PGSERVICEFILE PGOPTIONS
initdb -D "$cluster/postgres" --auth=trust --username=anker --locale=C --encoding=UTF8 >/dev/null
pg_ctl -D "$cluster/postgres" -o "-F -k $cluster -c listen_addresses=''" -l "$cluster/postgres.log" -w start >/dev/null
createdb -h "$cluster" -U anker anker
export ANKER_DATABASE_URL="postgresql://anker@/anker?host=$cluster&sslmode=disable"
export ANKER_DATABASE_URL_FILE=
export ANKER_LISTEN="127.0.0.1:$port"
mkdir "$cluster/temporary"
export TMPDIR="$cluster/temporary"
if "$check"; then export ANKER_SYNC_INTERVAL=1h; fi

start_exporter() { "$exporter" & exporter_pid=$!; }
wait_ready() {
  local deadline=$((SECONDS + 120))
  while [ "$SECONDS" -lt "$deadline" ]; do
    kill -0 "$exporter_pid" 2>/dev/null || { echo 'Exporter stopped before becoming ready' >&2; return 1; }
    if curl -fsS --max-time 2 "http://$ANKER_LISTEN/readyz" >/dev/null 2>&1; then return 0; fi
    sleep 0.2
  done
  echo 'Timed out waiting for /readyz; check the exporter logs above' >&2
  return 1
}
start_exporter
if "$check"; then
  wait_ready
  stats=$(curl -fsS "http://$ANKER_LISTEN/stats")
  jq -e '.sync_ok and (.stale | not)' <<<"$stats" >/dev/null
  if [ "$mode" = demo ]; then
    jq -e '.reviews_today == 10 and .study_streak == 3 and .retention_percent == 80' <<<"$stats" >/dev/null
  fi
  stamp=$(jq -r '.last_successful_sync' <<<"$stats")
  kill "$exporter_pid"
  wait "$exporter_pid"
  exporter_pid=
  export ANKER_SYNC_URL=http://127.0.0.1:1
  start_exporter
  wait_ready
  restored=$(curl -fsS "http://$ANKER_LISTEN/stats")
  jq -e --arg stamp "$stamp" '.last_successful_sync == $stamp and .sync_ok and (.stale | not)' <<<"$restored" >/dev/null
  echo 'PASS: PostgreSQL import, fresh stats, and offline restart recovery.'
  exit 0
fi

echo "Stats: http://$ANKER_LISTEN/stats"
echo "Readiness: http://$ANKER_LISTEN/readyz"
if [ "$mode" = demo ]; then
  echo "Simulate a sync outage: curl -X POST $ANKER_SYNC_URL/demo/fail"
  echo "Recover sync: curl -X POST $ANKER_SYNC_URL/demo/recover"
fi
echo 'Press Ctrl+C to stop and delete the local database and temporary files.'
wait "$exporter_pid"
