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
	"sync"
	"time"
)

// connEntry is the per-connection state the listener wrapper hands to the HTTP
// handler, so that the handler can retune the socket the request arrived on.
type connEntry struct {
	conn    net.Conn
	group   uint64
	rate    uint64
	gain    uint32
	applied bool // brutal is active on the socket
	seen    time.Time
}

// registry maps a connection's local address to its brutal state.
//
// net/http exposes the local address to handlers through http.LocalAddrContextKey
// (the server end of the connection, unique per accepted socket), which is the
// only supported way for a handler to find the socket a request arrived on.
type registry struct {
	mu        sync.Mutex
	entries   map[string]*connEntry
	lastSweep time.Time
}

const (
	// entryTTL bounds the memory used by the registry for connections that are
	// gone. Entries are refreshed on every request, and keep-alive connections
	// default to 5 minutes of idle time in Caddy, so 15 minutes is generous.
	entryTTL = 15 * time.Minute
	// sweepInterval throttles the cleanup pass.
	sweepInterval = time.Minute
	// maxEntries is a hard cap; beyond it the oldest entries are dropped.
	maxEntries = 100_000
)

var conns = &registry{entries: make(map[string]*connEntry)}

// store records the state of a freshly accepted connection.
func (r *registry) store(c net.Conn, e *connEntry) {
	if c == nil || e == nil {
		return
	}
	key := addrKey(c.LocalAddr())
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[string]*connEntry)
	}
	r.entries[key] = e
	r.sweepLocked(e.seen)
}

// lookup returns the entry for a connection local address, refreshing it.
func (r *registry) lookup(addr net.Addr) (*connEntry, bool) {
	key := addrKey(addr)
	if key == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[key]
	if !ok {
		return nil, false
	}
	e.seen = time.Now()
	return e, true
}

// update records a new rate on an existing entry.
func (r *registry) update(addr net.Addr, rate uint64, gain uint32, applied bool) {
	key := addrKey(addr)
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[key]; ok {
		e.rate = rate
		if gain > 0 {
			e.gain = gain
		}
		e.applied = applied
		e.seen = time.Now()
	}
}

// forget drops an entry, e.g. after the socket turned out to be gone.
func (r *registry) forget(addr net.Addr) {
	key := addrKey(addr)
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, key)
}

// sweepLocked removes entries that have not been touched for entryTTL, and
// enforces maxEntries. Callers must hold r.mu.
func (r *registry) sweepLocked(now time.Time) {
	if len(r.entries) > maxEntries {
		// hard cap: drop everything old first, then the map is rebuilt by traffic
		for k, e := range r.entries {
			if now.Sub(e.seen) > entryTTL/4 {
				delete(r.entries, k)
			}
		}
		if len(r.entries) > maxEntries {
			clear(r.entries)
		}
	}
	if now.Sub(r.lastSweep) < sweepInterval {
		return
	}
	r.lastSweep = now
	for k, e := range r.entries {
		if now.Sub(e.seen) > entryTTL {
			delete(r.entries, k)
		}
	}
}

// size reports the current number of tracked connections (for tests/metrics).
func (r *registry) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// addrKey normalizes a net.Addr into a map key. The same value is produced for
// the connection's LocalAddr and for http.LocalAddrContextKey, because net/http
// stores the very same net.Addr value.
func addrKey(a net.Addr) string {
	if a == nil {
		return ""
	}
	if ta, ok := a.(*net.TCPAddr); ok {
		return ta.String()
	}
	return a.Network() + "/" + a.String()
}
