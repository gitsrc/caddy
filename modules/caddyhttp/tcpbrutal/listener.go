// Copyright 2015 Matthew Holt and The Caddy Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package tcpbrutal integrates TCP Brutal (github.com/HyNetworks/tcp-brutal)
// into Caddy, both as a listener wrapper and as an HTTP handler.
//
// TCP Brutal is Hysteria's congestion control ported to TCP as an out-of-tree
// Linux kernel module. Instead of probing for bandwidth it paces packets at a
// rate you declare, which makes it dramatically better than CUBIC on long,
// lossy paths and lets a server hold a stable rate per client.
//
// Two modules are provided:
//
//	caddy.listeners.tcp_brutal  (directive: listener_wrappers > tcp_brutal)
//	    Applies brutal to every accepted connection: a flat rate, or one
//	    shared pool per client IP.
//
//	http.handlers.tcp_brutal    (directive: tcp_brutal)
//	    Lets the *client* declare its downlink (X-Brutal-Down header or
//	    ?brutal= query) and adjusts the live socket accordingly, Hysteria
//	    style, without any change to the client beyond sending a header.
//
// Nothing is wrapped: connections keep flowing through net/http's own types, so
// HTTP/1.1, HTTP/2, TLS, gRPC and websockets are unaffected.
package tcpbrutal

import (
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"go.uber.org/zap"

	brutal "github.com/gitsrc/tcp-brutal-go"
)

func init() {
	caddy.RegisterModule(ListenerWrapper{})
	caddy.RegisterModule(Handler{})
}

// ListenerWrapper applies TCP Brutal to connections accepted by Caddy.
//
// It must be placed before the `tls` listener wrapper marker, so that it sees
// the raw TCP connection rather than a TLS stream: TCP Brutal only needs the
// file descriptor, but placing it first also makes it compatible with other
// wrappers that hide the descriptor.
type ListenerWrapper struct {
	// Rate is the total send rate shared by all connections of a peer, in
	// Mbit/s unless suffixed ("300" or "300Mbps", "37.5MB/s", "1Gbps").
	//
	// Keep it at or below ~90% of what the receiving side's link can carry:
	// TCP Brutal paces at exactly this rate and never probes for capacity.
	Rate string `json:"rate,omitempty"`

	// Gain is the congestion window gain in tenths (15 = 1.5x). Default 15.
	Gain uint32 `json:"gain,omitempty"`

	// Group controls which connections share the rate pool:
	//
	//	per_ip          (default) one pool per client IP
	//	per_connection  each connection gets the full rate
	//	fixed           one pool for everybody (uses group_id)
	Group string `json:"group,omitempty"`

	// GroupID is the group identifier for `group fixed`.
	GroupID uint64 `json:"group_id,omitempty"`

	// Lazy skips applying brutal at accept time; sockets are left untouched
	// until a handler (http.handlers.tcp_brutal) applies it on demand, e.g.
	// after the client declared its downlink. Handy for HTTP/2 and HTTP/3
	// frontends that want the rate to follow the client's wish.
	Lazy bool `json:"lazy,omitempty"`

	// RequireModule makes provisioning fail when the kernel module is not
	// loaded. By default Caddy starts anyway and connections keep the host's
	// default congestion control.
	RequireModule bool `json:"require_module,omitempty"`

	logger *zap.Logger

	rate   uint64
	gain   uint32
	group  groupMode
	module brutal.Version
	// warned is set once a module problem has been logged, to avoid flooding
	// the log for every accepted connection. A plain uint32 with CAS keeps the
	// struct copyable, which CaddyModule's value receiver requires.
	warned uint32
}

// CaddyModule returns the Caddy module information.
func (ListenerWrapper) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "caddy.listeners.tcp_brutal",
		New: func() caddy.Module { return new(ListenerWrapper) },
	}
}

