#!/usr/bin/env bash
# Swarm end-to-end smoke test (opt-in, not run in CI).
#
# Deploys stack.yml (server + global agent) and demo/stack-small.yml on the
# local Docker daemon, checks the HTTP API and the WebSocket snapshot, then
# tears everything down.
#
# If the daemon is not in a swarm, the script refuses to run unless --yes is
# given; it then runs `docker swarm init` and `docker swarm leave --force` at
# the end. An existing swarm is never left; only the resources this script
# created are removed. Intended for a single-node swarm: the locally built
# image is not available on other nodes.
#
# Usage: test/swarm_smoke.sh [--yes] [--skip-build]
#   --yes         allow initialising (and afterwards leaving) a swarm
#   --skip-build  reuse an existing $IMAGE
#
# Env: SMOKE_PORT (default 17800), IMAGE (default dockgraph:swarm-smoke)
set -euo pipefail

cd "$(dirname "$0")/.."

IMAGE="${IMAGE:-dockgraph:swarm-smoke}"
SMOKE_PORT="${SMOKE_PORT:-17800}"
STACK="dgsmoke"
DEMO_STACK="dgsmoke-demo"
SECRET="dg_agent_token"
SHARED_NET="demo_shared"
BASE_URL="http://localhost:$SMOKE_PORT"

ASSUME_YES=false
SKIP_BUILD=false
for arg in "$@"; do
  case "$arg" in
    --yes) ASSUME_YES=true ;;
    --skip-build) SKIP_BUILD=true ;;
    -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

INITIALISED_SWARM=false
CREATED_SECRET=false
CREATED_NETWORK=false

# ── Helpers ───────────────────────────────────────────────────

fail() {
  echo "FAIL: $*"
  echo "--- server logs ---"
  docker service logs --tail 50 "${STACK}_server" 2>/dev/null || true
  echo "--- agent logs ---"
  docker service logs --tail 20 "${STACK}_agent" 2>/dev/null || true
  exit 1
}

# wait_stack_removed <stack>: docker stack rm is asynchronous; wait until
# its services and networks are gone so secrets/networks can be removed.
wait_stack_removed() {
  local stack="$1"
  for _ in $(seq 1 30); do
    if [ -z "$(docker service ls -q --filter "label=com.docker.stack.namespace=$stack")" ] &&
       [ -z "$(docker network ls -q --filter "label=com.docker.stack.namespace=$stack")" ]; then
      return 0
    fi
    sleep 2
  done
  echo "WARN: stack $stack still being removed"
}

cleanup() {
  echo ""
  echo "Cleaning up..."
  docker stack rm "$DEMO_STACK" >/dev/null 2>&1 || true
  docker stack rm "$STACK" >/dev/null 2>&1 || true
  if [ "$INITIALISED_SWARM" = true ]; then
    echo "Leaving the swarm this script initialised..."
    docker swarm leave --force >/dev/null 2>&1 || true
    return
  fi
  wait_stack_removed "$DEMO_STACK"
  wait_stack_removed "$STACK"
  if [ "$CREATED_NETWORK" = true ]; then
    docker network rm "$SHARED_NET" >/dev/null 2>&1 || true
  fi
  if [ "$CREATED_SECRET" = true ]; then
    docker secret rm "$SECRET" >/dev/null 2>&1 || true
  fi
}

# wait_converged <stack>: wait until every service of the stack runs its
# desired number of tasks (e.g. "2/2").
wait_converged() {
  local stack="$1" pending
  for _ in $(seq 1 60); do
    pending=$(docker service ls --filter "label=com.docker.stack.namespace=$stack" \
      --format '{{.Replicas}}' | awk -F'[/ ]' '$1 != $2' | wc -l | tr -d ' ')
    if [ "$pending" = "0" ] && [ -n "$(docker service ls -q --filter "label=com.docker.stack.namespace=$stack")" ]; then
      echo "Stack $stack converged"
      return 0
    fi
    sleep 2
  done
  docker service ls --filter "label=com.docker.stack.namespace=$stack"
  fail "stack $stack did not converge"
}

# assert_http <label> <url> <expected_code>
assert_http() {
  local label="$1" url="$2" expected="$3" code
  code=$(curl -s -o /dev/null -w "%{http_code}" "$url" || true)
  [ "$code" = "$expected" ] || fail "$label — expected $expected, got $code"
  echo "$label: OK ($code)"
}

# ws_snapshot <file>: capture the first seconds of the WebSocket stream (the
# initial snapshot, as raw frames) into file.
ws_snapshot() {
  curl -s -N --max-time 3 -o "$1" \
    -H "Upgrade: websocket" \
    -H "Connection: Upgrade" \
    -H "Sec-WebSocket-Version: 13" \
    -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
    "$BASE_URL/ws" || true
}

