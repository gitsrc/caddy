#!/bin/bash
# netns-matrix.sh — the whole comparison, executed entirely on this host.
#
# Builds a throwaway namespace with a 200 Mbit/s TBF bottleneck and netem
# delay/loss, runs Caddy inside it (port 9090 with the tcp_brutal wrapper and
# handler, port 9091 stock), and sweeps:
#   - the namespace's default congestion control: cubic and bbr
#   - sender-side loss
#   - the rate declared to TCP Brutal
#
# For every run it reports goodput, how much of the 200 Mbit/s bottleneck is
# filled with useful bytes, and the wire overhead (veth TX bytes vs useful bytes).
#
# Usage: sudo bash netns-matrix.sh            (writes /root/bench/netns-matrix.md)
set -uo pipefail

NS=caddyns
HOST_IP=10.220.0.1
NS_IP=10.220.0.2
BRUTAL_PORT=9090
PLAIN_PORT=9091
LINK=200
DELAY=15ms
SECS=8
WWW=${WWW:-/srv/www/files}
FILE=${FILE:-/med.bin}
CADDY=${CADDY:-/usr/local/bin/caddy}
OUT=${OUT:-/root/bench/netns-matrix.md}

mkdir -p "$(dirname "$OUT")"
cleanup() {
  ip netns pids "$NS" 2>/dev/null | xargs -r kill 2>/dev/null
  ip link del vcbench 2>/dev/null
  ip netns del "$NS" 2>/dev/null
}
trap cleanup EXIT
cleanup

ip netns add "$NS"
ip link add vcbench type veth peer name vcpeer
ip link set vcpeer netns "$NS"
ip addr add "$HOST_IP/24" dev vcbench && ip link set vcbench up
ip netns exec "$NS" ip addr add "$NS_IP/24" dev vcpeer
ip netns exec "$NS" ip link set vcpeer up
ip netns exec "$NS" ip link set lo up
ip netns exec "$NS" tc qdisc add dev vcpeer root handle 1: tbf rate "${LINK}mbit" burst 64kb limit 1mb
ip netns exec "$NS" tc qdisc add dev vcpeer parent 1:1 handle 10: netem delay "$DELAY" loss 0%
tc qdisc add dev vcbench root netem delay "$DELAY"

cat >/tmp/caddy-matrix.json <<EOF
{
  "admin": {"disabled": true},
  "logging": {"logs": {"default": {"level": "ERROR"}}},
  "apps": {"http": {"servers": {
    "brutal": {"listen": ["$NS_IP:$BRUTAL_PORT"],
      "listener_wrappers": [{"wrapper": "tcp_brutal", "rate": "180"}],
      "routes": [{"handle": [
        {"handler": "vars", "root": "$WWW"},
        {"handler": "tcp_brutal", "max": "600"},
        {"handler": "file_server"}]}]},
    "plain": {"listen": ["$NS_IP:$PLAIN_PORT"],
      "routes": [{"handle": [
        {"handler": "vars", "root": "$WWW"},
        {"handler": "file_server"}]}]}
  }}}
}
EOF

ip netns exec "$NS" env HOME=/root XDG_DATA_HOME=/tmp/caddy-ns-data XDG_CONFIG_HOME=/tmp/caddy-ns-cfg \
  "$CADDY" run --config /tmp/caddy-matrix.json >/tmp/caddy-matrix.log 2>&1 &
sleep 3
if ! ip netns exec "$NS" ss -tln | grep -q ":$BRUTAL_PORT"; then
  echo "caddy failed to start:"; tail -5 /tmp/caddy-matrix.log; exit 1
fi

set_loss() { ip netns exec "$NS" tc qdisc change dev vcpeer parent 1:1 handle 10: netem delay "$DELAY" loss "$1"; }
set_cc()   { ip netns exec "$NS" sysctl -qw "net.ipv4.tcp_congestion_control=$1"; }
tx()       { ip netns exec "$NS" cat /sys/class/net/vcpeer/statistics/tx_bytes; }

measure() { # $1=kind label, $2=url  -> "goodput util overhead" appended to RESULTS
  local label=$1 url=$2 b0 b1 t0 t1 bytes secs
  b0=$(tx); t0=$(date +%s%N)
  bytes=$(curl -s -o /dev/null --max-time "$SECS" -w '%{size_download}' "$url")
  t1=$(date +%s%N); b1=$(tx)
  secs=$(( (t1 - t0) / 1000000 ))
  awk -v l="$label" -v b="$bytes" -v w="$((b1 - b0))" -v ms="$secs" -v link="$LINK" 'BEGIN{
    s=ms/1000.0; g=b*8/1e6/s; util=g/link*100;
    ov=(b>0)?(w-b)/b*100:0; wire=w*8/1e6/s;
    printf "%s|%.1f|%.1f|%.1f|%.0f\n", l, g, util, wire, ov
  }' >>"$RESULTS"
  sleep 0.5
}

RESULTS=$(mktemp)
{
  echo "# TCP Brutal in Caddy — netns matrix"
  echo
  echo "Executed on: $(hostname) / $(uname -r) / module $(cat /sys/module/brutal/version 2>/dev/null || echo '?')"
  echo "Path: TBF ${LINK} Mbit/s, $((2 * ${DELAY%ms})) ms RTT, loss on Caddy's egress."
  echo "Caddy runs inside the namespace: :$BRUTAL_PORT brutal, :$PLAIN_PORT stock."
  echo
  echo "| scenario | loss | goodput (Mbit/s) | link utilisation | wire load (Mbit/s) | overhead vs useful |"
  echo "|---|---|---|---|---|---|"
} >"$OUT"

emit() { # $1 scenario, $2 loss, $3.. from measure
  local scenario=$1 loss=$2 line g u w o
  line=$(tail -1 "$RESULTS")
  IFS='|' read -r _ g u w o <<<"$line"
  printf '| %s | %s%% | %s | %s%% | %s | %s%% |\n' "$scenario" "$loss" "$g" "$u" "$w" "$o" >>"$OUT"
}

for loss in 0 0.5 2 5; do
  set_loss "$loss"

  for cc in cubic bbr; do
    set_cc "$cc"
    measure "stock-$cc" "http://$NS_IP:$PLAIN_PORT$FILE"
    emit "stock Caddy, ns CC = $cc" "$loss"
  done

  for rate in 120 180 260; do
    measure "brutal-$rate" "http://$NS_IP:$BRUTAL_PORT$FILE?brutal=$rate"
    emit "brutal, rate $rate" "$loss"
  done
done

echo >>"$OUT"
echo "goodput = useful bytes/s at the receiver; link utilisation = goodput / ${LINK} Mbit/s;" >>"$OUT"
echo "wire load = everything Caddy transmitted (retransmits included, from veth TX counters);" >>"$OUT"
echo "overhead = (wire bytes - useful bytes) / useful bytes." >>"$OUT"

echo "== written to $OUT =="
cat "$OUT"
rm -f "$RESULTS"
