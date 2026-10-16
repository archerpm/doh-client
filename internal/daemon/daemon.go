// Package daemon ties the resolver, DNS server, system DNS and hosts management together.
package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/conflict"
	"github.com/archerpm/doh-client/internal/hosts"
	"github.com/archerpm/doh-client/internal/resolver"
	"github.com/archerpm/doh-client/internal/server"
	"github.com/archerpm/doh-client/internal/sysdns"
	"github.com/miekg/dns"
)

const (
	ensureEvery       = 20 * time.Second
	hostsRefreshEvery = 6 * time.Hour
)

// systemDNS allows daemon tests to exercise DNS operations without changing the OS.
type systemDNS interface {
	Apply([]string) error
	Restore() error
	HasBackup() bool
	Status() sysdns.Status
	FlushOSCache()
}

// Daemon is the long-running core.
type Daemon struct {
	store     *config.Store
	router    *resolver.Router
	srv       *server.Server
	sys       systemDNS
	hostsPath string

	events       eventBuffer
	healthEvents transitionState                                   // guarded by opMu
	inspectDNS   func(context.Context, []string) sysdns.Inspection // set before maintenance starts; nil uses sysdns.Inspect
	conflicts    *conflict.Detector                                // nil: no check for DNS-taking programs

	opMu             sync.Mutex // serializes enable/disable/config/maintenance (may block on I/O)
	mu               sync.Mutex // guards the fields below; held only briefly
	cfg              *config.Config
	active           bool
	activeMode       config.Mode
	activeGeneration uint64    // changes on enable/disable, including restarts in the same mode
	lastDNSCheck     time.Time // read under mu; written under both opMu and mu
	lastErr          string
	warnings         []string
	hostsSet         []string
	started          time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	svcTest serviceTest // the service check of the Providers tab

	poolMu    sync.Mutex
	poolCache map[string]*proxyPool
}

// New builds a daemon using the given store and DNS backup path.
func New(store *config.Store, backupPath string) *Daemon {
	r := resolver.New()
	d := &Daemon{store: store, router: r, srv: server.New(r), sys: sysdns.New(backupPath), hostsPath: hosts.Path(),
		conflicts: conflict.NewDetector(conflictScanEvery)}
	if store != nil {
		d.svcTest.path = filepath.Join(filepath.Dir(store.Path()), serviceTestFile)
	}
	return d
}

// Router exposes the router (diagnostics API).
func (d *Daemon) Router() *resolver.Router { return d.router }

// Start loads config, recovers from crashes and enables the proxy if it was enabled.
func (d *Daemon) Start() error {
	cfg, err := d.store.Load()
	if err != nil {
		log.Printf("config: %v — using defaults", err)
		cfg = config.Default()
		if err2 := cfg.Normalize(); err2 != nil {
			return err2
		}
	}
	if err := d.router.Apply(cfg); err != nil {
		return err
	}
	d.mu.Lock()
	d.cfg = cfg
	d.started = time.Now()
	d.ctx, d.cancel = context.WithCancel(context.Background())
	d.mu.Unlock()
	d.svcTest.load()

	// Crash recovery: a leftover backup while disabled means we died with DNS redirected.
	recoverFailed := false
	if !cfg.Enabled && d.sys.HasBackup() {
		log.Printf("restoring system DNS left over from previous run")
		if err := d.sys.Restore(); err != nil {
			// The OS still points at us: serve DNS anyway rather than leave it dead.
			log.Printf("restore: %v — keeping the resolver running", err)
			d.setErr("не удалось вернуть исходный DNS: " + err.Error())
			recoverFailed = true
		}
	}
	if !cfg.Enabled && len(hosts.Managed(d.hostsPath)) > 0 {
		_ = hosts.Write(d.hostsPath, nil)
	}
	// Died in the middle of a mode switch: leftovers of the other mode must not
	// outlive the start. Hosts mode needs no resident DNS, so a leftover DNS
	// backup means the OS still points at a server that is not running.
	if cfg.Enabled {
		switch cfg.Mode {
		case config.ModeHosts:
			if d.sys.HasBackup() {
				log.Printf("restoring system DNS left over from proxy mode")
				if err := d.sys.Restore(); err != nil {
					log.Printf("restore: %v", err)
					d.setErr("не удалось вернуть исходный DNS: " + err.Error())
				}
			}
		default:
			if len(hosts.Managed(d.hostsPath)) > 0 {
				if err := hosts.Write(d.hostsPath, nil); err != nil {
					log.Printf("hosts cleanup: %v", err)
				}
			}
		}
	}

	d.wg.Add(2)
	go func() {
		defer d.wg.Done()
		d.router.RunHealth(d.ctx, func() time.Duration { return time.Duration(d.config().HealthIntervalS) * time.Second })
	}()
	go func() { defer d.wg.Done(); d.maintain() }()

	if cfg.Enabled || recoverFailed {
		d.opMu.Lock()
		err := d.enableLocked()
		if err != nil {
			log.Printf("enable on start: %v", err)
			// Don't leave the OS pointing at a resolver that isn't running
			// (e.g. after a crash, with port 53 now taken by another program).
			if d.sys.HasBackup() {
				if rerr := d.sys.Restore(); rerr != nil {
					log.Printf("restore after failed start: %v", rerr)
				}
			}
		}
		d.opMu.Unlock()
	}
	return nil
}

