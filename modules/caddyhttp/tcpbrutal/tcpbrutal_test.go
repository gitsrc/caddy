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
	"net"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"

	brutal "github.com/gitsrc/tcp-brutal-go"
)

func TestParseRate(t *testing.T) {
	tests := []struct {
		in   string
		want uint64
	}{
		{"300", 37_500_000}, // bare numbers are Mbit/s
		{"300Mbps", 37_500_000},
		{"300mbps", 37_500_000},
		{"300 mb", 37_500_000},
		{"300mbit", 37_500_000},
		{"1Gbps", 125_000_000},
		{"1gb", 125_000_000},
		{"37.5MB/s", 37_500_000}, // megabytes per second
		{"12MBps", 12_000_000},
		{"1_000_000B/s", 1_000_000}, // bytes per second
		{"600kbps", 75_000},         // 600 kbit/s
		{"  42  ", 5_250_000},       // whitespace tolerated
	}
	for _, tc := range tests {
		got, err := ParseRate(tc.in)
		if err != nil {
			t.Errorf("ParseRate(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseRate(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}

	// 100 kbit/s is below the kernel's 500 kbit/s floor
	if v, err := ParseRate("600kbps"); err != nil || v != 75_000 {
		t.Errorf("ParseRate(600kbps) = %d, %v", v, err)
	}
	if _, err := ParseRate("100kbps"); err == nil {
		t.Error("ParseRate(100kbps) must fail: below the kernel's minimum rate")
	}

	bad := []string{"", "abc", "300parsecs", "-5", "0", "0.0001", "1e999", "1Tbps"}
	for _, in := range bad {
		if v, err := ParseRate(in); err == nil {
			t.Errorf("ParseRate(%q) = %d, want error", in, v)
		}
	}
}

func TestListenerWrapperUnmarshalCaddyfile(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    ListenerWrapper
		wantErr string
	}{
		{
			name:  "inline rate",
			input: "tcp_brutal 300",
			want:  ListenerWrapper{Rate: "300"},
		},
		{
			name: "block",
			input: `tcp_brutal {
				rate 250Mbps
				gain 20
				group fixed
				group_id 42
				lazy
				require_module
			}`,
			want: ListenerWrapper{
				Rate: "250Mbps", Gain: 20, Group: "fixed", GroupID: 42,
				Lazy: true, RequireModule: true,
			},
		},
		{
			name: "explicit booleans",
			input: `tcp_brutal {
				rate 1Gbps
				lazy false
				require_module false
			}`,
			want: ListenerWrapper{Rate: "1Gbps"},
		},
		{
			name:    "unknown subdirective",
			input:   "tcp_brutal {\n\tbogus 1\n}",
			wantErr: "unrecognized subdirective",
		},
		{
			name:    "missing arg",
			input:   "tcp_brutal {\n\trate\n}",
			wantErr: "wrong argument count",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var w ListenerWrapper
			d := caddyfile.NewTestDispenser(tc.input)
			err := w.UnmarshalCaddyfile(d)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if w != tc.want {
				t.Fatalf("got %+v, want %+v", w, tc.want)
			}
		})
	}
}

func TestHandlerUnmarshalCaddyfile(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		check   func(*testing.T, *Handler)
		wantErr string
	}{
		{
			name:  "bare directive",
			input: "tcp_brutal",
			check: func(t *testing.T, h *Handler) {
				if h.FromHeader != "" || h.Min != "" {
					t.Fatalf("unexpected config: %+v", h)
				}
			},
		},
		{
			name:  "inline header name",
			input: "tcp_brutal X-Down",
			check: func(t *testing.T, h *Handler) {
				if h.FromHeader != "X-Down" {
					t.Fatalf("from_header = %q", h.FromHeader)
				}
			},
		},
		{
			name:  "off",
			input: "tcp_brutal off",
			check: func(t *testing.T, h *Handler) {
				if h.FromHeader != "-" || h.FromQuery != "-" {
					t.Fatalf("sources not disabled: %+v", h)
				}
			},
		},
		{
			name: "block",
			input: `tcp_brutal {
				from_header X-Bw
				from_query bw
				min 5
				max 800
				default 100
				gain 18
				group false
			}`,
			check: func(t *testing.T, h *Handler) {
				if h.FromHeader != "X-Bw" || h.FromQuery != "bw" {
					t.Fatalf("sources = %q/%q", h.FromHeader, h.FromQuery)
				}
				if h.Min != "5" || h.Max != "800" || h.Default != "100" {
					t.Fatalf("rates = %q/%q/%q", h.Min, h.Max, h.Default)
				}
				if h.Gain != 18 {
					t.Fatalf("gain = %d", h.Gain)
				}
				if h.ApplyToGroup == nil || *h.ApplyToGroup {
					t.Fatalf("group flag = %v", h.ApplyToGroup)
				}
			},
		},
		{
			name:    "bad gain",
			input:   "tcp_brutal {\n\tgain abc\n}",
			wantErr: "parsing gain",
		},
		{
			name:    "unknown subdirective",
			input:   "tcp_brutal {\n\tnope\n}",
			wantErr: "unrecognized subdirective",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var h Handler
			err := h.UnmarshalCaddyfile(caddyfile.NewTestDispenser(tc.input))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc.check(t, &h)
		})
	}
}

