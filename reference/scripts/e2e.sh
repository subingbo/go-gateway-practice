#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

LOG=".run/e2e-devstack.log"
mkdir -p .run
rm -f "$LOG"

fail() {
  echo "FAIL: $*" >&2
  echo "---- devstack log tail ----" >&2
  tail -n 60 "$LOG" >&2 || true
  exit 1
}

json_field() {
  python -c "import sys,json; print(json.load(sys.stdin)['$1'])"
}

check_code() {
  local name="$1"
  local expected="$2"
  local url="$3"
  shift 3

  local code
  code=$(curl -sS -o /dev/null -w '%{http_code}' "$@" "$url")
  if [ "$code" != "$expected" ]; then
    fail "$name: got HTTP $code, want $expected"
  fi
  echo "ok   $name ($code)"
}

echo "starting devstack..."
go run ./cmd/devstack --run-for 120s >"$LOG" 2>&1 &
DEVSTACK_PID=$!

cleanup() {
  curl -sS -X POST http://127.0.0.1:18099/shutdown >/dev/null 2>&1 || true

  for _ in $(seq 1 20); do
    if ! kill -0 "$DEVSTACK_PID" 2>/dev/null; then
      break
    fi
    sleep 0.5
  done

  if kill -0 "$DEVSTACK_PID" 2>/dev/null; then
    if command -v taskkill >/dev/null 2>&1; then
      taskkill //F //T //PID "$DEVSTACK_PID" >/dev/null 2>&1 || true
    else
      kill "$DEVSTACK_PID" 2>/dev/null || true
    fi
  fi

  wait "$DEVSTACK_PID" 2>/dev/null || true
}
trap cleanup EXIT

echo "waiting for edge gateway..."
READY=0
for _ in $(seq 1 120); do
  if curl -fsS http://127.0.0.1:8080/ready >/dev/null 2>&1; then
    READY=1
    break
  fi
  if ! kill -0 "$DEVSTACK_PID" 2>/dev/null; then
    fail "devstack exited before the gateway became ready"
  fi
  sleep 0.5
done

if [ "$READY" != "1" ]; then
  fail "gateway did not become ready in time"
fi
echo "ok   gateway ready"

# devstack 会为演示账号生成随机密码并打印在摘要里，这里直接读取。
sleep 1
ADMIN_PASSWORD=$(grep -a "admin /" "$LOG" | tail -1 | sed 's/.*admin \/ //' | tr -d '\r')
VIEWER_PASSWORD=$(grep -a "viewer /" "$LOG" | tail -1 | sed 's/.*viewer \/ //' | tr -d '\r')

if [ -z "$ADMIN_PASSWORD" ] || [ -z "$VIEWER_PASSWORD" ]; then
  fail "could not read generated demo credentials from devstack log"
fi

echo "checking dashboards and health..."
curl -sS http://127.0.0.1:8080/health | grep -q '"status":"ok"' || fail "edge health"
curl -sS http://127.0.0.1:8080/ | grep -q '<title>Go Gateway Reference' || fail "edge dashboard"
curl -sS http://127.0.0.1:18000/ | grep -q '<title>Mesh Control Plane' || fail "control plane dashboard"
echo "ok   dashboards"