// Stop disables everything without persisting Enabled=false, so a reboot re-enables.
func (d *Daemon) Stop() {
	d.mu.Lock()
	if d.cancel != nil {
		d.cancel() // stop maintenance first so it cannot re-apply DNS after restore
	}
	d.mu.Unlock()
	d.wg.Wait()
	d.opMu.Lock()
	if err := d.disableLocked(); err != nil {
		// Process is exiting anyway; the backup stays on disk and is applied by
		// the next start (crash recovery) or `restore-dns`.
		d.srv.Stop()
	}
	d.opMu.Unlock()
}

func (d *Daemon) config() *config.Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg
}

// SetEnabled switches the proxy on or off and persists the choice.
func (d *Daemon) SetEnabled(on bool) error {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	if d.ctx != nil && d.ctx.Err() != nil {
		return fmt.Errorf("демон остановлен: %w", d.ctx.Err())
	}
	var err error
	if on {
		err = d.enableLocked()
	} else {
		err = d.disableLocked()
	}
	if err != nil {
		return err
	}
	c := d.config().Clone()
	c.Enabled = on
	if err := d.store.Save(c); err != nil {
		if on {
			_ = d.disableLocked() // keep running state and persisted state consistent
			d.recordEnableFailure()
		}
		return fmt.Errorf("не удалось сохранить состояние: %w", err)
	}
	d.setCfg(c)
	return nil
}

func (d *Daemon) setCfg(c *config.Config) {
	d.mu.Lock()
	d.cfg = c
	d.mu.Unlock()
}

func (d *Daemon) setErr(e string) {
	d.mu.Lock()
	d.lastErr = e
	d.mu.Unlock()
}

// UpdateConfig validates, persists and applies a new config.
func (d *Daemon) UpdateConfig(c *config.Config) error {
	c = c.Clone()
	if err := c.Normalize(); err != nil {
		return err
	}
	d.opMu.Lock()
	defer d.opMu.Unlock()
	if d.ctx != nil && d.ctx.Err() != nil {
		return fmt.Errorf("демон остановлен: %w", d.ctx.Err())
	}
	old := d.config()
	c.Enabled = old.Enabled // enabling goes through SetEnabled
	d.mu.Lock()
	active, mode := d.active, d.activeMode
	d.mu.Unlock()
	needRestart := active && (mode != c.Mode || !reflect.DeepEqual(old.Listen, c.Listen))
	var restoreHosts func() error
	if active && mode == config.ModeHosts {
		content, err := os.ReadFile(d.hostsPath)
		existed := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("hosts snapshot: %w", err)
		}
		perm := os.FileMode(0o644)
		if info, err := os.Stat(d.hostsPath); err == nil {
			perm = info.Mode().Perm()
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("hosts snapshot mode: %w", err)
		}
		restoreHosts = func() error {
			current, err := os.ReadFile(d.hostsPath)
			if err == nil && existed && bytes.Equal(current, content) {
				return nil
			}
			if !existed {
				err = os.Remove(d.hostsPath)
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
			} else {
				err = os.WriteFile(d.hostsPath, content, perm)
			}
			if err != nil {
				return fmt.Errorf("hosts rollback: %w", err)
			}
			d.sys.FlushOSCache()
			return nil
		}
	}
	rollback := func(cause error) error {
		applyErr := d.router.Apply(old)
		saveErr := d.store.Save(old)
		d.setCfg(old)
		var hostsErr error
		if restoreHosts != nil {
			hostsErr = restoreHosts()
		}
		return errors.Join(cause, applyErr, saveErr, hostsErr)
	}
	if err := d.router.Apply(c); err != nil { // validate upstreams before persisting
		return err
	}
	if err := d.store.Save(c); err != nil {
		return rollback(err)
	}
	d.setCfg(c)
	if needRestart {
		if err := d.disableLocked(); err != nil {
			// still running in the old mode: settings, router and status must say so
			return rollback(err)
		}
		if err := d.enableLocked(); err != nil {
			// New listen addresses / mode don't work: go back to what did.
			rerr := rollback(err)
			return errors.Join(rerr, d.enableLocked())
		}
	} else if active && mode == config.ModeHosts {
		if err := d.writeHostsLocked(); err != nil {
			return rollback(err)
		}
	}
	if old.ActiveProvider != c.ActiveProvider {
		d.healthEvents.resetProvider()
	}
	return nil
}

