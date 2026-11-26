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

package tcpbrutal_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/caddytest"
)

// TestServerWithBrutalListener runs a real Caddy instance whose listener is
// wrapped by tcp_brutal and whose route uses the tcp_brutal handler, then makes
// ordinary HTTP requests against it.
//
// On a machine with the kernel module loaded this exercises the full brutal
// path; without it, it proves that the integration degrades to plain TCP
// without breaking a single request.
func TestServerWithBrutalListener(t *testing.T) {
	tester := caddytest.NewTester(t)
	tester.InitServer(`
	{
		skip_install_trust
		admin localhost:2999
		http_port 9080
		https_port 9443
		servers {
			listener_wrappers {
				tcp_brutal {
					rate 100Mbps
					gain 15
				}
				tls
			}
		}
	}

	:9080 {
		tcp_brutal {
			max 500
			default 100
		}
		respond "brutal-ok"
	}
	`, "caddyfile")

	resp, err := tester.Client.Get("http://localhost:9080/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if strings.TrimSpace(string(body)) != "brutal-ok" {
		t.Fatalf("body = %q", body)
	}

	// A client that declares its downlink must get the same response, with the
	// socket retuned on the fly by the handler.
	req, err := http.NewRequest(http.MethodGet, "http://localhost:9080/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Brutal-Down", "42")
	resp2, err := tester.Client.Do(req)
	if err != nil {
		t.Fatalf("GET with X-Brutal-Down: %v", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusOK || strings.TrimSpace(string(body2)) != "brutal-ok" {
		t.Fatalf("declared-rate request failed: %d %q", resp2.StatusCode, body2)
	}

	// And a nonsense declaration must not break the request either.
	req.Header.Set("X-Brutal-Down", "not-a-number")
	resp3, err := tester.Client.Do(req)
	if err != nil {
		t.Fatalf("GET with an invalid declared rate: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("status = %d for an invalid declaration", resp3.StatusCode)
	}
}

// TestServerWithLazyBrutal exercises the lazy mode, where nothing is applied at
// accept time and rates only come from the handler.
func TestServerWithLazyBrutal(t *testing.T) {
	tester := caddytest.NewTester(t)
	tester.InitServer(`
	{
		skip_install_trust
		admin localhost:2999
		http_port 9081
		servers {
			listener_wrappers {
				tcp_brutal {
					rate 50Mbps
					lazy
				}
			}
		}
	}

	:9081 {
		tcp_brutal {
			default 20
			group false
		}
		respond "lazy-ok"
	}
	`, "caddyfile")

	resp, err := tester.Client.Get("http://localhost:9081/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "lazy-ok" {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
}
