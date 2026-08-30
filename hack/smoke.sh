#!/usr/bin/env bash
# Smoke test: start the binary against a temporary share, run a real scan,
# exercise the read APIs, then stop. Used by CI and `make smoke`.
# Usage: hack/smoke.sh path/to/sharedirstat
set -euo pipefail

BIN=${1:-bin/sharedirstat}
TMP=$(mktemp -d)
trap 'kill "${PID:-0}" 2>/dev/null || true; rm -rf "$TMP"' EXIT

mkdir -p "$TMP/shares/smoke/sub" "$TMP/data"
head -c 4096 /dev/zero > "$TMP/shares/smoke/big.mkv"
head -c 128  /dev/zero > "$TMP/shares/smoke/sub/small.txt"
ln -s big.mkv "$TMP/shares/smoke/link"

PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])' 2>/dev/null || echo 18080)
BASE="http://127.0.0.1:$PORT"

SDS_DISCOVERY__ROOT="$TMP/shares" SDS_DATA_DIR="$TMP/data" SDS_SERVER__LISTEN="127.0.0.1:$PORT" \
  SDS_SCAN__ON_STARTUP=always SDS_SCAN__DEFAULT_SCHEDULE= SDS_LOG__FORMAT=text \
  "$BIN" serve > "$TMP/server.log" 2>&1 &
PID=$!

fail() { echo "SMOKE FAIL: $*" >&2; echo "--- server log ---" >&2; cat "$TMP/server.log" >&2; exit 1; }
get() { curl -fsS "$BASE$1"; }

for _ in $(seq 1 100); do
  "$BIN" healthcheck --listen "127.0.0.1:$PORT" 2>/dev/null && break
  sleep 0.1
done
"$BIN" healthcheck --listen "127.0.0.1:$PORT" || fail "healthcheck never passed"

get /readyz | grep -q '"ready"'            || fail "readyz"
get /api/v1/shares | grep -q '"id":"smoke"' || fail "share not discovered"
get /api/v1/version | grep -q '"version"'   || fail "version"
get /metrics | grep -q 'sharedirstat_build_info' || fail "metrics"
get / | grep -qi 'sharedirstat'             || fail "ui root"

# Mutating requests must be refused without the CSRF header (FR-SEC-02).
code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/shares/smoke/scan")
[ "$code" = "403" ] || fail "CSRF not enforced (got $code)"

# Wait for the startup scan to publish results.
for _ in $(seq 1 100); do
  get /api/v1/shares/smoke | grep -q '"state":"ready"' && break
  sleep 0.1
done
get /api/v1/shares/smoke | grep -q '"state":"ready"' || fail "startup scan did not complete"

# 4096 + 128 + the symlink target's length (7).
get /api/v1/shares/smoke | grep -q '"size":4231' || fail "unexpected total size"
get "/api/v1/shares/smoke/tree" | grep -q '"name":"big.mkv"' || fail "tree listing"
get "/api/v1/shares/smoke/tree?path=sub" | grep -q '"name":"small.txt"' || fail "nested tree listing"
get "/api/v1/shares/smoke/treemap" | grep -q '"root"' || fail "treemap"
get "/api/v1/shares/smoke/top?n=1" | grep -q '"name":"big.mkv"' || fail "top files"
get "/api/v1/shares/smoke/extensions" | grep -q '"mkv"' || fail "extensions"
get "/api/v1/shares/smoke/search?q=small" | grep -q '"name":"small.txt"' || fail "search"
get "/api/v1/shares/smoke/errors" | grep -q '"total":0' || fail "errors list"
get "/api/v1/shares/smoke/scans" | grep -q '"outcome":"completed"' || fail "scan history"
get /api/v1/scans | grep -q '"scans"' || fail "running scans"

# A manual rescan must be accepted with the CSRF header.
code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
  -H 'X-Requested-With: ShareDirStat' -H 'Content-Type: application/json' \
  -d '{}' "$BASE/api/v1/shares/smoke/scan")
[ "$code" = "202" ] || fail "manual scan rejected (got $code)"

# The event stream must speak text/event-stream. curl is stopped by the
# timeout rather than by the server, so the headers are captured to a file
# (they would otherwise be lost when curl is killed mid-stream).
curl -sS -m 2 -D "$TMP/sse.headers" -o /dev/null "$BASE/api/v1/events" >/dev/null 2>&1 || true
grep -qi 'content-type: text/event-stream' "$TMP/sse.headers" || fail "SSE stream did not open"

# Results must survive a restart (snapshot round trip).
kill "$PID"; wait "$PID" 2>/dev/null || true
SDS_DISCOVERY__ROOT="$TMP/shares" SDS_DATA_DIR="$TMP/data" SDS_SERVER__LISTEN="127.0.0.1:$PORT" \
  SDS_SCAN__ON_STARTUP=never SDS_SCAN__DEFAULT_SCHEDULE= SDS_LOG__FORMAT=text \
  "$BIN" serve > "$TMP/server2.log" 2>&1 &
PID=$!
for _ in $(seq 1 100); do
  curl -fsS "$BASE/readyz" >/dev/null 2>&1 && break
  sleep 0.1
done
get /api/v1/shares/smoke | grep -q '"size":4231' || fail "results did not survive a restart"
get "/api/v1/shares/smoke/tree" | grep -q '"name":"big.mkv"' || fail "tree missing after restart"

kill "$PID"; wait "$PID" 2>/dev/null || true
echo "SMOKE OK"