// ReapplyDNS repairs system DNS for the running proxy without disabling it.
func (d *Daemon) ReapplyDNS() error {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	if d.ctx != nil && d.ctx.Err() != nil {
		return fmt.Errorf("демон остановлен: %w", d.ctx.Err())
	}
	d.mu.Lock()
	active, mode := d.active, d.activeMode
	d.mu.Unlock()
	if !active {
		return errors.New("обход выключен — повторное применение DNS недоступно")
	}
	if mode != config.ModeProxy {
		return errors.New("повторное применение DNS доступно только в режиме proxy")
	}
	servers, ok := sysServers(d.srv.Addrs())
	if !ok {
		return errors.New("локальный DNS не слушает порт 53 — системный DNS не переключён")
	}
	if err := d.sys.Apply(servers); err != nil {
		d.setErr("системный DNS: " + err.Error())
		return fmt.Errorf("системный DNS: %w", err)
	}
	d.mu.Lock()
	if strings.HasPrefix(d.lastErr, "системный DNS") {
		d.lastErr = ""
	}
	d.mu.Unlock()
	return nil
}

// Config returns a copy of the current config.
func (d *Daemon) Config() *config.Config { return d.config().Clone() }

func sysServers(listen []string) ([]string, bool) {
	var out []string
	for _, a := range listen {
		ap, err := netip.ParseAddrPort(a)
		if err != nil || ap.Port() != 53 {
			continue // OS resolvers can only use port 53
		}
		out = append(out, ap.Addr().String())
	}
	return out, len(out) > 0
}

// enableLocked requires opMu.
func (d *Daemon) enableLocked() (err error) {
	defer func() {
		if err != nil {
			d.recordEnableFailure()
		}
	}()
	cfg := d.config()
	d.mu.Lock()
	if d.active {
		d.mu.Unlock()
		return nil
	}
	d.lastErr, d.warnings = "", nil
	d.mu.Unlock()
	switch cfg.Mode {
	case config.ModeHosts:
		if err := d.writeHostsLocked(); err != nil {
			d.setErr(err.Error())
			return err
		}
	default:
		if err := d.srv.Start(cfg.Listen); err != nil {
			d.setErr(err.Error())
			return err
		}
		servers, ok := sysServers(d.srv.Addrs())
		if !ok {
			d.mu.Lock()
			d.warnings = append(d.warnings, "порт не 53 — системный DNS не переключён, используйте адрес вручную")
			d.mu.Unlock()
		} else if err := d.sys.Apply(servers); err != nil {
			// Keep running: some interfaces may have been switched. Report it.
			d.setErr("системный DNS: " + err.Error())
			d.recordEnableFailure()
			log.Printf("sysdns apply: %v", err)
		}
	}
	d.mu.Lock()
	d.active, d.activeMode = true, cfg.Mode
	d.activeGeneration++
	d.mu.Unlock()
	d.healthEvents.snapshot.DNSKnown = false
	return nil
}