// TestProvisionWithoutModule checks that a machine without the kernel module
// can still provision the listener (graceful degradation), and that
// require_module turns that into an error.
func TestProvisionWithoutModule(t *testing.T) {
	if brutal.Supports() {
		t.Skip("kernel module is loaded; this test covers the fallback path")
	}

	w := &ListenerWrapper{Rate: "300", Gain: 15}
	ctx, cancel := caddy.NewContext(caddy.Context{Context: t.Context()})
	defer cancel()
	if err := w.Provision(ctx); err != nil {
		t.Fatalf("provisioning without module: %v", err)
	}
	if w.rate != brutal.Mbps(300) {
		t.Fatalf("rate = %d, want %d", w.rate, brutal.Mbps(300))
	}
	if w.gain != 15 || w.group != groupPerIP {
		t.Fatalf("defaults not applied: gain=%d group=%v", w.gain, w.group)
	}

	strict := &ListenerWrapper{Rate: "300", RequireModule: true}
	ctx2, cancel2 := caddy.NewContext(caddy.Context{Context: t.Context()})
	defer cancel2()
	err := strict.Provision(ctx2)
	if err == nil || !strings.Contains(err.Error(), "tcp-brutal kernel module") {
		t.Fatalf("require_module error = %v, want a module error", err)
	}
}

func TestProvisionValidation(t *testing.T) {
	tests := []struct {
		name    string
		w       ListenerWrapper
		wantErr string
	}{
		{"missing rate", ListenerWrapper{}, "rate is required"},
		{"bad rate", ListenerWrapper{Rate: "fast"}, "parsing rate"},
		{"gain too high", ListenerWrapper{Rate: "300", Gain: 99}, "out of range"},
		{"gain too low", ListenerWrapper{Rate: "300", Gain: 2}, "out of range"},
		{"bad group", ListenerWrapper{Rate: "300", Group: "sideways"}, "unknown group mode"},
		{"fixed without id", ListenerWrapper{Rate: "300", Group: "fixed"}, "requires a non-zero group_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.w
			ctx, cancel := caddy.NewContext(caddy.Context{Context: t.Context()})
			defer cancel()
			err := w.Provision(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	r := &registry{entries: make(map[string]*connEntry)}
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	e := &connEntry{group: 7, rate: 100, gain: 15}
	r.store(nil, e) // nil conn must not panic
	r.store(&fakeConn{local: addr}, e)

	got, ok := r.lookup(addr)
	if !ok || got != e {
		t.Fatalf("lookup = %v, %v", got, ok)
	}
	if r.size() != 1 {
		t.Fatalf("size = %d", r.size())
	}

	r.update(addr, 200, 20, true)
	if got.rate != 200 || got.gain != 20 || !got.applied {
		t.Fatalf("update = %+v", got)
	}

	if _, ok := r.lookup(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999}); ok {
		t.Fatal("unknown address must not resolve")
	}

	// stale entries are swept
	e.seen = time.Now().Add(-2 * entryTTL)
	r.lastSweep = time.Time{}
	r.sweepLocked(time.Now())
	if r.size() != 0 {
		t.Fatalf("stale entry not swept, size = %d", r.size())
	}

	r.store(&fakeConn{local: addr}, e)
	r.forget(addr)
	if r.size() != 0 {
		t.Fatalf("forget did not remove the entry")
	}
}

func TestAddrKey(t *testing.T) {
	if addrKey(nil) != "" {
		t.Fatal("nil addr must produce an empty key")
	}
	a := &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 80}
	if addrKey(a) != a.String() {
		t.Fatalf("key = %q, want %q", addrKey(a), a.String())
	}
	type other struct{ net.Addr }
	o := other{&net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 80}}
	if !strings.Contains(addrKey(o), "/10.0.0.1:80") {
		t.Fatalf("non-TCP addr key = %q", addrKey(o))
	}
}

func TestGroupFor(t *testing.T) {
	peer := &net.TCPAddr{IP: net.IPv4(203, 0, 113, 5), Port: 4242}
	peer2 := &net.TCPAddr{IP: net.IPv4(203, 0, 113, 5), Port: 9001}

	perIP := &ListenerWrapper{group: groupPerIP}
	if g := perIP.groupFor(peer); g == 0 || g != perIP.groupFor(peer2) {
		t.Fatalf("per_ip grouping broken: %d vs %d", g, perIP.groupFor(peer2))
	}
	if g := (&ListenerWrapper{group: groupPerConnection}).groupFor(peer); g != 0 {
		t.Fatalf("per_connection group = %d", g)
	}
	if g := (&ListenerWrapper{group: groupFixed, GroupID: 99}).groupFor(peer); g != 99 {
		t.Fatalf("fixed group = %d", g)
	}
	for mode, want := range map[groupMode]string{
		groupPerIP:         "per_ip",
		groupPerConnection: "per_connection",
		groupFixed:         "fixed",
	} {
		if mode.String() != want {
			t.Errorf("groupMode(%d).String() = %q, want %q", mode, mode.String(), want)
		}
	}
}

// fakeConn is a net.Conn with a configurable local address, for registry tests.
type fakeConn struct {
	net.Conn
	local net.Addr
}

func (f *fakeConn) LocalAddr() net.Addr                { return f.local }
func (f *fakeConn) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (f *fakeConn) Read([]byte) (int, error)           { return 0, nil }
func (f *fakeConn) Write(b []byte) (int, error)        { return len(b), nil }
func (f *fakeConn) Close() error                       { return nil }
func (f *fakeConn) SetDeadline(t time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(t time.Time) error { return nil }
