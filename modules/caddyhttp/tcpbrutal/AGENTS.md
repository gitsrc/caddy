# AGENTS.md — `modules/caddyhttp/tcpbrutal`

Scope: this directory only. Caddy's own conventions live in the repository root
`AGENTS.md`; read that first, then this. The upstream project's guidance still
applies to anything you touch outside this directory — but **you should not be
touching anything outside this directory** (see §1).

Full project context: [`github.com/gitsrc/tcp-brutal-go` → `AGENTS.md`](https://github.com/gitsrc/tcp-brutal-go/blob/main/AGENTS.md).

---

## 1. What this is, and the size of the delta

A Caddy integration for [TCP Brutal](https://github.com/HyNetworks/tcp-brutal):
the server paces its responses at a declared rate instead of probing for
bandwidth. Two modules:

| Module | Caddyfile |
|---|---|
| `caddy.listeners.tcp_brutal` | `listener_wrappers { tcp_brutal { rate 150 } … }` |
| `http.handlers.tcp_brutal` | `tcp_brutal { max 500, default 150 }` (directive) |

**Total delta against upstream Caddy:** this directory plus **one line** in
`modules/caddyhttp/standard/imports.go`. Nothing else in the Caddy tree is
modified. If you find yourself editing another Caddy file, stop and reconsider —
the same module also ships as a stock-Caddy plugin (`gitsrc/tcp-brutal-go/caddy`)
built with `xcaddy`, which is the preferred route for anything upstream-facing.

## 2. Hard rules

1. **Connections are never wrapped.** `Accept` applies options through
   `syscall.Conn`/`SyscallConn()`/`Control` and returns the untouched conn. TLS,
   `proxy_protocol`, `http2Listener`, `net/http` and gRPC all depend on that.
2. **A performance feature must never fail a request or a config.** Missing module
   → one warning, connections keep the host CC; locked `brutalctl` rule → report
   `RuleLocked`, do not fight it. Only `require_module` may make it fatal.
3. **No automatic rate discovery.** No `TCP_INFO` sampling, no probing, no
   per-connection goroutines that retune. The rate is what the config says, or what
   the client declares (`X-Brutal-Down` / `?brutal=`), clamped by `max`. The
   library's optional `autotune` package is deliberately not linked here.
4. **Caddy module hygiene**: `caddy.Module` + `Provisioner` + `ListenerWrapper` /
   `caddyhttp.MiddlewareHandler` + `caddyfile.Unmarshaler`; register with
   `caddy.RegisterModule` in `init`; keep JSON tags documented with comments
   (Caddy's docs site generates from them); no panics, no global state beyond the
   connection registry.
5. **The registry self-expires.** `registry.go` maps a connection's local address
   (the only handle `net/http` exposes, via `http.LocalAddrContextKey`) to its
   per-connection state, with a 15-minute TTL and a size cap. Do not add an
   unbounded map.
6. **`go.mod` carries `replace github.com/gitsrc/tcp-brutal-go => ../`** in the
   library repo's copy of this package, because that repository is private. Keep
   the plugin's dependency list to `caddy/v2`, the library and `zap`.

## 3. Build and test

```bash
go build ./... && go vet ./... && gofmt -l .
go test ./...                     # unit + integration; integration t.Skip without
                                  # the kernel module, so this is green anywhere
go test -run TestServer -v ./...   # the two end-to-end tests (real Caddy instance)
```

Build a binary with it (either way — same code):

```bash
# inside the fork
go build -o caddy ./cmd/caddy && ./caddy list-modules | grep brutal
# stock Caddy + plugin
xcaddy build --with github.com/gitsrc/tcp-brutal-go/caddy
```

## 4. Deploy (all of this has been done for real, twice)

```bash
sudo bash .../tcp-brutal-go/scripts/install-module.sh --dkms   # kernel side, pinned
sudo install -m 755 caddy /usr/local/bin/caddy
sudo install -m 644 deploy/Caddyfile /etc/caddy/Caddyfile
sudo install -m 644 deploy/caddy.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now caddy
```

Two traps, both hit in production:

- **`ProtectHome=true` + Caddy's certificate storage.** Caddy keeps certs in
  `$XDG_DATA_HOME/caddy` (`/root/.local/share/caddy` by default). With
  `ProtectHome=true` every TLS handshake fails (Cloudflare reports *525*). The unit
  in `deploy/` sets `XDG_DATA_HOME=/var/lib/caddy`.
- **A global-only `log` does not produce access logs.** Add a `log` block inside
  the site, otherwise the file only receives provision events.

## 5. Verify a deployment

```bash
bash test/functional-test.sh          # 42 checks: redirect, TLS/HSTS, static files,
                                      # 60 MB download sha256, ranges, HEAD, 304,
                                      # gzip/zstd, reverse_proxy, 404, h1/h2,
                                      # keep-alive, declared rates, CDN path
ss -tin dst <client-ip> | grep -o 'brutal\|pacing_rate [0-9]*bps'
```

`pacing_rate` matching the configured rate is the only convincing proof. Do not
claim "brutal is on" from logs alone.

## 6. Benchmarks

`bench/netns-matrix.sh` (utilisation matrix in a throwaway netns, wire volume from
veth TX counters), `bench/rate-accuracy.sh`, `bench/client-ab-*.sh`,
`bench/run-all.sh` (all of it, writes `/root/bench/REPORT.md`).
`bench/results-on-host.md` holds a raw run. Read `REPORT.md` before quoting numbers:
brutal loses to BBR when the rate overshoots, and gains ~0 when the receiver's
window is the limit.

## 7. Definition of done

- [ ] `gofmt -l .` empty, `go vet ./...` clean, `go test ./...` green
- [ ] the two `TestServer…` end-to-end tests pass
- [ ] a Caddyfile without `tcp_brutal` still adapts byte-identically to upstream
      (`REGRESSION.md` describes the comparison; keep it valid)
- [ ] `pacing_rate` verified on a real socket when behaviour changed
- [ ] `README.md` / `docs` / `REPORT.md` updated in the same commit
