#!/bin/bash
# Assemble everything measured on this host into one report.
OUT=/root/bench/REPORT.md
{
  echo "# TCP Brutal in Caddy — measurements executed on this host"
  echo
  echo "- host: $(hostname)"
  echo "- kernel: $(uname -r)   cpus: $(nproc)   mem: $(free -m | awk '/Mem:/{print $2}') MB"
  echo "- module: tcp-brutal $(cat /sys/module/brutal/version 2>/dev/null)  ($(modinfo -n brutal 2>/dev/null))"
  echo "- caddy: $(/usr/local/bin/caddy version | head -1)"
  echo "- generated: $(date -u '+%Y-%m-%d %H:%M:%SZ')"
  echo
  echo "## 1. Functional suite (HTTP behaviour with brutal enabled)"
  echo
  grep -cE "PASS" /root/bench/functional.log | sed 's/^/checks passed: /'
  grep -E "passed:" /root/bench/functional.log
  echo
  echo "<details><summary>per-check output</summary>"
  echo
  echo '```'
  sed 's/\x1b\[[0-9;]*m//g' /root/bench/functional.log
  echo '```'
  echo
  echo "</details>"
  echo
  echo "## 2. Declared-rate accuracy (loopback)"
  echo
  sed -n '5,20p' /root/bench/rate-accuracy.md
  echo
  echo "## 3. Bandwidth utilisation matrix (shaped path inside a netns)"
  echo
  sed -n '6,40p' /root/bench/netns-matrix.md
  echo
  echo "## 4. Pacing proof"
  echo
  echo '```'
  echo "$(ss -tin state established 2>/dev/null | grep -c brutal) live sockets using the brutal algorithm right now"
  ss -tin state established 2>/dev/null | grep -A1 brutal | grep -oE 'brutal|pacing_rate [0-9]+bps|delivery_rate [0-9]+bps' | head -6
  echo '```'
} >"$OUT"
echo "written: $OUT ($(wc -l <"$OUT") lines)"
