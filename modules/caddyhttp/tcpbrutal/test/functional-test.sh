#!/bin/bash
# Functional test suite for the Caddy + TCP Brutal deployment.
# Run from a machine that can reach the origin (directly and/or via Cloudflare).
#
#   bash functional_test.sh
#
# Every check prints PASS/FAIL; the exit code is the number of failures.

ORIGIN_IP=${ORIGIN_IP:-127.0.0.1}
DOMAIN=${DOMAIN:-cs.ontoai.xyz}
R="--resolve $DOMAIN:443:$ORIGIN_IP --resolve $DOMAIN:8443:$ORIGIN_IP"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

pass=0; fail=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s  (%s)\n' "$1" "$2"; fail=$((fail+1)); }
check() { # $1 name, $2 expected, $3 actual
  if [[ "$3" == "$2" ]]; then ok "$1"; else bad "$1" "want '$2' got '$3'"; fi
}
check_ne() {
  if [[ "$3" != "$2" ]]; then ok "$1"; else bad "$1" "must not be '$2'"; fi
}
check_in() { # $1 name, $2 needle, $3 haystack
  if [[ "$3" == *"$2"* ]]; then ok "$1"; else bad "$1" "'$2' not in output"; fi
}

echo "== 1. HTTP → HTTPS redirect (port 80, direct) =="
out=$(curl -s -o /dev/null -D- "http://$ORIGIN_IP/health" -H "Host: $DOMAIN" --max-time 10 | tr -d '\r')
check "status 308" "HTTP/1.1 308 Permanent Redirect" "$(echo "$out" | head -1)"
check_in "Location points at https" "location: https://$DOMAIN/health" "$(echo "$out" | tr 'A-Z' 'a-z' | grep '^location:')"

echo "== 2. HTTPS basics (direct to origin) =="
code=$(curl -s -o "$TMP/health" -w '%{http_code}' --max-time 10 $R "https://$DOMAIN/health")
check "GET /health status" "200" "$code"
check "GET /health body" "ORIGIN-185" "$(cat "$TMP/health")"
hdr=$(curl -s -o /dev/null -D- --max-time 10 $R "https://$DOMAIN/health" | tr -d '\r')
check_in "Strict-Transport-Security" "strict-transport-security: max-age=31536000" "$(echo "$hdr" | tr 'A-Z' 'a-z')"
check_in "X-Served-By header" "x-served-by: caddy-brutal" "$(echo "$hdr" | tr 'A-Z' 'a-z')"
check "no Server header" "0" "$(echo "$hdr" | grep -ci '^server:' || true)"

echo "== 3. static files =="
code=$(curl -s -o "$TMP/index" -w '%{http_code}' --max-time 10 $R "https://$DOMAIN/")
check "GET / (index.html)" "200" "$code"
check_in "index.html content" "caddy.listeners.tcp_brutal" "$(cat "$TMP/index")"
code=$(curl -s -o "$TMP/dir" -w '%{http_code}' --max-time 10 $R "https://$DOMAIN/files/")
check "GET /files/ (directory browse)" "200" "$code"
check_in "directory listing" "small.bin" "$(cat "$TMP/dir")"

echo "== 4. full-file integrity (60 MB, pacé par brutal) =="
code=$(curl -s -o "$TMP/small.bin" -w '%{http_code}' --max-time 90 $R \
        -H 'X-Brutal-Down: 200' "https://$DOMAIN/files/small.bin")
check "download status" "200" "$code"
check "size" "60000000" "$(stat -c%s "$TMP/small.bin")"
check "sha256 matches origin" \
  "1dd28892ddb49efc547c120b882f8e44e99ed2eaac24959108808d5a34e954aa" \
  "$(sha256sum "$TMP/small.bin" | cut -d' ' -f1)"
check_in "no Server header on download" "" "$(curl -s -o /dev/null -D- --max-time 30 $R "https://$DOMAIN/files/small.bin" -r 0-1 | grep -i '^server:' || true)"

echo "== 5. range requests =="
code=$(curl -s -r 0-1023 -o "$TMP/part" -w '%{http_code}' --max-time 15 $R "https://$DOMAIN/files/small.bin")
check "range status (206)" "206" "$code"
check "range size (1024)" "1024" "$(stat -c%s "$TMP/part")"
code=$(curl -s -r 1000-1999 -o "$TMP/part2" -w '%{http_code}' --max-time 15 $R "https://$DOMAIN/files/small.bin")
check "second range status" "206" "$code"

echo "== 6. HEAD and conditional requests =="
check "HEAD status" "200" "$(curl -s -I -o /dev/null -w '%{http_code}' --max-time 10 $R "https://$DOMAIN/files/text.txt")"
len=$(curl -s -I --max-time 10 $R "https://$DOMAIN/files/text.txt" | tr -d '\r' | awk -F': ' 'tolower($1)=="content-length"{print $2}')
check "HEAD content-length" "17400" "$len"
etag=$(curl -s -I --max-time 10 $R "https://$DOMAIN/files/text.txt" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{print $2}')
if [[ -n "$etag" ]]; then
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 $R -H "If-None-Match: $etag" "https://$DOMAIN/files/text.txt")
  check "conditional GET (304)" "304" "$code"
