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

package tcpbrutal

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"

	brutal "github.com/gitsrc/tcp-brutal-go"
)

func init() {
	httpcaddyfile.RegisterHandlerDirective("tcp_brutal", parseCaddyfileHandler)
	// Run after headers/rewrite so a client's declared bandwidth is read from
	// the request as it finally looks, and before the response is written.
	httpcaddyfile.RegisterDirectiveOrder("tcp_brutal", httpcaddyfile.After, "header")
}

// Handler lets a client declare its downlink bandwidth and applies it to the
// TCP socket the request arrived on, Hysteria style but for plain HTTP.
//
// The client sends its capacity (in Mbit/s) either as a header
//
//	GET /file.bin HTTP/1.1
//	X-Brutal-Down: 120
//
// or as a query parameter (`?brutal=120`). Caddy then paces the rest of that
// connection's response at that rate, clamped to [min, max]. Because all
// connections of a client share one group, declaring it once per connection
// keeps the total at the declared value.
//
// This works for HTTP/1.1 and HTTP/2 alike (HTTP/3 is QUIC and cannot use TCP
// Brutal; the handler logs and skips those).
type Handler struct {
	// FromHeader is the request header holding the client's downlink in Mbit/s.
	// Default: X-Brutal-Down. Set to "-" to disable the header source.
	FromHeader string `json:"from_header,omitempty"`

	// FromQuery is the query parameter holding the client's downlink in Mbit/s.
	// Default: brutal. Set to "-" to disable the query source.
	FromQuery string `json:"from_query,omitempty"`

	// Min and Max clamp the client's declared rate (Mbit/s unless suffixed).
	// Defaults: min 1 Mbps, max 10 Gbps.
	Min string `json:"min,omitempty"`
	Max string `json:"max,omitempty"`

	// Default is applied when the client declares nothing. When empty, such
	// connections are left as they are.
	Default string `json:"default,omitempty"`

	// Gain is the congestion window gain in tenths; 0 keeps whatever the
	// listener wrapper set (normally 15 = 1.5x).
	Gain uint32 `json:"gain,omitempty"`

	// ApplyToGroup propagates the declared rate to every connection of the
	// client, not just the one carrying this request. Default true.
	ApplyToGroup *bool `json:"apply_to_group,omitempty"`

	logger  *zap.Logger
	minRate uint64
	maxRate uint64
	defRate uint64
}

// CaddyModule returns the Caddy module information.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.tcp_brutal",
		New: func() caddy.Module { return new(Handler) },
	}
}

// Provision validates the configuration.
func (h *Handler) Provision(ctx caddy.Context) error {
	h.logger = ctx.Logger()

	if h.FromHeader == "" {
		h.FromHeader = "X-Brutal-Down"
	}
	if h.FromQuery == "" {
		h.FromQuery = "brutal"
	}
	h.minRate = brutal.Mbps(1)
	h.maxRate = brutal.Gbps(10)
	var err error
	if h.Min != "" {
		if h.minRate, err = ParseRate(h.Min); err != nil {
			return fmt.Errorf("parsing min: %v", err)
		}
	}
	if h.Max != "" {
		if h.maxRate, err = ParseRate(h.Max); err != nil {
			return fmt.Errorf("parsing max: %v", err)
		}
	}
	if h.minRate > h.maxRate {
		return fmt.Errorf("min (%s) is greater than max (%s)",
			brutal.PrettyRate(h.minRate), brutal.PrettyRate(h.maxRate))
	}
	if h.Default != "" {
		if h.defRate, err = ParseRate(h.Default); err != nil {
			return fmt.Errorf("parsing default: %v", err)
		}
	}
	if h.Gain != 0 && (h.Gain < brutal.MinCwndGain || h.Gain > brutal.MaxCwndGain) {
		return fmt.Errorf("gain %d out of range [%d, %d]", h.Gain, brutal.MinCwndGain, brutal.MaxCwndGain)
	}
	if !brutal.Supports() {
		h.logger.Warn("tcp-brutal kernel module is not available; tcp_brutal handler will not change rates",
			zap.String("kernel", brutal.KernelRelease()))
	}
	return nil
}

// ServeHTTP applies the client-declared rate to the connection, then continues.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if err := h.apply(r); err != nil {
		// Never break the request because of a performance feature.
		h.logger.Debug("could not apply client-declared TCP Brutal rate",
			zap.String("remote", r.RemoteAddr),
			zap.Error(err))
	}
	return next.ServeHTTP(w, r)
}