// disableLocked requires opMu. If the system DNS cannot be restored, the local
// server keeps running: the OS still points at 127.0.0.1 and must keep resolving.
func (d *Daemon) disableLocked() error {
	if d.sys.HasBackup() {
		if err := d.sys.Restore(); err != nil {
			msg := "не удалось вернуть исходный DNS, клиент остаётся включённым: " + err.Error()
			d.setErr(msg)
			log.Printf("sysdns restore: %v", err)
			return errors.New(msg)
		}
	}
	d.srv.Stop()
	d.mu.Lock()
	hadHosts := d.activeMode == config.ModeHosts || len(d.hostsSet) > 0
	d.mu.Unlock()
	if hadHosts {
		if err := hosts.Write(d.hostsPath, nil); err != nil {
			// entries are still in hosts and still steer traffic: stay "active" so the
			// user (or the next disable) retries instead of forgetting them
			msg := "не удалось очистить файл hosts, клиент остаётся включённым: " + err.Error()
			d.setErr(msg)
			log.Printf("hosts cleanup: %v", err)
			return errors.New(msg)
		}
		d.sys.FlushOSCache()
	}
	d.mu.Lock()
	d.hostsSet = nil
	d.active = false
	d.activeGeneration++
	d.mu.Unlock()
	d.healthEvents.snapshot.DNSKnown = false
	return nil
}

// resolveRules resolves enabled rule domains in parallel (bounded) through the router.
func (d *Daemon) resolveRules(rules []config.Rule) (map[string][]netip.Addr, []string) {
	type res struct {
		dom   string
		addrs []netip.Addr
	}
	var doms []string
	for _, r := range rules {
		if r.Enabled {
			doms = append(doms, r.Domain)
		}
	}
	out := make(chan res, len(doms))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, dom := range doms {
		wg.Add(1)
		go func(dom string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			q := new(dns.Msg)
			q.SetQuestion(dns.Fqdn(dom), dns.TypeA)
			q.RecursionDesired = true
			parent := d.ctx
			if parent == nil {
				parent = context.Background()
			}
			ctx, cancel := context.WithTimeout(parent, 10*time.Second)
			defer cancel()
			var addrs []netip.Addr
			for _, a := range resolver.AddrsOf(d.router.Resolve(ctx, q, "hosts")) {
				if !a.IsUnspecified() {
					addrs = append(addrs, a)
				}
			}
			out <- res{dom, addrs}
		}(dom)
	}
	wg.Wait()
	close(out)
	entries := map[string][]netip.Addr{}
	var failed []string
	for r := range out {
		if len(r.addrs) == 0 {
			failed = append(failed, r.dom)
		} else {
			entries[r.dom] = r.addrs
		}
	}
	sort.Strings(failed)
	return entries, failed
}

// writeHostsLocked (requires opMu) resolves every enabled rule domain and writes hosts.
func (d *Daemon) writeHostsLocked() error {
	rules := d.config().Rules
	enabled := 0
	for _, r := range rules {
		if r.Enabled {
			enabled++
		}
	}
	if enabled == 0 { // no rules: hosts mode has nothing to write — remove our block
		if err := hosts.Write(d.hostsPath, nil); err != nil {
			return fmt.Errorf("hosts: %w", err)
		}
		d.mu.Lock()
		d.hostsSet, d.warnings = nil, []string{"режим hosts: правил нет — записи клиента убраны из hosts"}
		d.mu.Unlock()
		d.sys.FlushOSCache()
		return nil
	}
	entries, errs := d.resolveRules(rules)
	if len(entries) == 0 {
		return errors.New("hosts: ни один домен из правил не разрешился (добавьте правила)")
	}
	if err := hosts.Write(d.hostsPath, entries); err != nil {
		return fmt.Errorf("hosts: %w (нужны права администратора)", err)
	}
	set := make([]string, 0, len(entries))
	for k := range entries {
		set = append(set, k)
	}
	sort.Strings(set)
	warn := []string{"режим hosts покрывает только точные домены из правил, без поддоменов"}
	if len(errs) > 0 {
		warn = append(warn, "не разрешились: "+strings.Join(errs, ", "))
	}
	d.mu.Lock()
	d.hostsSet, d.warnings = set, warn
	d.mu.Unlock()
	d.sys.FlushOSCache()
	return nil
}

