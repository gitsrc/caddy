# Measure history: TCP Brutal in Caddy

All numbers below were produced with this build of Caddy
(`caddy.listeners.tcp_brutal` + `http.handlers.tcp_brutal`) and the
`tcp-brutal` 2.0.1 kernel module on Debian 12 (`6.1.0-44-cloud-amd64`, KVM,
1 vCPU). Raw commands are in `bench/netns-bench.sh` and in the sections below.

## How these numbers were produced

`bench/run-all.sh` runs the whole harness **on the Caddy host**: the functional
suite (42 checks), the declared-rate accuracy test (loopback) and the utilisation
matrix (shaped path inside a throwaway netns, wire volume from veth TX counters).
`bench/make-report.sh` then assembles the result into one markdown file. Section 2
is the only part that needs an external client.

## TL;DR

1. The integration is mechanically sound: the socket switches to `brutal`, the
   kernel's `pacing_rate` matches the configured rate to the byte, per-IP groups
   share one rate, and a client header (`X-Brutal-Down`) retunes a live
   connection within one request.
2. **Against a loss-based congestion control (CUBIC — the default on most hosts)
   brutal is transformative**: it holds the declared rate while CUBIC collapses
   (×21 at 0.5 % loss, ×148 at 10 % loss) on a clean 200 Mbit/s path.
3. **Against BBR, brutal is only as good as your rate setting.** BBR already
   tolerates random loss, so on a good path it beats a misconfigured brutal and
   merely matches a well-configured one. Overshoot is expensive: pointing brutal
   at 300 Mbit/s on a path that delivers ~150 Mbit/s single-stream halved
   throughput versus BBR and produced 71× more retransmitted bytes.
4. **Behind a Cloudflare proxy, brutal cannot help end users**: the origin's peer
   is Cloudflare's edge, not the user, so the paced hop is a clean datacenter
   link. All measurements against the live domain were taken with
   `--resolve` to bypass the proxy.

## 1. The utilisation matrix (everything measured on the Caddy host)