# ── Swarm state ───────────────────────────────────────────────

echo "=== DockGraph Swarm Smoke Test ==="

state=$(docker info --format '{{.Swarm.LocalNodeState}}')
case "$state" in
  active)
    if [ "$(docker info --format '{{.Swarm.ControlAvailable}}')" != "true" ]; then
      echo "This node is a swarm worker; run the smoke test on a manager." >&2
      exit 1
    fi
    echo "Using the existing swarm (it will NOT be left afterwards)."
    echo "Stacks $STACK and $DEMO_STACK will be deployed and removed; UI port $SMOKE_PORT."
    ;;
  inactive)
    if [ "$ASSUME_YES" != true ]; then
      echo "WARNING: this Docker daemon is not part of a swarm." >&2
      echo "The smoke test needs to run 'docker swarm init' and, at the end," >&2
      echo "'docker swarm leave --force'. Re-run with --yes to allow this." >&2
      exit 1
    fi
    echo "WARNING: initialising a single-node swarm (will leave it at the end)..."
    docker swarm init >/dev/null 2>&1 || docker swarm init --advertise-addr 127.0.0.1 >/dev/null
    INITIALISED_SWARM=true
    ;;
  *)
    echo "Unexpected swarm state '$state'; aborting." >&2
    exit 1
    ;;
esac
trap cleanup EXIT

# ── Build & deploy ────────────────────────────────────────────

if [ "$SKIP_BUILD" = false ]; then
  echo "Building image $IMAGE..."
  docker build -t "$IMAGE" .
fi

if ! docker secret inspect "$SECRET" >/dev/null 2>&1; then
  echo "Creating secret $SECRET..."
  openssl rand -hex 32 | docker secret create "$SECRET" - >/dev/null
  CREATED_SECRET=true
fi

if ! docker network inspect "$SHARED_NET" >/dev/null 2>&1; then
  echo "Creating external network $SHARED_NET..."
  docker network create -d overlay --attachable "$SHARED_NET" >/dev/null
  CREATED_NETWORK=true
fi

echo "Deploying $STACK..."
DG_IMAGE="$IMAGE" DG_HTTP_PORT="$SMOKE_PORT" \
  docker stack deploy --resolve-image never -c stack.yml "$STACK" >/dev/null
wait_converged "$STACK"

# ── Server assertions ─────────────────────────────────────────

echo "Waiting for $BASE_URL/healthz..."
attempts=1
until curl -s "$BASE_URL/healthz" | grep -q ok; do
  [ "$attempts" -ge 30 ] && fail "health check did not pass after $attempts attempts"
  sleep 2
  attempts=$((attempts + 1))
done
echo "Health check: OK ($attempts attempt(s))"

info=$(curl -s "$BASE_URL/api/system/info")
echo "$info" | grep -q '"mode":"swarm"' || fail "system info mode is not swarm: $info"
echo "System info mode=swarm: OK"

# Probe each container's own healthcheck (the agent serves /healthz on its
# unpublished port, reachable only from inside).
for svc in server agent; do
  cid=$(docker ps -q --filter "label=com.docker.swarm.service.name=${STACK}_${svc}" | head -n1)
  [ -n "$cid" ] || fail "no local ${svc} container"
  docker exec "$cid" /dockgraph --healthcheck || fail "${svc} healthcheck failed"
  echo "${svc} healthcheck: OK"
done

# ── Demo stack ────────────────────────────────────────────────

echo "Deploying $DEMO_STACK..."
docker stack deploy -c demo/stack-small.yml "$DEMO_STACK" >/dev/null
wait_converged "$DEMO_STACK"

web_id=$(docker service inspect --format '{{.ID}}' "${DEMO_STACK}_web")
assert_http "Service inspect" "$BASE_URL/api/services/$web_id" 200
assert_http "Stack-scoped stats" "$BASE_URL/api/stats/history?stack=$DEMO_STACK" 200

snapshot=$(mktemp)
found=false
for _ in $(seq 1 15); do
  ws_snapshot "$snapshot"
  if grep -a -q "service:${DEMO_STACK}_web" "$snapshot" &&
     grep -a -q "service:${DEMO_STACK}_log-shipper" "$snapshot"; then
    found=true
    break
  fi
  sleep 2
done
[ "$found" = true ] || { rm -f "$snapshot"; fail "WebSocket snapshot lacks ${DEMO_STACK} services"; }
echo "WebSocket snapshot lists demo services: OK"

if grep -a -q "service:${STACK}_" "$snapshot"; then
  rm -f "$snapshot"
  fail "WebSocket snapshot includes DockGraph's own services (self-exclusion)"
fi
rm -f "$snapshot"
echo "Self-exclusion: OK"

echo ""
echo "=== All swarm smoke tests passed ==="