// maintain re-applies system DNS (new interfaces, DHCP renewals) and refreshes hosts.
func (d *Daemon) maintain() {
	t := time.NewTicker(ensureEvery)
	defer t.Stop()
	lastHosts := time.Now()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-t.C:
		}
		d.maintainTick(&lastHosts)
	}
}

// maintainTick probes live DNS without daemon locks, then serializes observation
// and repairs with enable/disable/config changes.
func (d *Daemon) maintainTick(lastHosts *time.Time) {
	d.mu.Lock()
	active, mode := d.active, d.activeMode
	generation, lastCheck := d.activeGeneration, d.lastDNSCheck
	d.mu.Unlock()
	var probe dnsProbe
	if active && mode == config.ModeProxy && time.Since(lastCheck) >= dnsInspectEvery {
		if servers, ok := sysServers(d.srv.Addrs()); ok {
			probe.at = time.Now()
			ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
			inspect := d.inspectDNS
			if inspect == nil {
				inspect = sysdns.Inspect
			}
			inspection := inspect(ctx, servers)
			cancel()
			probe.done, probe.mismatch, probe.complete = true, inspection.Mismatch, inspection.Complete
		}
	}
	d.opMu.Lock()
	defer d.opMu.Unlock()
	if d.ctx.Err() != nil { // Stop raced with the tick or inspection
		return
	}
	d.mu.Lock()
	if active != d.active || mode != d.activeMode || generation != d.activeGeneration {
		probe = dnsProbe{} // the inspection belongs to an earlier activation
	}
	active, mode = d.active, d.activeMode
	if probe.done {
		d.lastDNSCheck = probe.at
	}
	d.mu.Unlock()
	d.observeHealthEventsLocked(active, mode, probe)
	if active && mode == config.ModeProxy {
		if servers, ok := sysServers(d.srv.Addrs()); ok {
			if err := d.sys.Apply(servers); err != nil {
				d.setErr("системный DNS: " + err.Error())
			} else {
				d.mu.Lock()
				if strings.HasPrefix(d.lastErr, "системный DNS") {
					d.lastErr = "" // recovered
				}
				d.mu.Unlock()
			}
		}
	}
	if active && mode == config.ModeHosts && time.Since(*lastHosts) > hostsRefreshEvery {
		if err := d.writeHostsLocked(); err != nil {
			d.setErr(err.Error())
		}
		*lastHosts = time.Now()
	}
}

// Status is the API status payload.
type Status struct {
	Active    bool                   `json:"active"`
	Enabled   bool                   `json:"enabled"`
	Mode      config.Mode            `json:"mode"`
	Listen    []string               `json:"listen"`
	SysDNS    sysdns.Status          `json:"sysdns"`
	HostsSet  []string               `json:"hosts_domains,omitempty"`
	LastError string                 `json:"last_error,omitempty"`
	Warnings  []string               `json:"warnings,omitempty"`
	Conflicts []string               `json:"conflicts,omitempty"` // warnings about programs that take over DNS; also in Warnings
	Health    []resolver.HealthInfo  `json:"health"`
	Events    []Event                `json:"events,omitempty"`
	Stats     resolver.StatsSnapshot `json:"stats"`
	Uptime    string                 `json:"uptime"`
	Version   string                 `json:"version"`
	Build     string                 `json:"build,omitempty"` // BuildID of the running executable
	Instance  int64                  `json:"instance"`        // changes when the service restarts
}

// conflictScanEvery bounds how often the process table is walked for status polls.
const conflictScanEvery = 15 * time.Second

// Version is set by main.
var Version = "dev"