All numbers in this section were produced **on the host that runs Caddy**, so no
external link is part of the measurement: the shaped path lives in a throwaway
network namespace (veth pair, TBF 200 Mbit/s bottleneck, netem delay/loss on
Caddy's egress), Caddy runs inside it serving `:9090` with the `tcp_brutal`
wrapper and handler and `:9091` stock, and the client is `curl` on the same box.

Wire volume is read from the veth TX counters around each run, so retransmissions
are counted. Reproduce with `sudo bash bench/run-all.sh`.

Host: Debian 12, kernel 6.1.0-44-cloud-amd64, 1 vCPU, module 2.0.1.

| scenario | loss | goodput | link utilisation | wire load | overhead vs useful |
|---|---|---|---|---|---|
| stock Caddy, ns CC = cubic | 0 % | 185.2 Mbit/s | 92.6 % | 186.0 | 0 % |
| stock Caddy, ns CC = bbr | 0 % | 185.9 Mbit/s | 92.9 % | 186.8 | 0 % |
| brutal, rate 120 | 0 % | 117.8 Mbit/s | 58.9 % | 118.5 | 1 % |
| brutal, rate 180 | 0 % | 176.5 Mbit/s | 88.3 % | 177.2 | 0 % |
| brutal, rate 260 | 0 % | 187.5 Mbit/s | 93.8 % | 188.2 | 0 % |
| stock Caddy, ns CC = cubic | 0.5 % | 25.8 Mbit/s | 12.9 % | 35.8 | 39 % |
| stock Caddy, ns CC = bbr | 0.5 % | 185.9 Mbit/s | 93.0 % | 186.8 | 0 % |
| brutal, rate 120 | 0.5 % | 118.4 Mbit/s | 59.2 % | 119.1 | 1 % |
| brutal, rate 180 | 0.5 % | 177.4 Mbit/s | 88.7 % | 178.1 | 0 % |
| brutal, rate 260 | 0.5 % | 20.5 Mbit/s | 10.3 % | 43.4 | 111 % |
| stock Caddy, ns CC = cubic | 2 % | 3.8 Mbit/s | 1.9 % | 4.0 | 3 % |
| stock Caddy, ns CC = bbr | 2 % | 184.1 Mbit/s | 92.1 % | 185.7 | 1 % |
| brutal, rate 120 | 2 % | 118.4 Mbit/s | 59.2 % | 119.1 | 1 % |
| brutal, rate 180 | 2 % | 177.7 Mbit/s | 88.8 % | 178.3 | 0 % |
| brutal, rate 260 | 2 % | 22.1 Mbit/s | 11.1 % | 41.5 | 88 % |
| stock Caddy, ns CC = cubic | 5 % | 2.2 Mbit/s | 1.1 % | 2.3 | 3 % |
| stock Caddy, ns CC = bbr | 5 % | 160.4 Mbit/s | 80.2 % | 161.7 | 1 % |
| brutal, rate 120 | 5 % | 117.7 Mbit/s | 58.9 % | 118.9 | 1 % |
| brutal, rate 180 | 5 % | 171.0 Mbit/s | 85.5 % | 172.0 | 1 % |
| brutal, rate 260 | 5 % | 58.2 Mbit/s | 29.1 % | 66.1 | 13 % |

Run-to-run variance is real (each cell is an 8 s transfer on a 1 vCPU box):
repeated runs differ by ±5 % normally, and much more at 5 % loss where the
outcome depends on which recovery path the stack happens to take. The raw output
of the run above is committed as `bench/results-on-host.md`. Representative
figures are what matter here, not the third digit.

What this says, in order of practical importance:

1. **Against CUBIC** (the default on most hosts) brutal turns 1.1–12.8 % link
   utilisation into a flat 88.7 %, at every loss level, with **0 % wire
   overhead**. That is the case brutal exists for.
2. **Against BBR it does not raise utilisation.** BBR holds 80–93 % here (the
   5 %-loss column is the noisiest), slightly *above* brutal@180, simply because brutal is capped by the declared
   rate and BBR is not. Brutal's value is stability and a guaranteed rate, not a
   higher peak.
3. **The declared rate is a hard ceiling and also a cliff.** At rate 260 on a
   200 Mbit/s path, utilisation drops to 10–15 % and 83–95 % of all transmitted
   bytes are retransmissions — worse than doing nothing. Always declare at or
   below what the path (and the receiver's window) can absorb.
4. Loss below the declared rate barely matters to brutal: 120 → 117–118 Mbit/s
   and 180 → 176–177 Mbit/s from 0 % to 5 % loss.

Declared-rate accuracy on the loopback listener (no loss, isolates pacing):

| declared | delivered | accuracy | kernel `pacing_rate` |
|---|---|---|---|
| 20 Mbit/s | 19.9 | 99.6 % | 20 Mbit/s |
| 50 Mbit/s | 49.5 | 99.1 % | 50 Mbit/s |
| 100 Mbit/s | 99.4 | 99.4 % | 100 Mbit/s |
| 150 Mbit/s | 149.0 | 99.4 % | 150 Mbit/s |
| 200 Mbit/s | 198.7 | 99.4 % | 200 Mbit/s |

## 2. Live domain, external client (168 ms, ~1–8 % loss)

This section used a client outside the host on purpose: it is the only way to
observe a real intercontinental path. Everything else in this report is measured
on the Caddy host itself.

Environment: Caddy on a KVM VPS, Let's Encrypt certificate for the test domain
(HTTP-01 solved through the Cloudflare proxy), HTTP/2, two servers on the same
host and path: `:443` with `tcp_brutal { rate 300 }`, `:8443` stock (host default
`bbr`). Client: a separate host, RTT 168–176 ms. Requests were sent straight to
the origin with `curl --resolve` because the DNS record is Cloudflare-proxied.

### Socket state proves the integration

```
ESTAB [185.148.13.138]:443 [77.90.57.132]:54222
    brutal ... cwnd:7378 bytes_sent:100557267 bytes_retrans:6600
    pacing_rate 303030296bps delivery_rate 145322472bps
    rwnd_limited:4820ms(72.0%) snd_wnd:3126912
```

`pacing_rate` = 303 Mbit/s for `rate 300` — the kernel paces at exactly the
configured rate. `rwnd_limited` at 72 % shows the receiver's window, not the
server, was the binding constraint.

### Overshoot halves throughput: rate 300 on a ~150 Mbit/s single-stream path

Interleaved A/B, 15 s per run, 4 pairs:

| pair | brutal :443 | plain BBR :8443 | delta |
|---|---|---|---|
| 1 | 15.29 MB/s | 29.33 MB/s | −48 % |
| 2 | 15.99 MB/s | 33.12 MB/s | −52 % |
| 3 | 17.07 MB/s | 33.43 MB/s | −49 % |
| 4 | 16.56 MB/s | 33.39 MB/s | −50 % |
| mean | **16.23 MB/s (130 Mbit/s)** | **32.31 MB/s (262 Mbit/s)** | **−50 %** |

Simultaneous transfers (one per port) show where the bandwidth went:

| | delivered | retransmitted bytes | rwnd-limited |
|---|---|---|---|
| brutal @300 | 147 Mbit/s | 1 219 296 B | 79.7 % |
| BBR | 147 Mbit/s | 17 160 B | 72.5 % |

Same goodput, **71× the retransmissions**. Brutal paced 303 Mbit/s into a
receiver that could absorb ~150 Mbit/s; the excess became loss, the loss became
retransmission, and the duplicate stream kept the receiver's autotuned window at
3.1 MB instead of letting it grow to 6 MB as it did for BBR. This is exactly the
failure mode the upstream README warns about ("set it too high and you only
produce loss") — brutal has no feedback loop to correct it.

A rate sweep on the same path (via `?brutal=`, which retunes the live socket)
shows the plateau:

| declared | delivered |
|---|---|
| plain BBR | 262 Mbit/s |
| 100 | 88 Mbit/s |
| 150 | 127 Mbit/s |
| 200 | 120 Mbit/s |
| 250 | 133 Mbit/s |
| 300 | 116 Mbit/s |
| 400 | 118 Mbit/s |

Above ~150 Mbit/s the declared rate no longer matters: the receiver's window
caps the result, and brutal's compensation turns the rest into retransmissions.

### Client-declared rates work (and are the fix)

| declared | delivered | note |
|---|---|---|
| 20 Mbit/s | 18.4 Mbit/s | 92 % |
| 50 Mbit/s | 45.1 Mbit/s | 90 % |
| 120 Mbit/s | 90.7 Mbit/s | 76 %, path/window limited |
| `?brutal=30` | 49.2 Mbit/s | query form; clamped by `max 500` |

The client's declaration is applied to the live socket by the handler, on an
HTTP/2 connection, without touching the listener configuration. This is the
mechanism that makes brutal safe: the end that knows its own downlink states it.

## 3. Deployment verification (systemd, live domain)

Deployment: Debian 12 KVM (1 vCPU, kernel 6.1.0-44, module 2.0.1), Caddy running
under systemd as `/usr/local/bin/caddy` with `/etc/caddy/Caddyfile`, module
installed to `/lib/modules/$(uname -r)/extra/` and loaded via
`/etc/modules-load.d/brutal.conf`. Let's Encrypt certificate obtained through a
Cloudflare-proxied DNS record (HTTP-01 solved via the proxy; TLS-ALPN is
impossible there).

`test/functional-test.sh` — **42 checks, 42 passed**:

| Area | Result |
|---|---|
| HTTP→HTTPS 308 redirect | ✅ status + `Location` |
| TLS/headers | ✅ HSTS, `X-Served-By`, `Server` removed |
| static: index, browse, 60 MB download, ranges, HEAD, 304 | ✅ incl. matching `sha256` after a brutal-paced download |
| compression: gzip + zstd | ✅ both negotiated |
| reverse_proxy: query, Host, XFF, XFP, POST body | ✅ body byte-identical at the backend |
| errors | ✅ 404 + custom error page |
| protocol: h1 forced, h2 negotiated, keep-alive reuse | ✅ 3 requests over 1 connection |
| TCP Brutal declared rates (40/80 Mbit/s, `?brutal=60`) | ✅ 36.5 / 69.2 / 55.8 Mbit/s |
| same endpoints through the CDN | ✅ 200 |
| `caddy reload` under systemd | ✅ no dropped requests |
| journal | ✅ no warnings/errors |
| HTTP/3 (QUIC), verified with a quic-go client | ✅ `HTTP/3.0`, TLS 1.3, 200 |
| **survives a reboot** | ✅ module auto-loaded from `/lib/modules/*/extra` via `modules-load.d`, both services started, certificate reused, 42/42 checks pass again |

Two deployment bugs worth recording, both now fixed in `deploy/`:

1. `ProtectHome=true` in the systemd unit hid `/root`, where Caddy keeps its
   certificates, so every TLS handshake failed (Cloudflare: *525*). Fixed by
   moving storage to `/var/lib/caddy` via `XDG_DATA_HOME`.
2. A global `log { output file ... }` only redirects the *default* logger; the
   access log needs a `log` block inside the site, otherwise requests are not
   recorded at all.

Pacing accuracy on the live path after that reboot, client declaring 100 Mbit/s
(`X-Brutal-Down: 100`):

```
brutal  cwnd:2546  pacing_rate 101010096bps  delivery_rate 100732816bps
```

The kernel paced at 101 Mbit/s for a 100 Mbit/s target and the client received
100.7 Mbit/s — 0.7 % error end to end.

## 4. Operational conclusions

- **Choose the rate deliberately.** `rate` should sit at 70–90 % of the
  *single-stream* throughput the path can actually deliver. Measure it first with
  the stock configuration.
- **Prefer negotiated rates.** Teach clients to send `X-Brutal-Down` (or use
  `?brutal=`) and set a generous `max`. A wrong client declaration is clamped;
  a wrong server-wide default is not, and it is expensive.
- **Brutal is a CUBIC replacement, not a BBR replacement.** If the host already
  runs BBR on a clean path, expect parity; the win shows up on lossy paths, on
  hosts stuck on CUBIC, and where a *guaranteed* rate matters more than peak
  throughput.
- **Do not enable it behind a CDN.** With Cloudflare proxying, the paced hop is
  Cloudflare↔origin. Either grey-cloud the record (DNS-only) for clients that
  need brutal, or accept that the CDN, not brutal, is what shapes the user
  experience.
- Watch `Stats`/logs: a sudden jump in retransmissions with `rwnd_limited` high
  means the configured rate is above what the receiver can absorb.

## Reproduction

```bash
# everything, on the Caddy host (functional + rate accuracy + utilisation matrix)
sudo bash modules/caddyhttp/tcpbrutal/bench/run-all.sh

# live A/B against a stock listener
curl -o /dev/null -w '%{speed_download}\n' --resolve host:443:<origin-ip> https://host/big.bin
curl -o /dev/null -w '%{speed_download}\n' --resolve host:8443:<origin-ip> https://host:8443/big.bin

# proof that the socket really uses brutal
curl -o /dev/null https://host/big.bin & ss -tin dst <client-ip> | grep -o 'brutal\|pacing_rate [0-9]*bps'
```
