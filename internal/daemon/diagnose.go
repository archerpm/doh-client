package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/hosts"
	"github.com/archerpm/doh-client/internal/probe"
	"github.com/archerpm/doh-client/internal/resolver"
	"github.com/archerpm/doh-client/internal/sysdns"
	"github.com/miekg/dns"
)

const diagnoseBudget = 12 * time.Second

type DiagnoseStage struct {
	ID     string `json:"id"`
	Status string `json:"status"` // ok, failed, partial, skipped
	Data   any    `json:"data"`
}
type DiagnoseResult struct {
	Domain  string          `json:"domain"`
	Stages  []DiagnoseStage `json:"stages"`
	Verdict probe.Verdict   `json:"verdict"`
}
type DiagnoseRule struct {
	Matched    string      `json:"matched"`
	Mode       config.Mode `json:"mode"`
	Active     bool        `json:"active"`
	BlockAAAA  bool        `json:"block_aaaa"`
	BlockHTTPS bool        `json:"block_https"`
	HostsExact bool        `json:"hosts_exact"`
	HostsStale bool        `json:"hosts_stale,omitempty"` // hosts hold addresses the provider no longer returns
}
type DiagnoseDNS struct {
	Providers []probe.Answer `json:"providers"`
}
type DiagnoseOSDNS struct {
	sysdns.Inspection
	Addrs        []string `json:"addrs"`
	LookupStatus string   `json:"lookup_status"`
	LookupError  string   `json:"lookup_error,omitempty"`
}
type DiagnoseConnect struct {
	Probes []probe.Connection `json:"probes"`
}

// diagnoseDeps keeps all I/O injectable without changing daemon construction.
type diagnoseDeps struct {
	resolve func(context.Context, config.Provider, string, uint16) (*dns.Msg, time.Duration, error)
	inspect func(context.Context, []string) sysdns.Inspection
	lookup  func(context.Context, string) ([]netip.Addr, error)
	dialer  probe.Dialer
	tls     *tls.Config
}

// Diagnose observes rules, live DNS and transport. It never applies settings.
func (d *Daemon) Diagnose(ctx context.Context, domain string) (DiagnoseResult, error) {
	deps := diagnoseDeps{resolve: d.router.ResolveProvider, inspect: sysdns.Inspect,
		lookup: func(ctx context.Context, name string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", name)
		}, dialer: &net.Dialer{}}
	return d.diagnose(ctx, domain, deps)
}

