//go:build live

package doh

import (
	"context"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/miekg/dns"
)

// go test -tags live ./internal/doh -run Live -v
func TestLiveProviders(t *testing.T) {
	boot := NewBootstrap(config.Default().BootstrapDNS)
	for _, p := range config.BuiltinProviders() {
		for _, useBootstrap := range []bool{true, false} {
			bs := p.Bootstrap
			if !useBootstrap {
				bs = nil
			}
			u, err := New(Options{ID: p.ID, URL: p.URL, Bootstrap: bs, Resolver: boot, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"example.com", "claude.ai", "chatgpt.com"} {
				q := new(dns.Msg)
				q.SetQuestion(dns.Fqdn(name), dns.TypeA)
				q.RecursionDesired = true
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				start := time.Now()
				r, err := u.Exchange(ctx, q)
				cancel()
				if err != nil {
					t.Errorf("%-10s boot=%v %-12s ERR %v", p.ID, useBootstrap, name, err)
					continue
				}
				var ips []string
				for _, rr := range r.Answer {
					if a, ok := rr.(*dns.A); ok {
						ips = append(ips, a.A.String())
					}
				}
				t.Logf("%-10s boot=%v %-12s %s %v %v", p.ID, useBootstrap, name, dns.RcodeToString[r.Rcode], ips, time.Since(start).Round(time.Millisecond))
			}
			u.Close()
		}
	}
}
