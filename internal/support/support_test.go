package support

import (
	"context"
	"net/netip"
	"reflect"
	"sync"
	"testing"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/miekg/dns"
)

func ip(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, v := range s {
		out = append(out, netip.MustParseAddr(v))
	}
	return out
}

func TestApexOf(t *testing.T) {
	for in, want := range map[string]string{"claude.ai": "claude.ai", "api.anthropic.com": "anthropic.com", "a.b.c.example.org": "example.org", "localhost": "localhost"} {
		if got := apexOf(in); got != want {
			t.Errorf("apexOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLearnPool(t *testing.T) {
	ref16 := map[netip.Prefix]bool{netip.MustParsePrefix("104.18.0.0/16"): true}
	answers := map[string][]netip.Addr{
		// a proxy address shared by four unrelated domains
		"chatgpt.com":   ip("193.233.112.68"),
		"claude.ai":     ip("193.233.112.68"),
		"grok.com":      ip("193.233.112.68", "193.233.112.67"),
		"notion.so":     ip("193.233.112.68"),
		"api.notion.so": ip("193.233.112.68"), // same apex as notion.so: counted once
		// a CDN address shared by many sites, but ordinary DNS shows its /16
		"patreon.com":  ip("104.18.69.106"),
		"linear.app":   ip("104.18.69.106"),
		"deezer.com":   ip("104.18.69.106"),
		"twitch.tv":    ip("104.18.69.106"),
		"only-two.com": ip("62.133.62.97"),
		"two-only.com": ip("62.133.62.97"),
		"blocked.com":  ip("0.0.0.0"),
		"blocked2.com": ip("0.0.0.0"),
		"blocked3.com": ip("0.0.0.0"),
		"lan.com":      ip("192.168.1.1"),
		"lan2.com":     ip("192.168.1.1"),
		"lan3.com":     ip("192.168.1.1"),
		"v6.com":       ip("2001:db8::1"),
		// Cloudflare addresses ordinary DNS did not show this time
		"cf1.com": ip("188.114.96.3"),
		"cf2.com": ip("188.114.96.3"),
		"cf3.com": ip("188.114.96.3"),
	}
	p := learnPool(answers, ref16, PoolMinApex)
	if !p.addrs[netip.MustParseAddr("193.233.112.68")] {
		t.Fatalf("shared proxy address not learned: %v", p.addrs)
	}
	for _, a := range []string{"104.18.69.106", "62.133.62.97", "0.0.0.0", "192.168.1.1", "188.114.96.3"} {
		if p.addrs[netip.MustParseAddr(a)] {
			t.Errorf("%s must not be a proxy address", a)
		}
	}
	// the whole /24 of a learned address counts (providers rotate within it)
	if !proxied(ip("193.233.112.200"), p) || proxied(ip("193.233.113.1"), p) || proxied(ip("2001:db8::1"), p) || proxied(nil, p) {
		t.Error("/24 handling is wrong")
	}
}

func TestSupports(t *testing.T) {
	set := map[string]bool{"a.test": true, "b.test": true, "c.test": false}
	is := func(h string) bool { return set[h] }
	for _, tc := range []struct {
		hosts []string
		n     int
		ok    bool
	}{
		{[]string{"a.test"}, 1, true},
		{[]string{"a.test", "c.test"}, 1, true}, // half is enough
		{[]string{"a.test", "c.test", "c.test"}, 1, false},
		{[]string{"a.test", "b.test", "c.test"}, 2, true},
		{[]string{"c.test"}, 0, false},
		{nil, 0, false},
	} {
		n, ok := supports(config.Service{Name: "x", Hosts: tc.hosts}, is)
		if n != tc.n || ok != tc.ok {
			t.Errorf("%v: (%d,%v), want (%d,%v)", tc.hosts, n, ok, tc.n, tc.ok)
		}
	}
}

func reply(host string, addrs ...string) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(host), dns.TypeA)
	m.Rcode = dns.RcodeSuccess
	for _, a := range addrs {
		m.Answer = append(m.Answer, &dns.A{Hdr: dns.RR_Header{Name: dns.Fqdn(host), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: netip.MustParseAddr(a).AsSlice()})
	}
	return m
}

// TestRun: provider "good" proxies the Claude and ChatGPT hosts through one
// address shared by three apexes, "plain" returns real addresses, "down" never answers.
func TestRun(t *testing.T) {
	services := []config.Service{
		{Name: "Claude", Hosts: []string{"claude.ai", "api.anthropic.com"}},
		{Name: "ChatGPT", Hosts: []string{"chatgpt.com"}},
		{Name: "Linear", Hosts: []string{"linear.app"}},
	}
	real := map[string]string{"claude.ai": "160.79.104.10", "api.anthropic.com": "160.79.104.11", "chatgpt.com": "104.18.1.1", "linear.app": "104.18.2.2"}
	o := Options{
		Services:  services,
		Extra:     []string{"extra.example", "extra2.example"},
		Providers: []config.Provider{{ID: "good"}, {ID: "plain"}, {ID: "down"}},
		Repeat:    3,
		Workers:   2,
		Ref: func(_ context.Context, h string) []netip.Addr {
			if a, ok := real[h]; ok {
				return ip(a)
			}
			return ip("93.184.216.34")
		},
		Ask: func(_ context.Context, p config.Provider, h string) (*dns.Msg, error) {
			switch p.ID {
			case "good":
				if h == "linear.app" || h == "extra.example" {
					return reply(h, "104.18.2.2"), nil // not proxied
				}
				return reply(h, "193.233.112.68"), nil
			case "plain":
				return reply(h, "93.184.216.34"), nil
			}
			return nil, context.DeadlineExceeded
		},
	}
	var last, total int
	o.Progress = func(done, all int) { last, total = done, all }
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || res[0].Provider != "good" || res[1].Provider != "plain" || res[2].Provider != "down" {
		t.Fatalf("results out of order: %+v", res)
	}
	if want := []string{"Claude", "ChatGPT"}; !reflect.DeepEqual(res[0].Services, want) {
		t.Errorf("good: %v, want %v", res[0].Services, want)
	}
	if len(res[0].Pool) == 0 || res[0].Pool[0] != "193.233.112.68" || res[0].Errors != 0 {
		t.Errorf("good: pool %v errors %d", res[0].Pool, res[0].Errors)
	}
	if res[0].Proxied("linear.app") || !res[0].Proxied("claude.ai") {
		t.Error("Proxied(host) is wrong")
	}
	if len(res[1].Services) != 0 {
		t.Errorf("plain must support nothing, got %v", res[1].Services)
	}
	if len(res[2].Services) != 0 || res[2].Errors != res[2].Hosts {
		t.Errorf("down: %v, %d errors of %d hosts", res[2].Services, res[2].Errors, res[2].Hosts)
	}
	if want := 6 + 3*3*6; total != want || last != want { // hosts + repeat*providers*hosts
		t.Errorf("progress ended at %d of %d, want %d", last, total, want)
	}
}

func TestRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, Options{Services: []config.Service{{Name: "x", Hosts: []string{"a.test"}}},
		Providers: []config.Provider{{ID: "p"}}, Ref: func(context.Context, string) []netip.Addr { return nil },
		Ask: func(context.Context, config.Provider, string) (*dns.Msg, error) { return nil, nil }})
	if err == nil {
		t.Fatal("a cancelled run must report the cancellation")
	}
}

