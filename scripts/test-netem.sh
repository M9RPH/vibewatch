#!/usr/bin/env bash
set -euo pipefail

# Optional Linux/root-only latency regression. It creates isolated network
# namespaces, so the developer workstation's normal interfaces are untouched.
if [[ "${EUID}" -ne 0 ]]; then echo "test-netem.sh requires root" >&2; exit 2; fi
for cmd in ip tc curl go; do command -v "$cmd" >/dev/null || { echo "$cmd is required" >&2; exit 2; }; done
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$(mktemp /tmp/vw-netem-fixture.XXXXXX)"
A="vw-netem-a-$$"; B="vw-netem-b-$$"; VA="vwa$$"; VB="vwb$$"
cleanup(){ pkill -f "$BIN" >/dev/null 2>&1 || true; ip netns del "$A" >/dev/null 2>&1 || true; ip netns del "$B" >/dev/null 2>&1 || true; rm -f "$BIN"; }
trap cleanup EXIT
CGO_ENABLED=0 go build -trimpath -o "$BIN" "$ROOT/tests/integration/fixture"
ip netns add "$A"; ip netns add "$B"
ip link add "$VA" type veth peer name "$VB"
ip link set "$VA" netns "$A"; ip link set "$VB" netns "$B"
ip -n "$A" addr add 10.254.99.1/30 dev "$VA"; ip -n "$B" addr add 10.254.99.2/30 dev "$VB"
ip -n "$A" link set lo up; ip -n "$B" link set lo up; ip -n "$A" link set "$VA" up; ip -n "$B" link set "$VB" up
# 25ms each direction ~= 50ms RTT, plus modest jitter. Packet loss defaults to
# zero for deterministic CI; set VIBEWATCH_NETEM_LOSS=1% to exercise retries.
LOSS="${VIBEWATCH_NETEM_LOSS:-0%}"
ip netns exec "$A" tc qdisc add dev "$VA" root netem delay 25ms 5ms loss "$LOSS"
ip netns exec "$B" tc qdisc add dev "$VB" root netem delay 25ms 5ms loss "$LOSS"
ip netns exec "$B" env LISTEN_ADDR=10.254.99.2:18080 HEALTH_DELAY_SECONDS=2 "$BIN" >/tmp/vw-netem-server.$$ 2>&1 &
for _ in $(seq 1 20); do
  code="$(ip netns exec "$A" curl -sS --max-time 2 -o /dev/null -w '%{http_code}' http://10.254.99.2:18080/health || true)"
  [[ "$code" == "200" ]] && break
  sleep .3
done
[[ "${code:-}" == "200" ]] || { cat /tmp/vw-netem-server.$$ >&2 || true; echo "latency smoke test failed" >&2; exit 1; }
rm -f /tmp/vw-netem-server.$$
echo "Vibewatch netem smoke test passed (~50ms RTT, loss=$LOSS)."
