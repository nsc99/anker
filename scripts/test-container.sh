#!/usr/bin/env bash
# Run on a Linux host with Docker; only synthetic credentials are used.
set -euo pipefail
image=${1:?image archive required}
demo_server=${2:?demo server binary required}
workspace=$(mktemp -d)
container_id=
demo_pid=
cleanup() {
  if [ -n "$container_id" ]; then docker logs "$container_id"; docker rm -f "$container_id" >/dev/null; fi
  if [ -n "$demo_pid" ]; then kill "$demo_pid" 2>/dev/null || true; wait "$demo_pid" 2>/dev/null || true; fi
  rm -rf "$workspace"
}
trap cleanup EXIT
"$demo_server" --url-file "$workspace/url" &
demo_pid=$!
for _ in {1..100}; do
  [ -f "$workspace/url" ] && break
  kill -0 "$demo_pid"
  sleep 0.1
done
sync_url=$(cat "$workspace/url")
docker load --input "$image"
# Host networking connects to the demo server's loopback address on Linux.
container_id=$(docker run -d --network host \
  -e ANKER_SYNC_URL="$sync_url" -e ANKER_USERNAME=demo -e ANKER_PASSWORD=demo \
  -e ANKER_LISTEN=127.0.0.1:18080 anker:local)
for _ in {1..120}; do
  if curl -fsS http://127.0.0.1:18080/readyz >/dev/null 2>&1; then break; fi
  [ "$(docker inspect -f '{{.State.Running}}' "$container_id")" = true ]
  sleep 0.5
done
curl -fsS http://127.0.0.1:18080/healthz >/dev/null
curl -fsS http://127.0.0.1:18080/stats | jq -e \
  '.sync_ok and (.stale | not) and .reviews_today == 10 and .study_streak == 3 and .retention_percent == 80' >/dev/null
[ "$(docker inspect -f '{{.Config.User}}' "$container_id")" = 10001:10001 ]
echo 'PASS: non-root container downloads a collection and serves fresh statistics.'