// Provision parses and validates the configuration and probes the kernel module.
func (w *ListenerWrapper) Provision(ctx caddy.Context) error {
	w.logger = ctx.Logger()

	if w.Rate == "" {
		return fmt.Errorf("rate is required (e.g. rate 300)")
	}
	rate, err := ParseRate(w.Rate)
	if err != nil {
		return fmt.Errorf("parsing rate: %v", err)
	}
	w.rate = rate

	if w.Gain == 0 {
		w.Gain = brutal.DefaultCwndGain
	}
	if w.Gain < brutal.MinCwndGain || w.Gain > brutal.MaxCwndGain {
		return fmt.Errorf("gain %d out of range [%d, %d]", w.Gain, brutal.MinCwndGain, brutal.MaxCwndGain)
	}
	w.gain = w.Gain

	switch strings.ToLower(strings.TrimSpace(w.Group)) {
	case "", "per_ip", "ip", "per_client":
		w.group = groupPerIP
	case "per_connection", "per_conn", "none":
		w.group = groupPerConnection
	case "fixed", "global":
		w.group = groupFixed
		if w.GroupID == 0 {
			return fmt.Errorf("group fixed requires a non-zero group_id")
		}
	default:
		return fmt.Errorf("unknown group mode %q (want per_ip, per_connection or fixed)", w.Group)
	}

	if params := (brutal.Params{Rate: w.rate, CwndGain: w.gain}); params.Validate() != nil {
		return fmt.Errorf("invalid TCP Brutal parameters: %v", params.Validate())
	}

	if v, err := brutal.ModuleVersionProbe(); err != nil {
		if w.RequireModule {
			return fmt.Errorf("tcp-brutal kernel module is not available on %s: %v "+
				"(load it with 'insmod brutal.ko', see docs/kernel-setup.md)", brutal.KernelRelease(), err)
		}
		w.logger.Warn("tcp-brutal kernel module is not available; connections will use the "+
			"host's default congestion control",
			zap.String("kernel", brutal.KernelRelease()),
			zap.Error(err))
	} else {
		w.module = v
		if !v.SupportsGroups() && w.group != groupPerConnection {
			w.logger.Warn("kernel module is too old for TCP Brutal groups; "+
				"every connection will get the full rate",
				zap.String("module", v.String()))
			w.group = groupPerConnection
		}
		if w.Lazy {
			w.logger.Info("tcp-brutal listener is lazy: rates are applied by http.handlers.tcp_brutal",
				zap.String("module", v.String()), zap.String("rate", brutal.PrettyRate(w.rate)))
		} else {
			w.logger.Info("tcp-brutal enabled",
				zap.String("module", v.String()),
				zap.String("rate", brutal.PrettyRate(w.rate)),
				zap.Uint32("gain_tenths", w.gain),
				zap.String("group", w.group.String()))
		}
	}

	return nil
}

// WrapListener applies TCP Brutal to accepted connections.
func (w *ListenerWrapper) WrapListener(ln net.Listener) net.Listener {
	return &brutalListener{Listener: ln, w: w}
}

// UnmarshalCaddyfile parses the Caddyfile form. Syntax:
//
//	tcp_brutal {
//		rate <rate>            # required, e.g. 300, 300Mbps, 37.5MB/s, 1Gbps
//		gain <tenths>          # default 15 (1.5x)
//		group <mode>           # per_ip (default) | per_connection | fixed
//		group_id <id>          # required with 'group fixed'
//		lazy                   # apply rates on demand from http.handlers.tcp_brutal
//		require_module         # fail config load if the kernel module is missing
//	}
func (w *ListenerWrapper) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume the wrapper name

	// allow the one-line form: tcp_brutal 300
	if d.NextArg() {
		w.Rate = d.Val()
	}

	for d.NextBlock(0) {
		switch d.Val() {
		case "rate":
			if !d.NextArg() {
				return d.ArgErr()
			}
			w.Rate = d.Val()
		case "gain":
			if !d.NextArg() {
				return d.ArgErr()
			}
			g, err := strconv.ParseUint(d.Val(), 10, 32)
			if err != nil {
				return d.Errf("parsing gain: %v", err)
			}
			w.Gain = uint32(g)
		case "group":
			if !d.NextArg() {
				return d.ArgErr()
			}
			w.Group = d.Val()
		case "group_id":
			if !d.NextArg() {
				return d.ArgErr()
			}
			id, err := strconv.ParseUint(d.Val(), 10, 64)
			if err != nil {
				return d.Errf("parsing group_id: %v", err)
			}
			w.GroupID = id
		case "lazy":
			w.Lazy = true
			if d.NextArg() {
				b, err := strconv.ParseBool(d.Val())
				if err != nil {
					return d.Errf("parsing lazy: %v", err)
				}
				w.Lazy = b
			}
		case "require_module":
			w.RequireModule = true
			if d.NextArg() {
				b, err := strconv.ParseBool(d.Val())
				if err != nil {
					return d.Errf("parsing require_module: %v", err)
				}
				w.RequireModule = b
			}
		default:
			return d.Errf("unrecognized subdirective %q", d.Val())
		}
	}
	return nil
}

