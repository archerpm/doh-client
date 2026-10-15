package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/doh"
	"github.com/archerpm/doh-client/internal/probe"
	"github.com/archerpm/doh-client/internal/resolver"
	"github.com/miekg/dns"
)

// ErrBypassActive: provider tests build their own short-lived connections and
// must not run next to the working tunnel.
var ErrBypassActive = errors.New("обход включён: выключите его, чтобы проверить провайдеров")

// ProviderTest is one provider's bypass verdict from the Providers tab test.
type ProviderTest struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Geo      bool   `json:"geo"`
	Status   string `json:"status"` // works | fails | unreachable | error | unknown
	Works    bool   `json:"works"`
	Detail   string `json:"detail"`
	MS       int64  `json:"ms"` // median latency of the answered probes
}

// providerTestMinProxied: how many canary domains must resolve to addresses
// that differ from ordinary DNS for the bypass to count as working.
const providerTestMinProxied = 2

// newProbeUpstream builds the throw-away upstream of one test (replaceable in tests).
var newProbeUpstream = func(p config.Provider, boot *doh.Bootstrap, timeout time.Duration) (resolver.Exchanger, error) {
	return doh.New(doh.Options{ID: p.ID, URL: p.URL, Bootstrap: p.Bootstrap, UseGET: p.UseGET, Resolver: boot, Timeout: timeout})
}

// probeReach checks that the address a provider returned really accepts a TLS
// connection for the domain (replaceable in tests). A DNS answer that points at a
// proxy this network cannot reach is not a working bypass.
var probeReach = func(ctx context.Context, provider, domain string, ip netip.Addr) probe.Connection {
	return probe.Connect(ctx, &net.Dialer{}, nil, provider, domain, ip)
}

// canaryOutcome is the result of asking one provider for one canary domain.
type canaryOutcome struct {
	Domain string
	Kind   string // bypass | unreachable | real | blocked | error | noref
	Err    string
	MS     int64
	Addrs  []netip.Addr
}

// checkReach turns a "bypass" outcome into "unreachable" when none of the
// returned addresses (at most two per family) completes a TLS handshake.
func checkReach(ctx context.Context, timeout time.Duration, provider string, o canaryOutcome) canaryOutcome {
	if o.Kind != "bypass" {
		return o
	}
	var strs []string
	for _, a := range o.Addrs {
		strs = append(strs, a.String())
	}
	var lastErr string
	for _, ip := range probe.Addresses(strs) {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		c := probeReach(cctx, provider, o.Domain, ip)
		cancel()
		if c.TLS == "ok" {
			return o
		}
		lastErr = fmt.Sprintf("%s: %s", ip, c.Error)
	}
	o.Kind, o.Err = "unreachable", lastErr
	return o
}

// classifyCanary compares the provider's answer with the UDP reference set.
func classifyCanary(domain string, ref map[netip.Addr]bool, addrs []netip.Addr, rcode int, err error, ms int64) canaryOutcome {
	o := canaryOutcome{Domain: domain, MS: ms, Addrs: addrs}
	switch {
	case err != nil:
		o.Kind, o.Err = "error", err.Error()
		return o
	case rcode != dns.RcodeSuccess:
		o.Kind, o.Err = "error", dns.RcodeToString[rcode]
		return o
	case len(addrs) == 0:
		o.Kind, o.Err = "error", "пустой ответ"
		return o
	}
	for _, a := range addrs {
		if a.IsUnspecified() {
			o.Kind = "blocked"
			return o
		}
	}
	if len(ref) == 0 {
		o.Kind = "noref"
		return o
	}
	for _, a := range addrs {
		if ref[a] {
			o.Kind = "real"
			return o
		}
	}
	o.Kind = "bypass"
	return o
}