echo "logging in..."
ADMIN_LOGIN=$(curl -fsS -X POST http://127.0.0.1:8080/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASSWORD\"}")
TOKEN=$(printf '%s' "$ADMIN_LOGIN" | json_field access_token)
REFRESH=$(printf '%s' "$ADMIN_LOGIN" | json_field refresh_token)

VIEWER_LOGIN=$(curl -fsS -X POST http://127.0.0.1:8080/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"viewer\",\"password\":\"$VIEWER_PASSWORD\"}")
VIEWER_TOKEN=$(printf '%s' "$VIEWER_LOGIN" | json_field access_token)
echo "ok   login (admin + viewer)"

echo "checking authentication and RBAC..."
check_code "whoami without token" 401 "http://127.0.0.1:8080/api/whoami"
check_code "whoami with token" 200 "http://127.0.0.1:8080/api/whoami" -H "Authorization: Bearer $TOKEN"
check_code "viewer blocked from admin route" 403 "http://127.0.0.1:8080/api/admin/ping" -H "Authorization: Bearer $VIEWER_TOKEN"
check_code "admin allowed on admin route" 200 "http://127.0.0.1:8080/api/admin/ping" -H "Authorization: Bearer $TOKEN"

echo "checking north-south routing..."
check_code "user service" 200 "http://127.0.0.1:8080/api/users/e2e" -H "Authorization: Bearer $TOKEN"

CANARY_VERSION=$(curl -sS "http://127.0.0.1:8080/api/users/e2e" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'x-canary: true' | json_field version)
if [ "$CANARY_VERSION" != "v2" ]; then
  fail "canary header routed to $CANARY_VERSION, want v2"
fi
echo "ok   canary header -> v2"

ORDER_SOURCE=$(curl -sS "http://127.0.0.1:8080/api/orders/e2e" \
  -H "Authorization: Bearer $TOKEN" | grep -o 'spiffe://[^"]*order-service' | head -1 || true)
if [ "$ORDER_SOURCE" != "spiffe://cluster.local/ns/default/sa/order-service" ]; then
  fail "east-west call did not carry order-service identity (got: $ORDER_SOURCE)"
fi
echo "ok   east-west order -> user via mesh"

check_code "edge timeout returns 504" 504 "http://127.0.0.1:8080/api/orders/slow" -H "Authorization: Bearer $TOKEN" -m 10
check_code "fault injection returns 500" 500 "http://127.0.0.1:8080/api/users/faulty" -H "Authorization: Bearer $TOKEN" -m 10

echo "checking direct mesh access..."
DIRECT_SOURCE=$(curl -sS -x http://127.0.0.1:15003 http://user-service/users/e2e | json_field source)
if [ "$DIRECT_SOURCE" != "spiffe://cluster.local/ns/default/sa/order-service" ]; then
  fail "direct sidecar proxy source = $DIRECT_SOURCE"
fi
echo "ok   curl -x through order sidecar"

MTLS_SOURCE=$(curl -sk https://127.0.0.1:19101/users/e2e | json_field source)
if [ "$MTLS_SOURCE" != "anonymous" ]; then
  fail "direct inbound mTLS source = $MTLS_SOURCE"
fi
echo "ok   direct mTLS inbound (PERMISSIVE)"

echo "checking observability..."
curl -sS http://127.0.0.1:8080/metrics | grep -q 'gateway_http_requests_total' || fail "edge metrics"
curl -sS http://127.0.0.1:8080/metrics | grep -q 'mesh_requests_total' || fail "mesh metrics on edge"
curl -sS http://127.0.0.1:19203/metrics | grep -q 'mesh_requests_total' || fail "sidecar metrics"
curl -sS http://127.0.0.1:18000/metrics | grep -q 'mesh_registry_instances' || fail "control plane metrics"
curl -sS http://127.0.0.1:18000/v1/registry | grep -q 'user-service-v1-19101' || fail "registry snapshot"
curl -sS http://127.0.0.1:8080/api/routes -H "Authorization: Bearer $TOKEN" | grep -q '"endpoints"' || fail "routes API"

LOG_STREAM=$(curl -sS -N --max-time 2 "http://127.0.0.1:8080/api/logs/stream?access_token=$TOKEN" 2>/dev/null || true)
printf '%s' "$LOG_STREAM" | grep -q 'event: log' || fail "SSE log stream"
echo "ok   metrics, registry, routes, SSE logs"

echo "checking refresh rotation and logout revocation..."
REFRESH_RESPONSE=$(curl -fsS -X POST http://127.0.0.1:8080/auth/refresh \
  -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$REFRESH\"}")
NEW_TOKEN=$(printf '%s' "$REFRESH_RESPONSE" | json_field access_token)
NEW_REFRESH=$(printf '%s' "$REFRESH_RESPONSE" | json_field refresh_token)

check_code "old refresh token rejected" 401 "http://127.0.0.1:8080/auth/refresh" \
  -X POST -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$REFRESH\"}"

check_code "logout" 204 "http://127.0.0.1:8080/auth/logout" \
  -X POST -H "Authorization: Bearer $NEW_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$NEW_REFRESH\"}"

check_code "access token revoked after logout" 401 "http://127.0.0.1:8080/api/whoami" \
  -H "Authorization: Bearer $NEW_TOKEN"

echo
echo "E2E PASSED"