func (d *Daemon) Status() Status {
	d.mu.Lock()
	st := Status{Active: d.active, Enabled: d.cfg.Enabled, Mode: d.cfg.Mode, Listen: d.srv.Addrs(),
		HostsSet: append([]string(nil), d.hostsSet...), LastError: d.lastErr,
		Warnings: append([]string(nil), d.warnings...), Uptime: time.Since(d.started).Round(time.Second).String(), Version: Version, Build: BuildID(),
		Instance: d.started.UnixNano()}
	d.mu.Unlock()
	st.SysDNS = d.sys.Status()
	st.Warnings = append(st.Warnings, st.SysDNS.Warnings...)
	if d.conflicts != nil && st.Active && st.Mode == config.ModeProxy {
		st.Conflicts = conflict.Warnings(d.conflicts.Names())
		st.Warnings = append(st.Warnings, st.Conflicts...)
	}
	st.Health = d.router.Health()
	st.Events = d.events.Snapshot()
	st.Stats = d.router.Stats()
	return st
}

// ProbeResult is one provider's answer for the diagnostics test.
type ProbeResult struct {
	Provider       string   `json:"provider"`
	Name           string   `json:"name"`
	Geo            bool     `json:"geo"`
	Rcode          string   `json:"rcode,omitempty"`
	Addrs          []string `json:"addrs,omitempty"`
	MS             int64    `json:"ms"`
	Error          string   `json:"error,omitempty"`
	Classification string   `json:"classification"` // unknown, proxied, differs, real, blocked
	Proxied        bool     `json:"proxied"`        // answer points at this provider's known proxy addresses
	Differs        bool     `json:"differs"`        // differs from the reference resolvers but not a known proxy (CDN geo-DNS?)
	Blocked        bool     `json:"blocked"`        // 0.0.0.0 sinkhole
}

// canaries are domains every geo provider proxies; their answers reveal the proxy pool.
var canaries = []string{"chatgpt.com", "claude.ai", "grok.com"}

const proxyPoolTTL = 10 * time.Minute

type proxyPool struct {
	addrs map[netip.Addr]bool
	nets  map[netip.Prefix]bool
	at    time.Time
}

func (p *proxyPool) has(a netip.Addr) bool {
	if p == nil {
		return false
	}
	if p.addrs[a] {
		return true
	}
	if a.Is4() {
		pf, _ := a.Prefix(24)
		return p.nets[pf]
	}
	pf, _ := a.Prefix(64)
	return p.nets[pf]
}

func addPool(p *proxyPool, a netip.Addr) {
	p.addrs[a] = true
	bits := 64
	if a.Is4() {
		bits = 24
	}
	pf, _ := a.Prefix(bits)
	p.nets[pf] = true
}

// lookupA queries the installed active provider.
func (d *Daemon) lookupA(ctx context.Context, p config.Provider, name string) ([]netip.Addr, string, time.Duration, error) {
	m, lat, err := d.router.ResolveProvider(ctx, p, name, dns.TypeA)
	if err != nil {
		return nil, "", lat, err
	}
	if m == nil {
		return nil, "", lat, errors.New("empty DNS response")
	}
	return resolver.AddrsOf(m), dns.RcodeToString[m.Rcode], lat, nil
}

// referenceSet uses ordinary UDP DNS, never an additional DoH upstream.
// An empty set means the reference is unavailable and cannot support a verdict.
func (d *Daemon) referenceSet(ctx context.Context, cfg *config.Config, name string) map[netip.Addr]bool {
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout())
	defer cancel()
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), dns.TypeA)
	client := &dns.Client{Net: "udp", Timeout: cfg.Timeout()}
	for _, server := range cfg.BootstrapDNS {
		m, _, err := client.ExchangeContext(ctx, q, server)
		if err != nil || m == nil || m.Truncated || m.Rcode != dns.RcodeSuccess {
			continue
		}
		ref := map[netip.Addr]bool{}
		for _, a := range resolver.AddrsOf(m) {
			if !a.IsUnspecified() {
				ref[a] = true
			}
		}
		if len(ref) > 0 {
			return ref
		}
	}
	return nil
}

