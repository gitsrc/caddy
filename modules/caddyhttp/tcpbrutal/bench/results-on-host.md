# TCP Brutal in Caddy — measurements executed on this host

- host: vm125363.cloudsilk.io
- kernel: 6.1.0-44-cloud-amd64   cpus: 1   mem: 973 MB
- module: tcp-brutal 2.0.1  (/lib/modules/6.1.0-44-cloud-amd64/extra/brutal.ko)
- caddy: 679806f92d49e5b494200f2e90a93f228203a418+modified (24 Nov 26 07:49 UTC)
- generated: 2026-11-27 03:23:28Z

## 1. Functional suite (HTTP behaviour with brutal enabled)

checks passed: 42
passed: 42   failed: 0

<details><summary>per-check output</summary>

```
== 1. HTTP → HTTPS redirect (port 80, direct) ==
  PASS status 308
  PASS Location points at https
== 2. HTTPS basics (direct to origin) ==
  PASS GET /health status
  PASS GET /health body
  PASS Strict-Transport-Security
  PASS X-Served-By header
  PASS no Server header
== 3. static files ==
  PASS GET / (index.html)
  PASS index.html content
  PASS GET /files/ (directory browse)
  PASS directory listing
== 4. full-file integrity (60 MB, pacé par brutal) ==
  PASS download status
  PASS size
  PASS sha256 matches origin
  PASS no Server header on download
== 5. range requests ==
  PASS range status (206)
  PASS range size (1024)
  PASS second range status
== 6. HEAD and conditional requests ==
  PASS HEAD status
  PASS HEAD content-length
  PASS conditional GET (304)
== 7. compression ==
  PASS gzip negotiated
  PASS zstd negotiated
== 8. reverse_proxy ==
  PASS backend reached
  PASS query forwarded
  PASS Host forwarded
  PASS X-Forwarded-Proto
  PASS X-Forwarded-For present
  PASS POST body reached backend intact
== 9. error handling ==
  PASS 404 status
  PASS custom error page
== 10. protocol negotiation and keep-alive ==
  PASS HTTP/2 negotiated
  PASS HTTP/1.1 forced
  PASS connection reuse across 3 requests
== 11. TCP Brutal is actually pacing ==
  PASS declared 40 Mbit/s respected (got 39.8 Mbit/s)
  PASS declared 80 Mbit/s respected (got 79.6 Mbit/s)
  PASS ?brutal=60 query form respected (got 59.7 Mbit/s)
== 12. stock listener for comparison ==
  PASS plain :8443 serves
== 13. through the public DNS/CDN path ==
  PASS CF: /health
  PASS CF: /api/info
  PASS CF: /files/text.txt
  PASS CF: HTTP/2

-------------------------------------------------
passed: 42   failed: 0
```

</details>

## 2. Declared-rate accuracy (loopback)

| declared | delivered | accuracy | kernel pacing_rate |
|---|---|---|---|
| 20 Mbit/s | 19.9 Mbit/s | 99.5% | 20 Mbit/s |
| 50 Mbit/s | 49.7 Mbit/s | 99.4% | 50 Mbit/s |
| 100 Mbit/s | 99.4 Mbit/s | 99.4% | 100 Mbit/s |
| 150 Mbit/s | 149.0 Mbit/s | 99.4% | 150 Mbit/s |
| 200 Mbit/s | 198.8 Mbit/s | 99.4% | 200 Mbit/s |

Loopback has no loss, so this isolates the pacing accuracy from path effects;
pacing_rate is read from ss while a declared-rate transfer is in flight.

## 3. Bandwidth utilisation matrix (shaped path inside a netns)


| scenario | loss | goodput (Mbit/s) | link utilisation | wire load (Mbit/s) | overhead vs useful |
|---|---|---|---|---|---|
| stock Caddy, ns CC = cubic | 0% | 185.2 | 92.6% | 186.0 | 0% |
| stock Caddy, ns CC = bbr | 0% | 185.9 | 92.9% | 186.8 | 0% |
| brutal, rate 120 | 0% | 117.8 | 58.9% | 118.5 | 1% |
| brutal, rate 180 | 0% | 176.5 | 88.3% | 177.2 | 0% |
| brutal, rate 260 | 0% | 187.5 | 93.8% | 188.2 | 0% |
| stock Caddy, ns CC = cubic | 0.5% | 25.8 | 12.9% | 35.8 | 39% |
| stock Caddy, ns CC = bbr | 0.5% | 185.9 | 93.0% | 186.8 | 0% |
| brutal, rate 120 | 0.5% | 118.4 | 59.2% | 119.1 | 1% |
| brutal, rate 180 | 0.5% | 177.4 | 88.7% | 178.1 | 0% |
| brutal, rate 260 | 0.5% | 20.5 | 10.3% | 43.4 | 111% |
| stock Caddy, ns CC = cubic | 2% | 3.8 | 1.9% | 4.0 | 3% |
| stock Caddy, ns CC = bbr | 2% | 184.1 | 92.1% | 185.7 | 1% |
| brutal, rate 120 | 2% | 118.4 | 59.2% | 119.1 | 1% |
| brutal, rate 180 | 2% | 177.7 | 88.8% | 178.3 | 0% |
| brutal, rate 260 | 2% | 22.1 | 11.1% | 41.5 | 88% |
| stock Caddy, ns CC = cubic | 5% | 2.2 | 1.1% | 2.3 | 3% |
| stock Caddy, ns CC = bbr | 5% | 160.4 | 80.2% | 161.7 | 1% |
| brutal, rate 120 | 5% | 117.7 | 58.9% | 118.9 | 1% |
| brutal, rate 180 | 5% | 171.0 | 85.5% | 172.0 | 1% |
| brutal, rate 260 | 5% | 58.2 | 29.1% | 66.1 | 13% |

goodput = useful bytes/s at the receiver; link utilisation = goodput / 200 Mbit/s;
wire load = everything Caddy transmitted (retransmits included, from veth TX counters);
overhead = (wire bytes - useful bytes) / useful bytes.

## 4. Pacing proof

```
2 live sockets using the brutal algorithm right now
brutal
pacing_rate 200000000bps
delivery_rate 23349912bps
brutal
pacing_rate 200000000bps
delivery_rate 22515984bps
```
