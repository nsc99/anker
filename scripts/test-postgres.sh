#!/usr/bin/env bash
set -euo pipefail

# A private Unix socket and disposable cluster: no host service or TCP port.
cluster=$(mktemp -d /tmp/anker-postgres-test.XXXXXX)
cleanup() {
  if [ -f "$cluster/data/postmaster.pid" ]; then
    pg_ctl -D "$cluster/data" -m immediate -w stop >/dev/null 2>&1 || true
  fi
  rm -rf "$cluster"
}
trap cleanup EXIT

initdb -D "$cluster/data" --auth=trust --username=anker --locale=C --encoding=UTF8 >/dev/null
pg_ctl -D "$cluster/data" -o "-F -k $cluster -c listen_addresses=''" -l "$cluster/postgres.log" -w start >/dev/null
createdb -h "$cluster" -U anker anker_test
export ANKER_TEST_DATABASE_URL="postgresql://anker@/anker_test?host=$cluster&sslmode=disable"
go test -race ./...
