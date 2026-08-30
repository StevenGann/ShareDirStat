#!/usr/bin/env bash
# Scan-throughput benchmark. Generates a synthetic tree, scans it with the
# real binary at several concurrency levels, and checks the result against
# the budget in NFR-1.
#
# Usage: hack/bench.sh [files] [min-files-per-second]
set -euo pipefail

FILES=${1:-200000}
MIN_RATE=${2:-15000}
TMP=$(mktemp -d)
trap 'kill "${PID:-0}" 2>/dev/null || true; rm -rf "$TMP"' EXIT

echo "== building"
CGO_ENABLED=0 go build -o "$TMP/sharedirstat" ./cmd/sharedirstat

echo "== generating a ${FILES}-file fixture"
go run ./hack/mkfixture -root "$TMP/shares/bench" -files "$FILES" -depth 3 -width 6 \
  -hardlinks 50 -wide 20000 -sparse -nonutf8 >/dev/null

PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])' 2>/dev/null || echo 18090)
status=0

for conc in 1 4 8; do
  rm -rf "$TMP/data"; mkdir -p "$TMP/data"
  SDS_DISCOVERY__ROOT="$TMP/shares" SDS_DATA_DIR="$TMP/data" SDS_SERVER__LISTEN="127.0.0.1:$PORT" \
    SDS_SCAN__ON_STARTUP=always SDS_SCAN__DEFAULT_CONCURRENCY="$conc" SDS_SCAN__DEFAULT_SCHEDULE= \
    "$TMP/sharedirstat" serve > "$TMP/log-$conc.json" 2>&1 &
  PID=$!
  for _ in $(seq 1 600); do
    grep -q '"scan completed"' "$TMP/log-$conc.json" 2>/dev/null && break
    sleep 0.2
  done
  peak_kb=$(grep VmHWM "/proc/$PID/status" 2>/dev/null | awk '{print $2}')
  kill "$PID"; wait "$PID" 2>/dev/null || true

  line=$(grep '"scan completed"' "$TMP/log-$conc.json" | head -1)
  if [ -z "$line" ]; then
    echo "concurrency=$conc: scan did not complete"; status=1; continue
  fi
  files=$(echo "$line" | sed -n 's/.*"files":\([0-9]*\).*/\1/p')
  ms=$(echo "$line" | sed -n 's/.*"duration_ms":\([0-9]*\).*/\1/p')
  rate=$(echo "$line" | sed -n 's/.*"files_per_s":\([0-9]*\).*/\1/p')
  snap=$(grep '"snapshot saved"' "$TMP/log-$conc.json" | head -1 | sed -n 's/.*"bytes":\([0-9]*\).*/\1/p')
  printf "concurrency=%d files=%s duration_ms=%s files_per_s=%s peak_rss_mb=%d snapshot_bytes_per_node=%s\n" \
    "$conc" "$files" "$ms" "$rate" "$(( ${peak_kb:-0} / 1024 ))" \
    "$(python3 -c "print(round(${snap:-0}/max(${files:-1},1),1))")"
  if [ "${rate:-0}" -lt "$MIN_RATE" ]; then
    echo "  FAIL: $rate files/s is below the NFR-1 budget of $MIN_RATE"; status=1
  fi
done
exit $status
