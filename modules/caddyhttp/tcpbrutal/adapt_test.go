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
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	// import the standard modules so the adapter knows "respond" etc.
	_ "github.com/caddyserver/caddy/v2/modules/standard"
	// and the module under test
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/tcpbrutal"
)

// TestCaddyfileAdaptation proves the modules are wired into the Caddyfile
// adapter: the global listener wrapper lands in the server's listener_wrappers
// and the directive becomes a route handler.
func TestCaddyfileAdaptation(t *testing.T) {
	caddyfileInput := `
	{
		skip_install_trust
		admin localhost:2999
		http_port 9080
		servers {
			listener_wrappers {
				tcp_brutal {
					rate 250Mbps
					gain 20
					group per_ip
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
		respond "hello"
	}
	`

	adapter := caddyfile.Adapter{ServerType: httpcaddyfile.ServerType{}}
	result, warnings, err := adapter.Adapt([]byte(caddyfileInput), nil)
	if err != nil {
		t.Fatalf("adapting: %v", err)
	}
	for _, w := range warnings {
		if !strings.Contains(w.String(), "not formatted") {
			t.Fatalf("unexpected warning: %v", w)
		}
	}
	cfg := string(result)

	for _, want := range []string{
		`"handler":"tcp_brutal"`,
		`"wrapper":"tcp_brutal"`,
		`"rate":"250Mbps"`,
		`"gain":20`,
		`"group":"per_ip"`,
		`"max":"500"`,
		`"default":"100"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("adapted config missing %s\n%s", want, cfg)
		}
	}
}
