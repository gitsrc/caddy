#!/bin/bash
# run-all.sh — run the whole measurement harness on the host that runs Caddy.
#
# Everything runs locally on this machine: the functional suite talks to
# 127.0.0.1 (plus the public DNS/CDN path), the rate-accuracy test uses the
# loopback listener, and the utilisation matrix uses a throwaway network
# namespace so the shaped path does not touch real interfaces.
#
#   sudo bash bench/run-all.sh
#
# Output: /root/bench/{functional.log,rate-accuracy.md,netns-matrix.md,REPORT.md}
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
OUT=${OUT:-/root/bench}
mkdir -p "$OUT"

echo "== 1/3 functional suite =="
bash "$HERE/../test/functional-test.sh" >"$OUT/functional.log" 2>&1
echo "   $(grep -E 'passed:' "$OUT/functional.log" | tail -1)"

echo "== 2/3 declared-rate accuracy =="
OUT="$OUT/rate-accuracy.md" bash "$HERE/rate-accuracy.sh" >/dev/null 2>&1
echo "   written $OUT/rate-accuracy.md"

echo "== 3/3 utilisation matrix (netns, ~4 min) =="
OUT="$OUT/netns-matrix.md" bash "$HERE/netns-matrix.sh" >/dev/null 2>&1
echo "   written $OUT/netns-matrix.md"

echo "== assembling report =="
bash "$HERE/make-report.sh"
