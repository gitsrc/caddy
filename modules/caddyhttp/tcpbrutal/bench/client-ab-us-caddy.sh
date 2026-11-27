#!/bin/bash
# Germany (Frankfurt) -> San Jose, US. Modified Caddy only, NO domain lookup:
# everything is pinned to the origin IP with --resolve (SNI/Host = cs.ontoai.xyz),
# so Cloudflare is out of the path.
set -u
IP=185.148.13.138
D=cs.ontoai.xyz
RB="--resolve $D:443:$IP"      # brutal listener (:443, tcp_brutal)
RP="--resolve $D:8443:$IP"     # stock listener (:8443, host default bbr)
F=/files/big2.bin
SECS=${SECS:-20}

single() { # $1=B|P
  if [[ $1 == B ]]; then
    curl -s -o /dev/null --max-time "$SECS" $RB -H 'X-Brutal-Down: 500' -w '%{size_download} %{speed_download}' "https://$D$F"
  else
    curl -s -o /dev/null --max-time "$SECS" $RP -w '%{size_download} %{speed_download}' "https://$D:8443$F"
  fi
}
agg() { # $1=B|P, $2=streams
  local mode=$1 n=$2
  rm -f /tmp/agg.*
  for j in $(seq 1 "$n"); do
    if [[ $mode == B ]]; then
      curl -s -o /dev/null --max-time "$SECS" $RB -H 'X-Brutal-Down: 500' -w '%{size_download}\n' "https://$D$F" > /tmp/agg.$j &
    else
      curl -s -o /dev/null --max-time "$SECS" $RP -w '%{size_download}\n' "https://$D:8443$F" > /tmp/agg.$j &
    fi
  done
  wait
  cat /tmp/agg.* | awk '{s+=$1} END{print s+0}'; rm -f /tmp/agg.*
}

echo "client: $(curl -s --max-time 5 https://api.ipify.org) (Frankfurt, DE)"
echo "server: $IP (San Jose, US, AS41378)   pinned by --resolve, no DNS/Cloudflare"
echo "RTT: $(ping -c 6 -i 0.3 -W 2 $IP 2>/dev/null | tail -1 | awk -F'/' '{print $5}') ms"
echo "files: 2 GB per stream, ${SECS}s windows"
echo

echo "=== A. single stream (h2 over TLS), 4 interleaved pairs ==="
printf '%-8s %-18s %-18s %s\n' pair "stock :8443 / bbr" "brutal :443 / 500M" delta
: > /tmp/us_single.txt
for i in 1 2 3 4; do
  if (( i % 2 )); then
    p=$(single P | awk '{print $2}'); b=$(single B | awk '{print $2}')
  else
    b=$(single B | awk '{print $2}'); p=$(single P | awk '{print $2}')
  fi
  awk -v i="$i" -v p="$p" -v b="$b" 'BEGIN{printf "pair%-4s %-18s %-18s %+.1f%%\n", i, sprintf("%.1f Mbit/s", p*8/1e6), sprintf("%.1f Mbit/s", b*8/1e6), (p>0)?(b-p)/p*100:0}'
  echo "$p $b" >> /tmp/us_single.txt
done

echo
echo "=== B. 4 parallel streams, 3 interleaved pairs ==="
printf '%-8s %-18s %-18s %s\n' pair "stock :8443 / bbr" "brutal :443 / 500M" delta
: > /tmp/us_multi.txt
for i in 1 2 3; do
  if (( i % 2 )); then
    p=$(agg P 4); b=$(agg B 4)
  else
    b=$(agg B 4); p=$(agg P 4)
  fi
  awk -v i="$i" -v p="$p" -v b="$b" -v s="$SECS" 'BEGIN{
    printf "pair%-4s %-18s %-18s %+.1f%%\n", i, sprintf("%.0f Mbit/s", p*8/1e6/s), sprintf("%.0f Mbit/s", b*8/1e6/s), (p>0)?(b-p)/p*100:0}'
  echo "$p $b" >> /tmp/us_multi.txt
done

echo
awk -v s="$SECS" '{p+=$1;b+=$2;n++} END{printf "A mean : stock %.0f Mbit/s (%s), brutal %.0f Mbit/s (%s), %+.1f%%\n", p*8/1e6/n, "single", b*8/1e6/n, "single", (b-p)/p*100}' /tmp/us_single.txt
awk -v s="$SECS" '{p+=$1;b+=$2;n++} END{printf "B mean : stock %.0f Mbit/s (4 streams), brutal %.0f Mbit/s (4 streams), %+.1f%%\n", p*8/1e6/s/n, b*8/1e6/s/n, (b-p)/p*100}' /tmp/us_multi.txt
