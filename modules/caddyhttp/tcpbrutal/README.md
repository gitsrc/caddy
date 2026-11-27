# TCP Brutal for Caddy

This document describes the `tcpbrutal` module added to this Caddy build: a
[TCP Brutal](https://github.com/HyNetworks/tcp-brutal) integration that makes
Caddy pace its responses at a declared rate instead of probing for bandwidth.

Two modules are provided:

| Module | Caddyfile | Purpose |
|---|---|---|
| `caddy.listeners.tcp_brutal` | `listener_wrappers { tcp_brutal { ... } }` | applies brutal to every accepted connection (flat rate, or one pool per client IP) |
| `http.handlers.tcp_brutal` | `tcp_brutal { ... }` | lets the **client** declare its downlink (`X-Brutal-Down` / `?brutal=`), Hysteria-style, on an HTTP request |

Connections are never wrapped, so HTTP/1.1, HTTP/2, TLS, gRPC, websockets and
Caddy's other listener wrappers keep working unchanged.

## Requirements

- Linux ≥ 5.10 with the `tcp-brutal` kernel module **v2.0.0+** loaded:

  ```bash
  bash <(curl -fsSL https://tcp.hy2.sh/)     # DKMS build + brutalctl
  curl -fsSL https://raw.githubusercontent.com/gitsrc/tcp-brutal-go/main/scripts/kernel-check.sh | sudo bash
  ```

- Caddy built with this module (see *Building* below).

Without the module Caddy still starts: the wrapper and the handler log one
warning and every connection keeps the host's default congestion control. Add
`require_module` if you would rather fail the config load.

## Quick start

```caddyfile
{
	servers :443 {
		protocols h1 h2
		listener_wrappers {
			tcp_brutal {
				rate 150          # Mbit/s in total per client IP
			}
			tls
		}
	}
}

example.com {
	# optional: let the client declare its own downlink
	tcp_brutal {
		max 500
		default 150
	}
	root * /srv
	file_server
}
```

`rate` is the *sum* for all connections of one client IP (the default
`group per_ip`), which is what one-connection-per-stream clients need.

Clients can then self-limit:

```bash
curl -H 'X-Brutal-Down: 50' https://example.com/big.bin    # 50 Mbit/s
curl 'https://example.com/big.bin?brutal=50'               # same, query form
```

## Choosing the rate (important)

Brutal paces at exactly the declared rate and compensates loss by sending more.
It never probes and never backs off. Two consequences:

- **Too high → collapse.** If the rate exceeds what the receiver can absorb, the
  excess becomes loss, loss becomes retransmission, and the retransmissions
  shrink the receiver's window. Measured on a 168 ms path where stock Caddy
  (BBR) delivered 262 Mbit/s: `rate 300` delivered 116–133 Mbit/s with **71×
  more retransmitted bytes** than BBR. Half the bandwidth was spent on
  duplicates.
- **Right-sized → stable and loss-proof.** Same path with the client declaring
  `?brutal=100`: 88 Mbit/s delivered against a 100 Mbit/s target. On a path
  where the alternative is a loss-based CC (CUBIC), brutal holds the target
  while CUBIC collapses (see `REPORT.md` in this directory).

Practical guidance:

1. Measure what stock Caddy achieves on the path, then set `rate` to ~70–90 % of
   that **per stream** figure.
2. Prefer the negotiated form (`tcp_brutal` handler) when clients can be taught
   to send one header or query parameter: the client knows its own downlink.
3. Remember brutal only helps when the path is **loss-limited**. Behind a
   Cloudflare proxy the origin sees a clean datacenter path, so brutal changes
   nothing for the end user — it only applies to the hop Caddy terminates.

## Caddyfile reference

### listener wrapper

```caddyfile
listener_wrappers {
	tcp_brutal {
		rate <rate>            # required: 300 | 300Mbps | 37.5MB/s | 1Gbps
		gain <tenths>          # default 15 (1.5x); 20 is more aggressive
		group <mode>           # per_ip (default) | per_connection | fixed
		group_id <id>          # required with 'group fixed'
		lazy                   # do not touch sockets at accept; let the handler do it
		require_module         # fail config load if the kernel module is missing
	}
}
```

`lazy` is what you want when only the HTTP handler should decide the rate: it
keeps connections untouched (still BBR/cubic) until a request declares a rate.

Place `tcp_brutal` **before** the `tls` marker. Caddy applies wrappers listed
before the marker to the raw socket and those after it to the TLS stream. Both
work for brutal (the module unwraps `*tls.Conn`), but before-TLS is cheaper and
composes with `proxy_protocol` and other wrappers that hide the descriptor.

### HTTP handler

```caddyfile
tcp_brutal {
	from_header <name>     # default X-Brutal-Down, "-" disables
	from_query <name>      # default brutal, "-" disables
	min <rate>             # default 1 Mbps
	max <rate>             # default 10 Gbps
	default <rate>         # rate when the client declares nothing (empty = leave as is)
	gain <tenths>          # default: keep what the listener set
	group <bool>           # apply to all connections of the client (default true)
}
```

The handler finds the socket through `http.LocalAddrContextKey`, so it works for
HTTP/1.1 and HTTP/2 (multiplexed requests all share the same socket). HTTP/3 is
QUIC and has no TCP socket: the handler logs at debug level and does nothing.

### JSON

```json
{
  "listener_wrappers": [
    {"wrapper": "tcp_brutal", "rate": "150", "gain": 15, "group": "per_ip"}
  ],
  "routes": [{"handle": [
    {"handler": "tcp_brutal", "max": "500", "default": "150"}
  ]}]
}
```

## Verifying it works

```bash
# 1. the module is loaded and the connection really uses brutal
curl -o /dev/null https://example.com/big.bin &
ss -tin dst <client-ip> | grep -o 'brutal\|pacing_rate [0-9]*bps'
#   brutal ... pacing_rate 150000000bps      <- exactly the configured rate

# 2. the client-declared rate is honoured
for r in 20 50 100; do
  curl -s -o /dev/null -H "X-Brutal-Down: $r" -w "$r -> %{speed_download}\n" \
       https://example.com/big.bin
done
```

`pacing_rate` matching the configured rate is the definitive proof; the delivered
`delivery_rate` will be lower when the receiver's window or the path is the
limit.

## Building

The module lives in `modules/caddyhttp/tcpbrutal` and is registered in
`modules/caddyhttp/standard/imports.go`, so a plain build includes it:

```bash
git clone https://github.com/caddyserver/caddy   # this fork/branch
cd caddy
go build -o caddy ./cmd/caddy
./caddy list-modules | grep brutal
#   caddy.listeners.tcp_brutal
#   http.handlers.tcp_brutal
```

`go.mod` carries a `replace` to a local checkout of
`github.com/gitsrc/tcp-brutal-go` (the library is currently private). Either keep
the checkout next to `caddy/`, or drop the `replace` once the module is public:

```bash
# private-module fetch, if you prefer not to vendor a local path
export GOPRIVATE=github.com/gitsrc
git config --global url."git@github.com:".insteadOf "https://github.com/"
```

## Tests

```bash
go test ./modules/caddyhttp/tcpbrutal/ -v
```

- unit tests: rate parsing, Caddyfile parsing for both modules, config
  validation, registry behaviour, adaptation to JSON;
- integration tests: a real Caddy instance with the wrapper and the handler
  serving requests (with and without a declared rate, and in `lazy` mode). They
  pass on machines without the kernel module (graceful-degradation path) and
  exercise the real brutal path when it is loaded.

## Deployment

A working systemd deployment lives in `deploy/`; it is what runs on the host used
for the numbers in `REPORT.md`.

```bash
# 1. kernel module: into /lib/modules so modprobe and modules-load.d work
sudo install -d "/lib/modules/$(uname -r)/extra"
sudo install -m 644 brutal.ko "/lib/modules/$(uname -r)/extra/"
sudo depmod -a && sudo modprobe brutal
echo brutal | sudo tee /etc/modules-load.d/brutal.conf

# 2. caddy with the module
sudo install -m 755 caddy /usr/local/bin/caddy
sudo install -d /etc/caddy /var/log/caddy /srv/www
sudo install -m 644 deploy/Caddyfile /etc/caddy/Caddyfile

# 3. units
sudo install -m 644 deploy/caddy.service /etc/systemd/system/
sudo install -m 644 deploy/brutal-backend.service /etc/systemd/system/   # demo reverse_proxy target
sudo systemctl daemon-reload
sudo systemctl enable --now brutal-backend caddy

# 4. verify
curl -sI https://example.com/health
curl -s -o /dev/null -w '%{http_code} %{http_version}\n' https://example.com/
ss -tin dst <client-ip> | grep -o 'brutal\|pacing_rate [0-9]*bps'
```

### Two pitfalls that cost real debugging time

1. **`ProtectHome=true` breaks TLS.** Caddy stores certificates in
   `$XDG_DATA_HOME/caddy` (`/root/.local/share/caddy` by default). With
   `ProtectHome=true` the unit cannot read them and every handshake fails
   (Cloudflare reports *525 SSL Handshake Failed*). The unit in `deploy/`
   therefore sets `XDG_DATA_HOME=/var/lib/caddy` and keeps `/root` untouched —
   move the existing `certificates/` and `acme/` directories there when
   migrating.
2. **Do not put `log` at the global level only.** A global `log { output file ... }`
   configures the *default* logger; to get structured **access** logs you also
   need a `log` block inside the site (see `deploy/Caddyfile`). Otherwise the
   log file only receives provision/runtime events and looks suspiciously quiet.

### Verified functional test suite

`test/functional-test.sh` runs 42 checks against a live deployment (direct to
the origin and through a CDN) and prints PASS/FAIL per check:

| Area | Checks |
|---|---|
| redirects | HTTP→HTTPS 308 with the right `Location` |
| TLS/headers | HSTS, custom headers, `Server` removed, certificate identity |
| static files | index, directory browse, 60 MB download with matching `sha256`, ranges (206), HEAD + `Content-Length`, conditional GET (304) |
| compression | `gzip` and `zstd` negotiated |
| reverse_proxy | backend reached, query/Host/`X-Forwarded-Proto`/`X-Forwarded-For` forwarded, POST body delivered byte-identical |
| errors | 404 status and the custom error page |
| protocol | HTTP/1.1 forced, HTTP/2 negotiated, keep-alive reuse (3 requests, 1 TCP connection) |
| TCP Brutal | declared rates honoured (`X-Brutal-Down: 40/80`, `?brutal=60`), pacing confirmed via `ss` |
| CDN path | the same endpoints through a Cloudflare proxy |

Latest run on a Debian 12 KVM host (1 vCPU, kernel 6.1.0-44, module 2.0.1,
Let's Encrypt certificate issued over the proxied DNS record):

```
passed: 42   failed: 0
```

The host was then rebooted to prove the deployment is persistent: the module
loaded from `/lib/modules/$(uname -r)/extra` via `modules-load.d`, both units
started, the existing certificate was reused, and the suite passed 42/42 again.
With a client declaring `X-Brutal-Down: 100`, the socket reported
`pacing_rate 101010096bps` and the client received 100.7 Mbit/s (0.7 % error).

HTTP/3 (QUIC) was verified separately, since it must keep working untouched by
the TCP-only module: a quic-go client pinned to the origin IP with the site name
as SNI got `proto=HTTP/3.0 tls=TLS 1.3 status=200`.

## Operational notes

- **Do not** set `net.ipv4.tcp_congestion_control=brutal` system-wide: sockets
  with no parameters would be pinned at 1 Mbit/s.
- Keep `net.core.default_qdisc=fq` (Caddy hosts usually have it).
- `group per_ip` needs the module to be v2; on a v1 module the wrapper falls back
  to per-connection rates and logs a warning.
- Rules created manually with `brutalctl add <cidr> <rate>` take precedence: the
  handler then reports "locked" and leaves the socket alone.
- Caddy reloads keep working: each config creates its own wrappers and the
  registry self-expires stale entries after 15 minutes of inactivity.

## Known limitations

- UDP/HTTP3 cannot use TCP Brutal (it is a TCP congestion control).
- The handler needs the listener wrapper of the same configuration to have
  registered the connection; used alone it logs "connection not tracked" at
  debug level and does nothing.
- Per-client rate limits are per client **IP**: clients behind a shared NAT share
  one pool. Use `group fixed` or a different `group_id` scheme if that matters.