// brutalListener applies TCP Brutal in Accept and remembers each connection so
// that the HTTP handler can adjust its rate later.
type brutalListener struct {
	net.Listener
	w *ListenerWrapper
}

// Accept applies TCP Brutal to the new connection.
//
// A failure never breaks the connection unless require_module is set: the whole
// point of brutal is performance, not correctness.
func (bl *brutalListener) Accept() (net.Conn, error) {
	conn, err := bl.Listener.Accept()
	if err != nil {
		return nil, err
	}

	w := bl.w
	group := w.groupFor(conn.RemoteAddr())
	entry := &connEntry{
		conn:  conn,
		group: group,
		rate:  w.rate,
		gain:  w.gain,
		seen:  time.Now(),
	}

	if !w.Lazy {
		switch err := apply(conn, brutal.Params{Rate: w.rate, CwndGain: w.gain, GroupID: group}); {
		case err == nil:
			entry.applied = true
		default:
			if w.RequireModule {
				_ = conn.Close()
				return nil, fmt.Errorf("tcp_brutal: %v", err)
			}
			if !isBenign(err) && atomic.CompareAndSwapUint32(&w.warned, 0, 1) {
				w.logger.Warn("could not apply TCP Brutal; connections keep the default congestion control",
					zap.Error(err))
			}
			// ErrLocked means a brutalctl rule already governs this socket.
			entry.applied = isLocked(err)
		}
	}

	conns.store(conn, entry)
	return conn, nil
}

// Close also drops the registry so a new config does not inherit stale entries.
func (bl *brutalListener) Close() error {
	return bl.Listener.Close()
}

func (w *ListenerWrapper) groupFor(peer net.Addr) uint64 {
	switch w.group {
	case groupPerConnection:
		return 0
	case groupFixed:
		return w.GroupID
	default:
		return brutal.GroupByRemoteIP(peer)
	}
}

// apply sets brutal on a connection, tolerating the wrappers Caddy may have
// put around the socket.
func apply(c net.Conn, p brutal.Params) error {
	rc, ok := rawConn(c)
	if !ok {
		return &brutal.OpError{Op: "syscall.Conn", Err: brutal.ErrUnsupported}
	}
	var applyErr error
	if err := rc.Control(func(fd uintptr) {
		if err := brutal.Enable(fd); err != nil {
			applyErr = err
			return
		}
		applyErr = brutal.SetParams(fd, p)
	}); err != nil {
		return &brutal.OpError{Op: "control", Err: brutal.ErrClosed, Errno: err}
	}
	return applyErr
}

// rawConn finds the connection that owns the file descriptor, unwrapping TLS
// and any other wrapper that exposes the layer below it.
func rawConn(c net.Conn) (syscall.RawConn, bool) {
	for range 8 {
		if sc, ok := c.(syscall.Conn); ok {
			rc, err := sc.SyscallConn()
			if err == nil {
				return rc, true
			}
			return nil, false
		}
		switch v := c.(type) {
		case interface{ NetConn() net.Conn }: // crypto/tls.Conn
			c = v.NetConn()
		case interface{ Unwrap() net.Conn }:
			c = v.Unwrap()
		default:
			return nil, false
		}
	}
	return nil, false
}

func isLocked(err error) bool {
	return err != nil && strings.Contains(err.Error(), "locked rule")
}

// isBenign reports whether the error is an expected "no module here" case that
// deserves a single log line rather than a warning per connection.
func isBenign(err error) bool {
	if err == nil {
		return true
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not loaded"),
		strings.Contains(msg, "locked rule"),
		strings.Contains(msg, "not supported"):
		return true
	}
	return false
}