else
  bad "conditional GET (304)" "no ETag advertised"
fi

echo "== 7. compression =="
enc=$(curl -s -o /dev/null -D- --max-time 10 $R -H 'Accept-Encoding: gzip' "https://$DOMAIN/files/text.txt" | tr -d '\r' | tr 'A-Z' 'a-z' | awk -F': ' '/^content-encoding/{print $2}')
check "gzip negotiated" "gzip" "$enc"
enc=$(curl -s -o /dev/null -D- --max-time 10 $R -H 'Accept-Encoding: zstd' "https://$DOMAIN/files/text.txt" | tr -d '\r' | tr 'A-Z' 'a-z' | awk -F': ' '/^content-encoding/{print $2}')
check "zstd negotiated" "zstd" "$enc"

echo "== 8. reverse_proxy =="
out=$(curl -s --max-time 15 $R "https://$DOMAIN/api/info?x=1")
check_in "backend reached" '"backend": "python3"' "$out"
check_in "query forwarded" '"query": "x=1"' "$out"
check_in "Host forwarded" "\"host\": \"$DOMAIN\"" "$out"
check_in "X-Forwarded-Proto" '"proto": "https"' "$out"
check_ne "X-Forwarded-For present" 'null' "$(echo "$out" | awk -F'"' '/"xff"/{print $4}')"
printf 'hello brutal proxy\n' > "$TMP/post.txt"
want=$(sha256sum "$TMP/post.txt" | cut -d' ' -f1)
out=$(curl -s --max-time 15 $R -X POST --data-binary @"$TMP/post.txt" "https://$DOMAIN/api/echo")
check_in "POST body reached backend intact" "$want" "$out"

echo "== 9. error handling =="
code=$(curl -s -o "$TMP/404" -w '%{http_code}' --max-time 10 $R "https://$DOMAIN/does-not-exist")
check "404 status" "404" "$code"
check_in "custom error page" "HTTP 404" "$(cat "$TMP/404")"

echo "== 10. protocol negotiation and keep-alive =="
v=$(curl -s -o /dev/null -w '%{http_version}' --http2 --max-time 10 $R "https://$DOMAIN/health")
check "HTTP/2 negotiated" "2" "$v"
v=$(curl -s -o /dev/null -w '%{http_version}' --http1.1 --max-time 10 $R "https://$DOMAIN/health")
check "HTTP/1.1 forced" "1.1" "$v"
total=$(curl -s -o /dev/null -w '%{num_connects}\n' --http2 --max-time 10 $R \
  "https://$DOMAIN/health" "https://$DOMAIN/api/info" "https://$DOMAIN/files/text.txt" \
  | awk '{s+=$1} END {printf "%d", s}')
check "connection reuse across 3 requests" "1" "$total"

echo "== 11. TCP Brutal is actually pacing =="
for rate in 40 80; do
  got=$(curl -s -o /dev/null --max-time 20 $R -H "X-Brutal-Down: $rate" \
        -w '%{speed_download}' "https://$DOMAIN/files/med.bin")
  mbps=$(awk -v s="$got" 'BEGIN{printf "%.1f", s*8/1e6}')
  within=$(awk -v g="$mbps" -v r="$rate" 'BEGIN{print (g > r*0.5 && g < r*1.15) ? "yes" : "no"}')
  check "declared ${rate} Mbit/s respected (got ${mbps} Mbit/s)" "yes" "$within"
done
got=$(curl -s -o /dev/null --max-time 20 $R -w '%{speed_download}' "https://$DOMAIN/files/med.bin?brutal=60")
mbps=$(awk -v s="$got" 'BEGIN{printf "%.1f", s*8/1e6}')
check "?brutal=60 query form respected (got ${mbps} Mbit/s)" "yes" \
  "$(awk -v g="$mbps" 'BEGIN{print (g > 30 && g < 70) ? "yes" : "no"}')"

echo "== 12. stock listener for comparison =="
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 $R "https://$DOMAIN:8443/health")
check "plain :8443 serves" "200" "$code"

echo "== 13. through the public DNS/CDN path =="
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 "https://$DOMAIN/health")
check "CF: /health" "200" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 "https://$DOMAIN/api/info")
check "CF: /api/info" "200" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 "https://$DOMAIN/files/text.txt")
check "CF: /files/text.txt" "200" "$code"
v=$(curl -s -o /dev/null -w '%{http_version}' --max-time 15 "https://$DOMAIN/health")
check "CF: HTTP/2" "2" "$v"

echo
echo "-------------------------------------------------"
echo "passed: $pass   failed: $fail"
exit "$fail"