// pools learns only the active provider's proxy addresses from canary domains.
func (d *Daemon) pools(ctx context.Context, cfg *config.Config, provs []config.Provider) map[string]*proxyPool {
	d.poolMu.Lock()
	defer d.poolMu.Unlock()
	if d.poolCache == nil {
		d.poolCache = map[string]*proxyPool{}
	}
	var need []config.Provider
	for _, p := range provs {
		if pp := d.poolCache[p.ID+"|"+p.URL]; p.ID == cfg.ActiveProvider && p.Geo && (pp == nil || time.Since(pp.at) > proxyPoolTTL) {
			need = append(need, p)
		}
	}
	if len(need) > 0 {
		refs := map[string]map[netip.Addr]bool{}
		for _, c := range canaries {
			refs[c] = d.referenceSet(ctx, cfg, c)
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, p := range need {
			wg.Add(1)
			go func(p config.Provider) {
				defer wg.Done()
				pp := &proxyPool{addrs: map[netip.Addr]bool{}, nets: map[netip.Prefix]bool{}, at: time.Now()}
				for _, c := range canaries {
					addrs, rcode, _, err := d.lookupA(ctx, p, c)
					if err != nil || rcode != "NOERROR" {
						continue
					}
					disjoint := len(addrs) > 0 && len(refs[c]) > 0
					for _, a := range addrs {
						if refs[c][a] || a.IsUnspecified() {
							disjoint = false
						}
					}
					if disjoint {
						for _, a := range addrs {
							addPool(pp, a)
						}
					}
				}
				if len(pp.addrs) == 0 {
					return // nothing learned (timeout, foreign IP for dns-ai…): retry next time
				}
				mu.Lock()
				d.poolCache[p.ID+"|"+p.URL] = pp
				mu.Unlock()
			}(p)
		}
		wg.Wait()
	}
	out := map[string]*proxyPool{}
	for _, p := range provs {
		if p.ID == cfg.ActiveProvider {
			out[p.ID] = d.poolCache[p.ID+"|"+p.URL]
		}
	}
	return out
}

// TestDomain queries the active provider and compares its answer to ordinary DNS.
func (d *Daemon) TestDomain(ctx context.Context, domain string) ([]ProbeResult, error) {
	dom, err := config.NormalizeDomain(domain)
	if err != nil {
		return nil, err
	}
	d.opMu.Lock()
	defer d.opMu.Unlock()
	cfg := d.config().Clone()
	var provs []config.Provider
	for _, p := range cfg.Providers {
		if p.ID == cfg.ActiveProvider && p.Enabled {
			provs = append(provs, p)
		}
	}
	var (
		ref   map[netip.Addr]bool
		pools map[string]*proxyPool
		pwg   sync.WaitGroup
	)
	pwg.Add(2)
	go func() { defer pwg.Done(); ref = d.referenceSet(ctx, cfg, dom) }()
	go func() { defer pwg.Done(); pools = d.pools(ctx, cfg, provs) }()
	res := make([]ProbeResult, len(provs))
	var wg sync.WaitGroup
	for i, p := range provs {
		wg.Add(1)
		go func(i int, p config.Provider) {
			defer wg.Done()
			pr := ProbeResult{Provider: p.ID, Name: p.Name, Geo: p.Geo, Classification: "unknown"}
			addrs, rcode, lat, err := d.lookupA(ctx, p, dom)
			pr.MS = lat.Milliseconds()
			pr.Rcode = rcode
			if err != nil {
				pr.Error = err.Error()
			}
			for _, a := range addrs {
				pr.Addrs = append(pr.Addrs, a.String())
				if a.IsUnspecified() {
					pr.Blocked = true
				}
			}
			res[i] = pr
		}(i, p)
	}
	wg.Wait()
	pwg.Wait()
	for i, p := range provs {
		r := &res[i]
		if r.Error != "" || r.Rcode != "NOERROR" || len(r.Addrs) == 0 {
			continue
		}
		if r.Blocked {
			r.Classification = "blocked"
			continue
		}
		if len(ref) == 0 {
			continue
		}
		disjoint := true
		for _, value := range r.Addrs {
			a := netip.MustParseAddr(value)
			if p.Geo && pools[p.ID].has(a) {
				r.Proxied = true
			}
			if ref[a] {
				disjoint = false
			}
		}
		switch {
		case r.Proxied:
			r.Classification = "proxied"
		case disjoint:
			r.Differs = true
			r.Classification = "differs"
		default:
			r.Classification = "real"
		}
	}
	return res, nil
}

// FlushCache clears the resolver and OS caches.
func (d *Daemon) FlushCache() {
	d.router.FlushCache()
	d.sys.FlushOSCache()
}