func (h *Handler) apply(r *http.Request) error {
	localAddr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if localAddr == nil {
		return fmt.Errorf("no local address in request context")
	}

	entry, ok := conns.lookup(localAddr)
	if !ok {
		// The listener wrapper is not in use (or this is a QUIC connection).
		return fmt.Errorf("connection %v is not tracked by caddy.listeners.tcp_brutal", localAddr)
	}

	rate, declared, err := h.declaredRate(r)
	if err != nil {
		return err
	}
	if !declared {
		if h.defRate == 0 {
			return nil // nothing to do; keep the listener's rate
		}
		rate = h.defRate
	}

	gain := h.Gain
	if gain == 0 {
		gain = entry.gain
	}
	if gain == 0 {
		gain = brutal.DefaultCwndGain
	}
	group := entry.group
	if h.ApplyToGroup != nil && !*h.ApplyToGroup {
		group = 0
	}

	params := brutal.Params{Rate: rate, CwndGain: gain, GroupID: group}
	if err := params.Validate(); err != nil {
		return err
	}

	applied := entry.applied
	if !applied {
		// The listener was lazy, or the module showed up later: enable now.
		if err := apply(entry.conn, params); err != nil {
			if isLocked(err) {
				conns.update(localAddr, entry.rate, entry.gain, true)
				return nil
			}
			if isBenign(err) {
				return err
			}
			conns.forget(localAddr)
			return err
		}
		applied = true
	} else if err := setParams(entry.conn, params); err != nil {
		if isLocked(err) {
			return nil // a brutalctl rule owns the socket; leave it alone
		}
		conns.forget(localAddr)
		return err
	}

	if declared || h.defRate > 0 {
		conns.update(localAddr, rate, gain, applied)
	}
	return nil
}

// declaredRate reads the client's downlink from the request. The returned
// declared flag reports whether the client said anything at all.
func (h *Handler) declaredRate(r *http.Request) (uint64, bool, error) {
	var raw, source string
	if h.FromHeader != "-" {
		if v := strings.TrimSpace(r.Header.Get(h.FromHeader)); v != "" {
			raw, source = v, "header "+h.FromHeader
		}
	}
	if raw == "" && h.FromQuery != "-" {
		if v := strings.TrimSpace(r.URL.Query().Get(h.FromQuery)); v != "" {
			raw, source = v, "query "+h.FromQuery
		}
	}
	if raw == "" {
		return 0, false, nil
	}

	rate, err := ParseRate(raw)
	if err != nil {
		return 0, true, fmt.Errorf("%s: %v", source, err)
	}
	if rate < h.minRate {
		rate = h.minRate
	}
	if rate > h.maxRate {
		rate = h.maxRate
	}
	return rate, true, nil
}

// setParams changes the rate of an already-brutal socket.
func setParams(c net.Conn, p brutal.Params) error {
	rc, ok := rawConn(c)
	if !ok {
		return &brutal.OpError{Op: "syscall.Conn", Err: brutal.ErrUnsupported}
	}
	var setErr error
	if err := rc.Control(func(fd uintptr) { setErr = brutal.SetParams(fd, p) }); err != nil {
		return &brutal.OpError{Op: "control", Err: brutal.ErrClosed, Errno: err}
	}
	return setErr
}

// UnmarshalCaddyfile parses the Caddyfile form. Syntax:
//
//	tcp_brutal {
//		from_header <name>     # default X-Brutal-Down, "-" to disable
//		from_query <name>      # default brutal, "-" to disable
//		min <rate>             # default 1Mbps
//		max <rate>             # default 10Gbps
//		default <rate>         # rate when the client declares nothing
//		gain <tenths>          # default: keep the listener's
//		group <bool>           # apply to all connections of the client (default true)
//	}
func (h *Handler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume the directive name

	// inline form: tcp_brutal <header-name>
	if d.NextArg() {
		switch d.Val() {
		case "off":
			h.FromHeader = "-"
			h.FromQuery = "-"
		default:
			h.FromHeader = d.Val()
		}
	}

	for d.NextBlock(0) {
		switch d.Val() {
		case "from_header":
			if !d.NextArg() {
				return d.ArgErr()
			}
			h.FromHeader = d.Val()
		case "from_query":
			if !d.NextArg() {
				return d.ArgErr()
			}
			h.FromQuery = d.Val()
		case "min":
			if !d.NextArg() {
				return d.ArgErr()
			}
			h.Min = d.Val()
		case "max":
			if !d.NextArg() {
				return d.ArgErr()
			}
			h.Max = d.Val()
		case "default":
			if !d.NextArg() {
				return d.ArgErr()
			}
			h.Default = d.Val()
		case "gain":
			if !d.NextArg() {
				return d.ArgErr()
			}
			g, err := strconv.ParseUint(d.Val(), 10, 32)
			if err != nil {
				return d.Errf("parsing gain: %v", err)
			}
			h.Gain = uint32(g)
		case "group":
			b := true
			if d.NextArg() {
				var err error
				if b, err = strconv.ParseBool(d.Val()); err != nil {
					return d.Errf("parsing group: %v", err)
				}
			}
			h.ApplyToGroup = &b
		default:
			return d.Errf("unrecognized subdirective %q", d.Val())
		}
	}
	return nil
}

// parseCaddyfileHandler unmarshals the handler from the Caddyfile.
func parseCaddyfileHandler(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var handler Handler
	err := handler.UnmarshalCaddyfile(h.Dispenser)
	return &handler, err
}

// Interface guards
var (
	_ caddy.Module                = (*Handler)(nil)
	_ caddy.Provisioner           = (*Handler)(nil)
	_ caddyhttp.MiddlewareHandler = (*Handler)(nil)
	_ caddyfile.Unmarshaler       = (*Handler)(nil)
)
