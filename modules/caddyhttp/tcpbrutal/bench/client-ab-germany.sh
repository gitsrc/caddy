#!/bin/bash
# Germany -> VPS: repeated interleaved A/B, single stream and 4 streams.
set -u
IP=185.148.13.138
one() { curl -s -o /dev/null --max-time "$2" -w '%{size_download} %{speed_download}' "http://$IP:$1/med.bin"; }
setrate() { python3 /tmp/rssh.py "bash /root/start_servers.sh $1 >/dev/null 2>&1; echo ok" 60 >/dev/null 2>&1; sleep 1; }
mbps() { awk -v s="$1" 'BEGIN{printf "%.1f", s*8/1e6}'; }

echo "=== A. single stream, 30 s runs, 3 interleaved pairs ==="
setrate 300
printf '%-8s %-16s %-16s %s\n' pair "plain :9551" "brutal@300 :9550" delta
for i in 1 2 3; do
  if (( i % 2 )); then
    p=$(one 9551 30 | awk '{print $2}'); b=$(one 9550 30 | awk '{print $2}')
  else
    b=$(one 9550 30 | awk '{print $2}'); p=$(one 9551 30 | awk '{print $2}')
  fi
  awk -v i="$i" -v p="$p" -v b="$b" 'BEGIN{
    printf "%-8s %-16s %-16s %+.1f%%\n", "pair"i, sprintf("%.1f Mbit/s", p*8/1e6), sprintf("%.1f Mbit/s", b*8/1e6), (p>0)?(b-p)/p*100:0 }'
  echo "$p $b" >> /tmp/a_single.txt
done

echo
echo "=== B. 4 parallel streams, 20 s runs, 4 interleaved pairs ==="
setrate 400
agg() { # $1 port -> bytes
  rm -f /tmp/o.*; local port=$1
  for j in 1 2 3 4; do curl -s -o /dev/null --max-time 20 -w '%{size_download}\n' "http://$IP:$port/med.bin" > /tmp/o.$j & done
  wait; cat /tmp/o.* | awk '{s+=$1} END{print s+0}'; rm -f /tmp/o.*
}
printf '%-8s %-16s %-16s %s\n' pair "plain :9551" "brutal@400 :9550" delta
for i in 1 2 3 4; do
  if (( i % 2 )); then
    p=$(agg 9551); b=$(agg 9550)
  else
    b=$(agg 9550); p=$(agg 9551)
  fi
  awk -v i="$i" -v p="$p" -v b="$b" 'BEGIN{
    printf "%-8s %-16s %-16s %+.1f%%\n", "pair"i, sprintf("%.0f Mbit/s", p*8/1e6/20), sprintf("%.0f Mbit/s", b*8/1e6/20), (p>0)?(b-p)/p*100:0 }'
  echo "$p $b" >> /tmp/b_multi.txt
done

echo
awk '{p+=$1; b+=$2; n++} END{printf "A mean: plain %.0f Mbit/s, brutal %.0f Mbit/s, %+.1f%%\n", p*8/1e6/n, b*8/1e6/n, (b-p)/p*100}' /tmp/a_single.txt
awk '{p+=$1; b+=$2; n++} END{printf "B mean: plain %.0f Mbit/s, brutal %.0f Mbit/s, %+.1f%%\n", p*8/1e6/20/n, b*8/1e6/20/n, (b-p)/p*100}' /tmp/b_multi.txt
