#!/usr/bin/env bash
# Smoke test: start the binary against a temporary share, run a real scan,
# exercise the read APIs, then stop. Used by CI and `make smoke`.
# Usage: hack/smoke.sh path/to/sharedirstat
set -euo pipefail

BIN=${1:-bin/sharedirstat}
TMP=$(mktemp -d)
trap 'if [ -n "${PID:-}" ]; then kill "$PID" 2>/dev/null || true; fi; rm -rf "$TMP"' EXIT

mkdir -p "$TMP/shares/smoke/sub" "$TMP/data"
head -c 4096 /dev/zero > "$TMP/shares/smoke/big.mkv"
head -c 128  /dev/zero > "$TMP/shares/smoke/sub/small.txt"
ln -s big.mkv "$TMP/shares/smoke/link"

PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])' 2>/dev/null || echo 18080)
BASE="http://127.0.0.1:$PORT"

SDS_DISCOVERY__ROOT="$TMP/shares" SDS_DATA_DIR="$TMP/data" SDS_SERVER__LISTEN="127.0.0.1:$PORT" \
  SDS_DISCOVERY__ALLOW_DELETE=true \
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

post() { curl -sS -o "$2" -w '%{http_code}' -X POST \
  -H 'X-Requested-With: ShareDirStat' -H 'Content-Type: application/json' \
  -d "$3" "$BASE$1"; }

# A single file downloads, and byte ranges work (FR-DL-01).
get "/api/v1/shares/smoke/download?path=sub/small.txt" > "$TMP/dl" || fail "download"
[ "$(wc -c < "$TMP/dl")" = "128" ] || fail "download length"
curl -fsS -r 0-9 "$BASE/api/v1/shares/smoke/download?path=sub/small.txt" > "$TMP/dlr"
[ "$(wc -c < "$TMP/dlr")" = "10" ] || fail "range request"

# Path traversal is refused on every path-taking endpoint.
for p in "../../etc/passwd" "/etc/passwd" "sub/../../etc/passwd"; do
  code=$(curl -sS -o /dev/null -w '%{http_code}' --get --data-urlencode "path=$p" \
    "$BASE/api/v1/shares/smoke/download")
  [ "$code" = "400" ] || [ "$code" = "404" ] || fail "traversal $p not refused (got $code)"
done

# A ZIP of the share extracts to the files we put there.
get "/api/v1/shares/smoke/download/zip?path=sub" > "$TMP/out.zip" || fail "zip"
if command -v unzip >/dev/null 2>&1; then
  unzip -qq -o "$TMP/out.zip" -d "$TMP/unz" || fail "zip is not extractable"
  extracted=$(find "$TMP/unz" -name small.txt -type f | head -1)
  [ -n "$extracted" ] || fail "zip missing small.txt"
  [ "$(wc -c < "$extracted")" = "128" ] || fail "zip content differs"
  # No entry may escape the extraction directory.
  unzip -l "$TMP/out.zip" | grep -qE '\.\.[/\\]' && fail "zip entry escapes"
fi

# Delete: preview issues a token bound to the path list, the token deletes,
# and it cannot be replayed (FR-DEL-02).
code=$(post "/api/v1/shares/smoke/delete/preview" "$TMP/prev" '{"paths":["sub/small.txt"]}')
[ "$code" = "200" ] || fail "delete preview refused (got $code); $(cat "$TMP/prev")"
grep -q '"confirm"' "$TMP/prev" || fail "preview returned no token"
TOKEN=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["confirm"])' "$TMP/prev")
code=$(post "/api/v1/shares/smoke/delete" "$TMP/del" "{\"paths\":[\"sub/small.txt\"],\"confirm\":\"$TOKEN\"}")
[ "$code" = "200" ] || fail "delete refused (got $code); $(cat "$TMP/del")"
[ -f "$TMP/shares/smoke/sub/small.txt" ] && fail "file still on disk after delete"
code=$(post "/api/v1/shares/smoke/delete" "$TMP/del2" "{\"paths\":[\"sub/small.txt\"],\"confirm\":\"$TOKEN\"}")
[ "$code" = "403" ] || fail "delete token was replayable (got $code)"

# The deletion is audited (FR-DEL-06).
get "/api/v1/audit/deletes" | grep -q 'small.txt' || fail "delete not audited"

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
get /api/v1/shares/smoke | grep -q '"size":4103' || fail "results did not survive a restart"
get "/api/v1/shares/smoke/tree" | grep -q '"name":"big.mkv"' || fail "tree missing after restart"

kill "$PID"; wait "$PID" 2>/dev/null || true
echo "SMOKE OK"