// TestRunReach: the DNS says both services are proxied, but the proxy answers
// TLS only for Claude, so ChatGPT is reported unreachable (and the progress
// total counts the reach phase).
func TestRunReach(t *testing.T) {
	services := []config.Service{
		{Name: "Claude", Hosts: []string{"claude.ai", "api.anthropic.com"}},
		{Name: "ChatGPT", Hosts: []string{"chatgpt.com"}},
		{Name: "Notion", Hosts: []string{"notion.so"}}, // not proxied: never tested
	}
	var tested []string
	var mu sync.Mutex
	o := Options{
		Services:  services,
		Providers: []config.Provider{{ID: "good"}},
		Repeat:    3,
		Workers:   2,
		Ref:       func(context.Context, string) []netip.Addr { return ip("93.184.216.34") },
		Ask: func(_ context.Context, _ config.Provider, h string) (*dns.Msg, error) {
			if h == "notion.so" {
				return reply(h, "93.184.216.34"), nil
			}
			return reply(h, "193.233.112.68"), nil
		},
		Reach: func(_ context.Context, provider, host string, a netip.Addr) bool {
			mu.Lock()
			tested = append(tested, host)
			mu.Unlock()
			return host != "chatgpt.com" && a.String() == "193.233.112.68" && provider == "good"
		},
	}
	var last, total int
	o.Progress = func(done, all int) { last, total = done, all }
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Claude", "ChatGPT"}; !reflect.DeepEqual(res[0].Services, want) {
		t.Errorf("Services = %v, want %v", res[0].Services, want)
	}
	if want := []string{"ChatGPT"}; !reflect.DeepEqual(res[0].Unreachable, want) {
		t.Errorf("Unreachable = %v, want %v", res[0].Unreachable, want)
	}
	for _, h := range tested {
		if h == "notion.so" {
			t.Error("an unsupported service must not be probed")
		}
	}
	if want := 4 + 3*4 + 3; total != want || last != want { // hosts + repeat*hosts + services
		t.Errorf("progress ended at %d of %d, want %d", last, total, want)
	}
}

