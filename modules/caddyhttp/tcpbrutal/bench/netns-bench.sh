#!/bin/bash
# bench/netns-bench.sh — compare Caddy+TCP Brutal against a stock Caddy listener
# on a shaped, lossy path, without touching the host's real interfaces.
#
# Creates a throwaway netns with a veth pair, puts a TBF bottleneck plus netem
# delay/loss on Caddy's egress, runs Caddy inside the namespace serving two
# ports (:9090 with the tcp_brutal wrapper+handler, :9091 stock), and sweeps
# sender-side loss. Cleans up on exit.
#
# Requirements: root, iproute2 (tc), curl, a Caddy binary built with this module,
#                the tcp-brutal kernel module, and two files to serve.
#
#   sudo WWW=/srv/bench FILE=/med.bin SMALL=/small.bin CADDY=$(which caddy) \
#        LOSSES="0 0.5 2 5 10" bash modules/caddyhttp/tcpbrutal/bench/netns-bench.sh
#
# The namespace default congestion control is set to cubic on purpose: that is
# what most hosts still run, and it is the baseline brutal is meant to beat.
set -uo pipefail

NS=caddyns
HOST_IP=10.220.0.1
NS_IP=10.220.0.2
BRUTAL_PORT=9090
PLAIN_PORT=9091
LINK=200          # Mbit/s bottleneck on the sender (Caddy's) egress
RATE=180          # Mbit/s brutal target
DELAY=15ms        # one way; RTT = 2 x DELAY
FILE=${FILE:-/med.bin}
WWW=${WWW:-/root/www}
CADDY=${CADDY:-$(command -v caddy)}

cleanup() {
  ip netns pids "$NS" 2>/dev/null | xargs -r kill 2>/dev/null
  ip link del vcbench 2>/dev/null
  ip netns del "$NS" 2>/dev/null
  echo flush >/proc/net/tcp_brutal/rules 2>/dev/null
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

# TCP Brutal is interesting precisely because most hosts still default to a
# loss-based algorithm: make the plain port a fair "typical VPS" baseline.
ip netns exec "$NS" sysctl -qw net.ipv4.tcp_congestion_control=cubic
echo "namespace default CC: $(ip netns exec "$NS" cat /proc/sys/net/ipv4/tcp_congestion_control)"

# The sender is Caddy, inside the namespace: shape its egress (vcpeer).
ip netns exec "$NS" tc qdisc add dev vcpeer root handle 1: tbf rate "${LINK}mbit" burst 64kb limit 1mb
ip netns exec "$NS" tc qdisc add dev vcpeer parent 1:1 handle 10: netem delay "$DELAY" loss 0%
# and the reverse direction just gets the delay
tc qdisc add dev vcbench root netem delay "$DELAY"

cat >/tmp/caddy-netns.json <<EOF
{
  "admin": {"disabled": true},
  "apps": {
    "http": {
      "servers": {
        "brutal": {
          "listen": ["$NS_IP:$BRUTAL_PORT"],
          "listener_wrappers": [{"wrapper": "tcp_brutal", "rate": "$RATE"}],
          "routes": [{"handle": [
            {"handler": "vars", "root": "$WWW"},
            {"handler": "tcp_brutal", "max": "500"},
            {"handler": "file_server"}
          ]}]
        },
        "plain": {
          "listen": ["$NS_IP:$PLAIN_PORT"],
          "routes": [{"handle": [
            {"handler": "vars", "root": "$WWW"},
            {"handler": "file_server"}
          ]}]
        }
      }
    }
  }
}
EOF

ip netns exec "$NS" env HOME=/root XDG_CONFIG_HOME=/tmp/caddy-ns-config \
  "$CADDY" run --config /tmp/caddy-netns.json >/tmp/caddy-netns.log 2>&1 &
sleep 3
if ! ip netns exec "$NS" ss -tln | grep -q ":$BRUTAL_PORT"; then
  echo "caddy failed to start inside the namespace:"; tail -5 /tmp/caddy-netns.log; exit 1
fi

echo "caddy+brutal : $NS_IP:$BRUTAL_PORT  (wrapper rate $RATE Mbit/s, per-IP group, handler on)"
echo "caddy plain  : $NS_IP:$PLAIN_PORT  (stock file_server)"
echo "path         : TBF $LINK Mbit/s + $((2 * ${DELAY%ms}))ms RTT, loss on Caddy's egress"
echo
printf '%-6s %-18s %-18s %-10s\n' "loss" "brutal (Mbit/s)" "plain/cubic (Mbit/s)" "gain"
printf '%-6s %-18s %-18s %-10s\n' "----" "---------------" "------------------" "----"

mbps() { awk '{printf "%.1f", $1*8/1e6}'; }

for loss in ${LOSSES:-"0 0.5 2 5 10"}; do
  ip netns exec "$NS" tc qdisc change dev vcpeer parent 1:1 handle 10: netem delay "$DELAY" loss "${loss}%"
  br=$(curl -s -o /dev/null --max-time 25 -w "%{speed_download}" "http://$NS_IP:$BRUTAL_PORT$FILE" | mbps)
  pl=$(curl -s -o /dev/null --max-time 25 -w "%{speed_download}" "http://$NS_IP:$PLAIN_PORT$FILE" | mbps)
  awk -v l="$loss%" -v b="$br" -v p="$pl" 'BEGIN{
    printf "%-6s %-18s %-18s x%.2f\n", l, b, p, (p+0 > 0 ? (b+0)/(p+0) : 0)
  }'
  sleep 1
done

echo
echo "== declared-rate negotiation: client sends X-Brutal-Down, handler retunes the socket =="
for r in 20 40 100; do
  got=$(curl -s -o /dev/null --max-time 20 -H "X-Brutal-Down: $r" -w "%{speed_download}" \
        "http://$NS_IP:$BRUTAL_PORT${SMALL:-/small.bin}" | mbps)
  printf "  client declares %3d Mbit/s -> delivered %s Mbit/s\n" "$r" "$got"
done

echo
echo "== socket state during a brutal transfer =="
curl -s -o /dev/null --max-time 5 "http://$NS_IP:$BRUTAL_PORT$FILE" &
sleep 2
ip netns exec "$NS" ss -tin dst "$HOST_IP" 2>/dev/null | grep -oE "brutal|pacing_rate [0-9]+bps" | head -3
wait 2>/dev/null
echo
echo "== regression check: plain port still uses the host default CC =="
curl -s -o /dev/null --max-time 4 "http://$NS_IP:$PLAIN_PORT$FILE" &
sleep 2
ip netns exec "$NS" ss -tin dst "$HOST_IP" 2>/dev/null | grep -oE "^\s+(bbr|cubic|brutal)|bbr|pacing_rate [0-9]+bps" | head -3
wait 2>/dev/null