type groupMode int

const (
	groupPerIP groupMode = iota
	groupPerConnection
	groupFixed
)

func (g groupMode) String() string {
	switch g {
	case groupPerConnection:
		return "per_connection"
	case groupFixed:
		return "fixed"
	default:
		return "per_ip"
	}
}

// ParseRate parses a human rate into bytes per second. Accepted forms:
//
//	300, 300Mbps, 300mb, 300Mbit/s     Mbit/s
//	1Gbps, 1gb                         Gbit/s
//	600kbps                            Kbit/s
//	37.5MB/s, 37.5MBps                 MByte/s (an uppercase B means bytes)
//	1000000B/s                         bytes/s
//
// A bare number is Mbit/s, which is what people write when talking about links.
func ParseRate(s string) (uint64, error) {
	orig := s
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("empty rate")
	}

	// split the numeric prefix from the unit suffix
	i := 0
	for i < len(trimmed) && (trimmed[i] >= '0' && trimmed[i] <= '9' || trimmed[i] == '.' || trimmed[i] == '_') {
		i++
	}
	numStr := strings.ReplaceAll(trimmed[:i], "_", "")
	unit := strings.TrimSpace(trimmed[i:])

	if numStr == "" {
		return 0, fmt.Errorf("invalid rate %q: no number", orig)
	}
	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil || val <= 0 || math.IsInf(val, 0) || math.IsNaN(val) {
		return 0, fmt.Errorf("invalid rate %q", orig)
	}

	factor, ok := rateUnitFactor(unit)
	if !ok {
		return 0, fmt.Errorf("invalid rate unit %q in %q (want Mbps, Gbps, MB/s or B/s)", unit, orig)
	}
	bytesPerSec := val * factor

	if bytesPerSec < float64(brutal.MinRate) || bytesPerSec > float64(brutal.MaxRate) {
		return 0, fmt.Errorf("rate %q is outside the kernel's supported range [%s, %s]",
			orig, brutal.PrettyRate(brutal.MinRate), brutal.PrettyRate(brutal.MaxRate))
	}
	return uint64(bytesPerSec), nil
}

// rateUnits maps a unit suffix to a bytes-per-second multiplier. An uppercase
// B in the unit (MB/s, MBps) means bytes; anything else means bits.
var rateUnits = []struct {
	suffix string
	factor float64
}{
	{"Gbit/s", 1e9 / 8}, {"Gbit", 1e9 / 8}, {"Gbps", 1e9 / 8}, {"g", 1e9 / 8}, {"gb", 1e9 / 8},
	{"Mbit/s", 1e6 / 8}, {"Mbit", 1e6 / 8}, {"Mbps", 1e6 / 8}, {"m", 1e6 / 8}, {"mb", 1e6 / 8},
	{"Kbit/s", 1e3 / 8}, {"Kbit", 1e3 / 8}, {"Kbps", 1e3 / 8}, {"k", 1e3 / 8}, {"kb", 1e3 / 8},
	{"GB/s", 1e9}, {"GBps", 1e9}, {"GBytes/s", 1e9},
	{"MB/s", 1e6}, {"MBps", 1e6}, {"MBytes/s", 1e6},
	{"KB/s", 1e3}, {"KBps", 1e3},
	{"B/s", 1}, {"Bps", 1}, {"bytes/s", 1},
}

func rateUnitFactor(unit string) (float64, bool) {
	if unit == "" {
		return 1e6 / 8, true // bare numbers are Mbit/s
	}
	bytesMarker := strings.ContainsRune(unit, 'B')
	for _, u := range rateUnits {
		if bytesMarker != strings.ContainsRune(u.suffix, 'B') {
			continue // do not confuse MB/s (bytes) with Mbps (bits)
		}
		if strings.EqualFold(unit, u.suffix) {
			return u.factor, true
		}
	}
	return 0, false
}

// Interface guards
var (
	_ caddy.Module          = (*ListenerWrapper)(nil)
	_ caddy.Provisioner     = (*ListenerWrapper)(nil)
	_ caddy.ListenerWrapper = (*ListenerWrapper)(nil)
	_ caddyfile.Unmarshaler = (*ListenerWrapper)(nil)
)
