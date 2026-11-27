# Regression evidence: does the integration change stock Caddy behaviour?

Short answer: no. The change is one import line in an existing file plus a new,
self-contained package; when `tcp_brutal` is not configured no code path runs.

Everything below was executed on the build host against a pristine upstream build
of the same commit (`679806f`), and on the live deployment.

## 1. Core footprint

```
$ git diff --stat 679806f
 19 files changed, 2810 insertions(+)
 $ git diff 679806f -- modules/caddyhttp/standard/imports.go
 +	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/tcpbrutal"
```

One line in an existing file. The rest is the new `modules/caddyhttp/tcpbrutal`
package, its tests, docs, deploy units and bench scripts.

## 2. Module registry

```
$ diff <(caddy-stock list-modules) <(caddy-brutal list-modules)
> caddy.listeners.tcp_brutal
> http.handlers.tcp_brutal
  Standard modules: 136 -> 138
```

Two additions, nothing removed or renamed.

## 3. Caddyfile adaptation is byte-identical for configs that do not use it

Four representative configs (reverse_proxy + health checks + load balancing,
`proxy_protocol` + `tls` listener wrappers, encode/header/root/file_server,
basic_auth, templates, redir, `tls internal`, multi-site, global options) adapted
with both binaries:

```
c1: IDENTICAL (2520 bytes)
c2: IDENTICAL (3936 bytes)
c3: IDENTICAL (1898 bytes)
c4: IDENTICAL (1172 bytes)
```

## 4. HTTP semantics identical with brutal switched on

pristine upstream on `:9443` vs this build with `tcp_brutal` on `:443`
(HTTP/1.1 on both sides, status + headers + body compared, volatile headers
`Date`/`Alt-Svc`/CSP-nonce excluded):

```
GET /health                identical  (status 200)
GET / (index)              identical  (status 200)
GET /api/info              identical  (reached through reverse_proxy)
POST /api/echo             identical  (body forwarded byte-identical)
range 0-1023               identical  (status 206)
HEAD /files/text.txt       identical  (status 200)
404                        identical  (custom error page)
gzip                       identical  (content-encoding negotiated)
zstd                       identical  (content-encoding negotiated)
```

## 5. Upstream test suite

`go test -short ./caddyconfig/... ./modules/caddyhttp/...` on both trees:

```
stock : 16 packages ok, 3 FAIL lines
mine  : 17 packages ok, 3 FAIL lines   (+1 = .../modules/caddyhttp/tcpbrutal)
```

The failure set is byte-identical: one pre-existing, unrelated failure
(`TestServer_ClosedRequestBodyDoesNotDesyncHTTP1`) that fails the same way on
pristine `679806f`.

## 6. Live deployment

- `test/functional-test.sh`: 42/42 checks (redirect, TLS/HSTS, static files,
  60 MB download with matching sha256, ranges, HEAD, 304, gzip/zstd,
  reverse_proxy with header forwarding, 404 page, h1/h2, keep-alive reuse,
  declared-rate pacing, CDN path).
- HTTP/3 (QUIC) verified with a quic-go client: `HTTP/3.0`, TLS 1.3, 200.
- `systemctl reload` under load: no failed requests, no warnings in the journal.
- Reboot: module auto-loaded, services started, certificate reused, suite green.
- Uptime since deployment: no restarts, no OOM, RSS steady around 20 MB.

## 7. What does change globally, and the caveats

1. `tcp_brutal` becomes a reserved Caddyfile directive name, ordered after
   `header`. A pre-existing custom directive with that exact name would clash.
2. The brute modules are Linux-only in effect: on other platforms `Listen`/`Dial`
   return `ErrUnsupported` and connections stay on the host's CC. The package
   still compiles everywhere (verified with GOOS=darwin/windows and arm64).
3. The socket options are only applied when a listener wrapper or handler is
   configured. Cost when configured: two `setsockopt` calls per connection
   (~0.5 µs measured), plus a map entry that expires after 15 min idle.
4. It is a fork. The same module also builds as a stock xcaddy plugin if you
   prefer not to carry a patched tree:

   ```bash
   xcaddy build --with github.com/gitsrc/tcp-brutal-go/caddy=./modules/caddyhttp/tcpbrutal
   ```

   (once the module path is published; today the tree carries a `replace` to a
   local checkout of `github.com/gitsrc/tcp-brutal-go`).