func (d *Daemon) diagnose(ctx context.Context, domain string, deps diagnoseDeps) (DiagnoseResult, error) {
	dom, err := config.NormalizeDomain(domain)
	if err != nil {
		return DiagnoseResult{}, fmt.Errorf("некорректный домен: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, diagnoseBudget)
	defer cancel()
	d.mu.Lock()
	if d.cfg == nil {
		d.mu.Unlock()
		return DiagnoseResult{}, errors.New("диагностика недоступна: служба не настроена")
	}
	cfg, active, mode := d.cfg.Clone(), d.active, d.activeMode
	d.mu.Unlock()
	if !active {
		mode = cfg.Mode
	}
	explanation := d.router.Explain(dom)
	rule := DiagnoseRule{Matched: explanation.Matched, Mode: mode, Active: active,
		BlockAAAA: explanation.BlockAAAA && mode == config.ModeProxy, BlockHTTPS: explanation.BlockHTTPS && mode == config.ModeProxy}
	if mode == config.ModeHosts {
		rule.HostsExact = slices.Contains(hosts.Managed(d.hostsPath), dom)
	}
	matched := rule.Matched != ""
	if mode == config.ModeHosts {
		matched = matched && rule.HostsExact
	}
	expected := []string{}
	if active && mode == config.ModeProxy {
		expected, _ = sysServers(d.srv.Addrs())
	}
	osData := DiagnoseOSDNS{Inspection: sysdns.Inspection{Expected: expected, Actual: []string{}, Interfaces: []sysdns.DNSInterface{}}, Addrs: []string{}, LookupStatus: "pending"}
	answers := []probe.Answer{}
	for _, id := range explanation.Effective {
		for _, typ := range []string{"A", "AAAA"} {
			answers = append(answers, probe.Answer{Provider: id, Type: typ, Addrs: []string{}, Status: "pending"})
		}
	}
	connections := []probe.Connection{}
	type event struct {
		kind       string
		index      int
		answer     probe.Answer
		connection probe.Connection
		inspection sysdns.Inspection
		ips        []netip.Addr
		err        error
	}
	// Enough space for every worker to finish after the caller's deadline.
	events := make(chan event, len(answers)*3+2)
	remaining := 2
	go func() {
		c, stop := context.WithTimeout(ctx, 4*time.Second)
		defer stop()
		events <- event{kind: "inspect", inspection: deps.inspect(c, expected)}
	}()
	go func() {
		c, stop := context.WithTimeout(ctx, 4*time.Second)
		defer stop()
		ips, err := deps.lookup(c, dom)
		events <- event{kind: "lookup", ips: ips, err: err}
	}()
	for i, a := range answers {
		p, ok := cfg.Provider(a.Provider)
		if !ok {
			answers[i].Status = "failed"
			answers[i].Error = "провайдер отсутствует в настройках"
			continue
		}
		remaining++
		go func(i int, a probe.Answer, p config.Provider) {
			c, stop := context.WithTimeout(ctx, 4*time.Second)
			defer stop()
			typ := uint16(dns.TypeA)
			if a.Type == "AAAA" {
				typ = dns.TypeAAAA
			}
			m, lat, err := deps.resolve(c, p, dom, typ)
			a.MS = lat.Milliseconds()
			a.Status = "ok"
			if err != nil {
				a.Status = "failed"
				if ctx.Err() != nil {
					a.Status = "pending"
				}
				a.Error = "ошибка DNS: " + err.Error()
			} else if m == nil {
				a.Status = "failed"
				a.Error = "провайдер не вернул DNS-ответ"
			} else {
				a.Rcode = dns.RcodeToString[m.Rcode]
				ips := resolver.AddrsOf(m)
				a.Blocked = len(ips) > 0
				for _, ip := range ips {
					a.Addrs = append(a.Addrs, ip.String())
					if !ip.IsUnspecified() {
						a.Blocked = false
					}
				}
			}
			events <- event{kind: "dns", index: i, answer: a}
		}(i, a, p)
	}
	sem := make(chan struct{}, 4)
	selected := map[string]map[netip.Addr]bool{}
	receive := func(ev event) {
		remaining--
		switch ev.kind {
		case "inspect":
			osData.Inspection = ev.inspection
		case "lookup":
			osData.LookupStatus = "ok"
			if ev.err != nil {
				osData.LookupStatus = "failed"
				osData.LookupError = "ошибка системного resolver: " + ev.err.Error()
			}
			for _, ip := range ev.ips {
				osData.Addrs = append(osData.Addrs, ip.String())
			}
		case "connect":
			connections[ev.index] = ev.connection
		case "dns":
			answers[ev.index] = ev.answer
			if ev.answer.Status != "ok" || ev.answer.Rcode != "NOERROR" || ev.answer.Blocked {
				break
			}
			id := ev.answer.Provider
			if selected[id] == nil {
				selected[id] = map[netip.Addr]bool{}
			}
			for _, ip := range probe.Addresses(ev.answer.Addrs) {
				if selected[id][ip] {
					continue
				}
				familyCount := 0
				for prior := range selected[id] {
					if prior.Is4() == ip.Is4() {
						familyCount++
					}
				}
				if familyCount >= 2 {
					continue
				}
				selected[id][ip] = true
				index := len(connections)
				connections = append(connections, probe.Connection{Provider: id, IP: ip.String(), TCP: "pending", TLS: "skipped"})
				remaining++
				go func(index int, ip netip.Addr) {
					select {
					case sem <- struct{}{}:
					case <-ctx.Done():
						return
					}
					defer func() { <-sem }()
					c, stop := context.WithTimeout(ctx, 3*time.Second)
					defer stop()
					result := probe.Connect(c, deps.dialer, deps.tls, id, dom, ip)
					// An individual attempt timeout is a transport failure; the total
					// deadline leaves a pending probe for a partial report.
					if ctx.Err() == nil {
						if result.TCP == "pending" {
							result.TCP = "failed"
						}
						if result.TLS == "pending" {
							result.TLS = "failed"
						}
					}
					events <- event{kind: "connect", index: index, connection: result}
				}(index, ip)
			}
		}
	}
collecting:
	for remaining > 0 {
		select {
		case ev := <-events:
			receive(ev)
		case <-ctx.Done():
			break collecting
		}
	}
	// Consume already finished work without extending the total budget.
draining:
	for {
		select {
		case ev := <-events:
			receive(ev)
		default:
			break draining
		}
	}
	routed := probe.Routed(explanation.Effective, answers, "A", rule.Matched != "")
	if !rule.BlockAAAA && mode != config.ModeHosts {
		routed = append(routed, probe.Routed(explanation.Effective, answers, "AAAA", rule.Matched != "")...)
	}
	// Hosts mode: applications use the addresses written to hosts, not the fresh
	// DoH answers. If the system lookup shares none of the fresh addresses, the
	// entry is stale and a successful probe of the fresh addresses proves nothing.
	if mode == config.ModeHosts && rule.HostsExact && osData.LookupStatus == "ok" && len(osData.Addrs) > 0 {
		fresh := map[string]bool{}
		for _, a := range routed {
			if a.Status == "ok" && !a.Blocked {
				for _, s := range a.Addrs {
					fresh[s] = true
				}
			}
		}
		if len(fresh) > 0 && !slices.ContainsFunc(osData.Addrs, func(s string) bool { return fresh[s] }) {
			rule.HostsStale = true
			matched = false
		}
	}
	evidence := probe.Evidence{Matched: matched, OSMismatch: osData.Mismatch, Answers: routed}
	for _, c := range connections {
		for _, a := range routed {
			if a.Status == "ok" && !a.Blocked && a.Provider == c.Provider && slices.Contains(a.Addrs, c.IP) {
				evidence.Connections = append(evidence.Connections, c)
				break
			}
		}
	}
	ruleStatus := "ok"
	if !matched {
		ruleStatus = "failed"
	}
	chainStatus := "ok"
	if len(explanation.Effective) == 0 {
		chainStatus = "failed"
	}
	dnsStatus := "ok"
	for _, a := range answers {
		if a.Status == "pending" {
			dnsStatus = "partial"
			break
		}
		if a.Status != "ok" || (a.Rcode != "NOERROR" && a.Rcode != "NXDOMAIN") {
			dnsStatus = "failed"
		}
	}
	osStatus := "ok"
	if !osData.Complete || osData.LookupStatus == "pending" {
		osStatus = "partial"
	} else if osData.Mismatch || osData.LookupStatus == "failed" {
		osStatus = "failed"
	}
	connectStatus := "skipped"
	if len(connections) > 0 {
		connectStatus = "ok"
		for _, c := range connections {
			if c.TCP == "pending" || c.TLS == "pending" {
				connectStatus = "partial"
				break
			}
			if c.TLS != "ok" {
				connectStatus = "failed"
			}
		}
		if connectStatus == "failed" {
			for _, c := range connections {
				if c.TLS == "ok" { // some addresses work: not a failure of the whole stage
					connectStatus = "partial"
					break
				}
			}
		}
	}
	return DiagnoseResult{Domain: dom, Stages: []DiagnoseStage{
		{"rule", ruleStatus, rule}, {"chain", chainStatus, explanation}, {"dns", dnsStatus, DiagnoseDNS{answers}},
		{"os_dns", osStatus, osData}, {"connect", connectStatus, DiagnoseConnect{connections}},
	}, Verdict: probe.Decide(evidence)}, nil
}