// summarizeProvider turns the canary outcomes into the provider's verdict.
func summarizeProvider(outcomes []canaryOutcome) (status, detail string, ms int64) {
	var proxied, unreachable, real, blocked, errs, noref []string
	var lat []int64
	var firstErr, firstUnreachable string
	for _, o := range outcomes {
		switch o.Kind {
		case "bypass":
			proxied = append(proxied, o.Domain)
		case "unreachable":
			unreachable = append(unreachable, o.Domain)
			if firstUnreachable == "" {
				firstUnreachable = o.Err
			}
		case "real":
			real = append(real, o.Domain)
		case "blocked":
			blocked = append(blocked, o.Domain)
		case "noref":
			noref = append(noref, o.Domain)
		default:
			errs = append(errs, o.Domain)
			if firstErr == "" {
				firstErr = o.Err
			}
		}
		if o.Kind != "error" {
			lat = append(lat, o.MS)
		}
	}
	if len(lat) > 0 {
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		ms = lat[len(lat)/2]
	}
	total := len(outcomes)
	switch {
	case len(errs) == total:
		return "error", "нет ответа: " + firstErr, ms
	case len(noref) == total-len(errs) && len(proxied) == 0:
		return "unknown", "не удалось получить эталонные адреса обычного DNS", ms
	case len(proxied) >= providerTestMinProxied:
		return "works", fmt.Sprintf("адреса прокси отдал для %d из %d доменов, TLS-соединение установлено: %s", len(proxied), total, strings.Join(proxied, ", ")), ms
	case len(proxied)+len(unreachable) >= providerTestMinProxied:
		return "unreachable", fmt.Sprintf("провайдер отдаёт адреса прокси (%d из %d доменов), но соединение с ними из вашей сети не устанавливается: %s (%s)", len(proxied)+len(unreachable), total, strings.Join(unreachable, ", "), firstUnreachable), ms
	}
	parts := []string{fmt.Sprintf("обход подтверждён только для %d из %d доменов", len(proxied), total)}
	if len(unreachable) > 0 {
		parts = append(parts, "прокси недоступен: "+strings.Join(unreachable, ", "))
	}
	if len(real) > 0 {
		parts = append(parts, "настоящие адреса: "+strings.Join(real, ", "))
	}
	if len(blocked) > 0 {
		parts = append(parts, "0.0.0.0: "+strings.Join(blocked, ", "))
	}
	if len(errs) > 0 {
		parts = append(parts, "ошибки: "+strings.Join(errs, ", ")+" ("+firstErr+")")
	}
	return "fails", strings.Join(parts, "; "), ms
}

// declaredBypass: addresses that merely differ from ordinary DNS prove a proxy
// only for a provider declared as a bypass one; for the others they are most
// likely CDN geo-DNS (the same reason TestDomain only calls them "differs").
func declaredBypass(geo bool, status, detail string) (string, string) {
	if geo || status != "works" {
		return status, detail
	}
	return "fails", "ответы отличаются от обычного DNS, но провайдер не заявлен как обходной (это может быть гео-DNS CDN): " + detail
}

// TestProviders checks every configured provider against the canary domains.
// It only runs while the bypass is off.
func (d *Daemon) TestProviders(ctx context.Context) ([]ProviderTest, error) {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	if d.active {
		return nil, ErrBypassActive
	}
	cfg := d.config().Clone()
	boot := doh.NewBootstrap(cfg.BootstrapDNS)

	refs := make([]map[netip.Addr]bool, len(canaries))
	var rwg sync.WaitGroup
	for i, c := range canaries {
		rwg.Add(1)
		go func() { defer rwg.Done(); refs[i] = d.referenceSet(ctx, cfg, c) }()
	}
	rwg.Wait()

	res := make([]ProviderTest, len(cfg.Providers))
	var wg sync.WaitGroup
	for i, p := range cfg.Providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := ProviderTest{Provider: p.ID, Name: p.Name, Geo: p.Geo}
			ex, err := newProbeUpstream(p, boot, cfg.Timeout())
			if err != nil {
				r.Status, r.Detail = "error", "не удалось подготовить соединение: "+err.Error()
				res[i] = r
				return
			}
			if c, ok := ex.(interface{ Close() }); ok {
				defer c.Close()
			}
			outcomes := make([]canaryOutcome, len(canaries))
			var cwg sync.WaitGroup
			for j, dom := range canaries {
				cwg.Add(1)
				go func() {
					defer cwg.Done()
					q := new(dns.Msg)
					q.SetQuestion(dns.Fqdn(dom), dns.TypeA)
					qctx, cancel := context.WithTimeout(ctx, cfg.Timeout())
					defer cancel()
					start := time.Now()
					m, err := ex.Exchange(qctx, q)
					ms := time.Since(start).Milliseconds()
					var addrs []netip.Addr
					rcode := dns.RcodeSuccess
					if m != nil {
						addrs, rcode = resolver.AddrsOf(m), m.Rcode
					} else if err == nil {
						err = errors.New("пустой ответ")
					}
					outcomes[j] = checkReach(ctx, cfg.Timeout(), p.ID, classifyCanary(dom, refs[j], addrs, rcode, err, ms))
				}()
			}
			cwg.Wait()
			r.Status, r.Detail, r.MS = summarizeProvider(outcomes)
			r.Status, r.Detail = declaredBypass(p.Geo, r.Status, r.Detail)
			r.Works = r.Status == "works"
			res[i] = r
		}()
	}
	wg.Wait()
	return res, nil
}
