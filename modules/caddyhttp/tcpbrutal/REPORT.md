# Measure history: TCP Brutal in Caddy

All numbers below were produced with this build of Caddy
(`caddy.listeners.tcp_brutal` + `http.handlers.tcp_brutal`) and the
`tcp-brutal` 2.0.1 kernel module on Debian 12 (`6.1.0-44-cloud-amd64`, KVM,
1 vCPU). Raw commands are in `bench/netns-bench.sh` and in the sections below.

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

## 1. Against CUBIC on a shaped path (the case brutal is built for)

Setup: throwaway netns + veth, TBF 200 Mbit/s on Caddy's egress, netem
15 ms/direction (30 ms RTT), loss swept on the sender, namespace default CC set
to `cubic`, Caddy serving 287 MB files from two servers — `:9090` with
`listener_wrappers { tcp_brutal { rate 180 } }` and `:9091` stock.

```
namespace default CC: cubic
path         : TBF 200 Mbit/s + 30ms RTT, loss on Caddy's egress

loss   brutal (Mbit/s)    plain/cubic (Mbit/s) gain
----   ---------------    ------------------ ----
0%     177.6              188.1              x0.94
0.5%   178.7              8.2                x21.79
2%     178.5              4.8                x37.19
5%     178.3              2.0                x89.15
10%    177.9              1.2                x148.25
```

Brutal's delivered rate is flat (177–179 Mbit/s against a 180 Mbit/s target,
99 %) regardless of loss; CUBIC treats every loss as congestion and never
recovers above 1–8 Mbit/s. This is the workload brutal exists for.

Client-declared rates on the same box (handler, `X-Brutal-Down`), clean path:

| declared | delivered | accuracy |
|---|---|---|
| 20 Mbit/s | 19.9 Mbit/s | 99.5 % |
| 40 Mbit/s | 36.3 Mbit/s | 91 % |
| 100 Mbit/s | 97.4 Mbit/s | 97 % |

## 2. Against BBR on the live domain (168 ms, ~1–8 % loss)

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

Two deployment bugs worth recording, both now fixed in `deploy/`:

1. `ProtectHome=true` in the systemd unit hid `/root`, where Caddy keeps its
   certificates, so every TLS handshake failed (Cloudflare: *525*). Fixed by
   moving storage to `/var/lib/caddy` via `XDG_DATA_HOME`.
2. A global `log { output file ... }` only redirects the *default* logger; the
   access log needs a `log` block inside the site, otherwise requests are not
   recorded at all.

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
# shaped-path comparison (root, iproute2 + iperf-free: uses curl)
sudo LOSSES="0 0.5 2 5 10" bash modules/caddyhttp/tcpbrutal/bench/netns-bench.sh

# live A/B against a stock listener
curl -o /dev/null -w '%{speed_download}\n' --resolve host:443:<origin-ip> https://host/big.bin
curl -o /dev/null -w '%{speed_download}\n' --resolve host:8443:<origin-ip> https://host:8443/big.bin

# proof that the socket really uses brutal
curl -o /dev/null https://host/big.bin & ss -tin dst <client-ip> | grep -o 'brutal\|pacing_rate [0-9]*bps'
```
