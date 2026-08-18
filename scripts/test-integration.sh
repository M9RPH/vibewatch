#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="vibewatch-integration-fixture:v090"
BIN="$ROOT/tests/integration/fixture-server"
NAMES=(vw-it-health vw-it-parent vw-it-dependent)

cleanup() {
  for n in "${NAMES[@]}"; do docker rm -f "$n" >/dev/null 2>&1 || true; done
  rm -f "$BIN"
}
trap cleanup EXIT

command -v docker >/dev/null || { echo "docker is required" >&2; exit 2; }
command -v curl >/dev/null || { echo "curl is required" >&2; exit 2; }
docker version >/dev/null

printf '[1/3] Building static fixture and scratch image...\n'
CGO_ENABLED=0 GOOS=linux GOARCH="$(go env GOARCH)" go build -trimpath -o "$BIN" "$ROOT/tests/integration/fixture"
docker build -q -t "$IMAGE" -f "$ROOT/tests/integration/Dockerfile.fixture" "$ROOT/tests/integration" >/dev/null

printf '[2/3] Health warm-up regression...\n'
docker run -d --name vw-it-health -e HEALTH_DELAY_SECONDS=3 -e APP_VERSION=fixture-v2 -p 127.0.0.1::8080 "$IMAGE" >/dev/null
PORT="$(docker port vw-it-health 8080/tcp | awk -F: 'NR==1{print $NF}')"
for _ in $(seq 1 20); do [[ -n "$PORT" ]] && break; sleep .1; PORT="$(docker port vw-it-health 8080/tcp | awk -F: 'NR==1{print $NF}')"; done
code="$(curl -sS -o /tmp/vw-it-health.$$ -w '%{http_code}' "http://127.0.0.1:${PORT}/health" || true)"
[[ "$code" == "503" ]] || { echo "expected warm-up 503, got $code" >&2; exit 1; }
sleep 3.2
code="$(curl -sS -o /tmp/vw-it-health.$$ -w '%{http_code}' "http://127.0.0.1:${PORT}/health")"
[[ "$code" == "200" ]] || { echo "expected healthy 200, got $code" >&2; exit 1; }
[[ "$(cat /tmp/vw-it-health.$$)" == "healthy" ]] || { echo "unexpected health body" >&2; exit 1; }
rm -f /tmp/vw-it-health.$$

printf '[3/3] Network namespace stale-ID/recreate regression...\n'
docker run -d --name vw-it-parent -e LISTEN_ADDR=:18080 "$IMAGE" >/dev/null
docker run -d --name vw-it-dependent --network container:vw-it-parent -e LISTEN_ADDR=:18081 "$IMAGE" >/dev/null
OLD_PARENT="$(docker inspect -f '{{.Id}}' vw-it-parent)"
OLD_MODE="$(docker inspect -f '{{.HostConfig.NetworkMode}}' vw-it-dependent)"
case "$OLD_MODE" in container:"$OLD_PARENT"*) ;; *) echo "dependent did not bind to original parent id: $OLD_MODE" >&2; exit 1;; esac
# A stopped dependent retains the original concrete namespace id. Recreating the
# parent must not make a simple dependent restart magically safe.
docker stop vw-it-dependent >/dev/null
docker rm -f vw-it-parent >/dev/null
docker run -d --name vw-it-parent -e LISTEN_ADDR=:18080 "$IMAGE" >/dev/null
NEW_PARENT="$(docker inspect -f '{{.Id}}' vw-it-parent)"
[[ "$NEW_PARENT" != "$OLD_PARENT" ]] || { echo "parent was not recreated" >&2; exit 1; }
if docker start vw-it-dependent >/tmp/vw-it-start.$$ 2>&1; then
  echo "warning: this Docker release retained the old namespace after parent removal; validating explicit recreate anyway"
  docker stop vw-it-dependent >/dev/null || true
fi
rm -f /tmp/vw-it-start.$$
docker rm -f vw-it-dependent >/dev/null
docker run -d --name vw-it-dependent --network container:vw-it-parent -e LISTEN_ADDR=:18081 "$IMAGE" >/dev/null
NEW_MODE="$(docker inspect -f '{{.HostConfig.NetworkMode}}' vw-it-dependent)"
case "$NEW_MODE" in container:"$NEW_PARENT"*) ;; *) echo "dependent recreate did not bind to new parent id: $NEW_MODE" >&2; exit 1;; esac
[[ "$(docker inspect -f '{{.State.Running}}' vw-it-parent)" == "true" ]]
[[ "$(docker inspect -f '{{.State.Running}}' vw-it-dependent)" == "true" ]]

echo "Vibewatch Docker integration smoke tests passed."