// TestRunRefUnavailable: when the ordinary DNS answers nothing, a proxy address
// cannot be told from a real one, so the run is refused instead of guessing.
func TestRunRefUnavailable(t *testing.T) {
	_, err := Run(context.Background(), Options{
		Services:  []config.Service{{Name: "x", Hosts: []string{"a.test", "b.test", "c.test"}}},
		Providers: []config.Provider{{ID: "p"}},
		Repeat:    1,
		Workers:   2,
		Ref:       func(context.Context, string) []netip.Addr { return nil },
		Ask: func(_ context.Context, _ config.Provider, h string) (*dns.Msg, error) {
			return reply(h, "193.233.112.68"), nil
		},
	})
	if err == nil {
		t.Fatal("a run without any reference answer must fail")
	}
}

// reachScenario runs three rounds where ChatGPT's host gets no answer in the last
// one (failAll: nobody does) and reports what Reach was asked and the result.
func reachScenario(t *testing.T, failAll bool) (ProviderResult, map[string]string) {
	t.Helper()
	var mu sync.Mutex
	calls := map[string]int{}
	tested := map[string]string{}
	res, err := Run(context.Background(), Options{
		Services: []config.Service{
			{Name: "Claude", Hosts: []string{"claude.ai", "api.anthropic.com"}},
			{Name: "ChatGPT", Hosts: []string{"chatgpt.com"}},
		},
		Extra:     []string{"e1.test", "e2.test", "e3.test"},
		Providers: []config.Provider{{ID: "p"}},
		Repeat:    3,
		Workers:   2,
		Ref:       func(context.Context, string) []netip.Addr { return ip("93.184.216.34") },
		Ask: func(_ context.Context, _ config.Provider, h string) (*dns.Msg, error) {
			mu.Lock()
			calls[h]++
			n := calls[h]
			mu.Unlock()
			if n >= 3 && (failAll || h == "chatgpt.com") {
				return nil, context.DeadlineExceeded
			}
			return reply(h, "193.233.112.68"), nil
		},
		Reach: func(_ context.Context, _, host string, a netip.Addr) bool {
			mu.Lock()
			tested[host] = a.String()
			mu.Unlock()
			return true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return res[0], tested
}

// TestRunReachSpareAddress: the last round has no answer for chatgpt.com, but the
// proxy is chosen by name, so another address of the pool is used for the handshake.
func TestRunReachSpareAddress(t *testing.T) {
	r, tested := reachScenario(t, false)
	if tested["chatgpt.com"] != "193.233.112.68" {
		t.Fatalf("the handshake for chatgpt.com must use a pool address: %v", tested)
	}
	if len(r.Unchecked) != 0 || len(r.Unreachable) != 0 {
		t.Fatalf("unchecked %v, unreachable %v", r.Unchecked, r.Unreachable)
	}
}

// TestRunReachUnchecked: a service that was proxied in earlier rounds but has no
// address at all in the last one is reported as not checked, not as reachable.
func TestRunReachUnchecked(t *testing.T) {
	r, tested := reachScenario(t, true)
	if len(tested) != 0 {
		t.Fatalf("nothing could be tried, yet Reach was called: %v", tested)
	}
	if len(r.Unchecked) != 2 || len(r.Unreachable) != 0 {
		t.Fatalf("unchecked %v, unreachable %v", r.Unchecked, r.Unreachable)
	}
}
