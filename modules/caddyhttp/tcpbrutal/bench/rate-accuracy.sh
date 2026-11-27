#!/bin/bash
# Declared-rate accuracy, measured entirely on this host (loopback, no loss).
# Also verifies the socket really switched to brutal and paced at the target.
OUT=${OUT:-/root/bench/rate-accuracy.md}
DOMAIN=cs.ontoai.xyz
R="--resolve $DOMAIN:443:127.0.0.1"
{
  echo "# Declared-rate accuracy (loopback, no loss)"
  echo
  echo "Executed on: $(hostname) / $(uname -r) / module $(cat /sys/module/brutal/version 2>/dev/null || echo '?')"
  echo
  echo "| declared | delivered | accuracy | kernel pacing_rate |"
  echo "|---|---|---|---|"
} >"$OUT"

for rate in 20 50 100 150 200; do
  # one transfer per rate: measure its goodput and sample *its* socket
  rm -f /tmp/spd
  ( curl -s -o /dev/null --max-time 12 $R -H "X-Brutal-Down: $rate" \
      -w '%{speed_download}' "https://$DOMAIN/files/med.bin" > /tmp/spd ) &
  sleep 4
  # among brutal sockets on :443, take the one that has sent the most data
  pace=$(ss -tin 2>/dev/null | awk '
    /^[A-Z]/ { state=$0; brutal=0; pace=""; sent=0; next }
    /brutal/ { brutal=1 }
    /pacing_rate/ {
      for (i=1;i<=NF;i++) {
        if ($i=="pacing_rate") { v=$(i+1); sub(/bps$/,"",v); pace=v }
        if ($i ~ /^bytes_sent:/) { split($i,a,":"); sent=a[2] }
      }
      if (brutal && state ~ /:443 / && sent+0 > best) { best=sent+0; bestpace=pace }
    }
    END { if (bestpace != "") printf "%.0f Mbit/s", bestpace/1e6; else printf "n/a" }')
  wait
  got=$(cat /tmp/spd 2>/dev/null || echo 0)
  awk -v r="$rate" -v s="$got" -v p="$pace" 'BEGIN{
    d=s*8/1e6; acc=(r>0)?d/r*100:0;
    printf "| %d Mbit/s | %.1f Mbit/s | %.1f%% | %s |\n", r, d, acc, p
  }' >>"$OUT"
done

echo >>"$OUT"
echo "Loopback has no loss, so this isolates the pacing accuracy from path effects;" >>"$OUT"
echo "pacing_rate is read from ss while a declared-rate transfer is in flight." >>"$OUT"
cat "$OUT"
