package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/support"
)

// ErrServiceTestRunning: one service check at a time.
var ErrServiceTestRunning = errors.New("проверка сервисов уже идёт")

// ErrNotBypassProvider: only providers declared as bypass ones are checked for services.
var ErrNotBypassProvider = errors.New("сервисы проверяются только у обходных провайдеров")

// ServiceTestResult is what one provider was seen proxying in the last check.
type ServiceTestResult struct {
	Provider string   `json:"provider"`
	Name     string   `json:"name"`
	Services []string `json:"services"` // proxied and the proxy answers TLS from here
	// Unreachable are proxied in DNS, but the proxy address does not complete a TLS handshake from here.
	Unreachable []string  `json:"unreachable"`
	Hosts       int       `json:"hosts"`  // hosts asked
	Errors      int       `json:"errors"` // hosts the provider did not answer; many means the result is unreliable
	At          time.Time `json:"at"`
}

// ServiceTestState is the progress and results of the service check.
type ServiceTestState struct {
	Running  bool                `json:"running"`
	Provider string              `json:"provider,omitempty"` // empty: all bypass providers
	Done     int                 `json:"done"`
	Total    int                 `json:"total"`
	Error    string              `json:"error,omitempty"` // why the last run did not finish
	Results  []ServiceTestResult `json:"results"`
}

type serviceTest struct {
	mu     sync.Mutex
	state  ServiceTestState
	cancel context.CancelFunc
	path   string // where the latest results are kept between runs; empty: memory only
}

// serviceTestFile is the name of the saved results, next to the config.
const serviceTestFile = "service-check.json"

// load restores the results saved by the previous run of the service.
func (s *serviceTest) load() {
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("service check: %v", err)
		}
		return
	}
	var res []ServiceTestResult
	if err := json.Unmarshal(b, &res); err != nil {
		log.Printf("service check: %s: %v — ignored", s.path, err)
		return
	}
	s.mu.Lock()
	s.state.Results = res
	s.mu.Unlock()
}

// save overwrites the file with the current results; the caller holds s.mu.
func (s *serviceTest) save() {
	if s.path == "" {
		return
	}
	b, err := json.Marshal(s.state.Results)
	if err == nil {
		tmp := s.path + ".tmp"
		if err = os.WriteFile(tmp, b, 0o644); err == nil {
			err = os.Rename(tmp, s.path)
		}
	}
	if err != nil {
		log.Printf("service check: save: %v", err)
	}
}

// serviceReachTimeout bounds one TLS handshake with a provider's proxy address.
const serviceReachTimeout = 5 * time.Second

// runSupport is support.Run (replaceable in tests).
var runSupport = support.Run

func (s *serviceTest) snapshot() ServiceTestState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	st.Results = append([]ServiceTestResult{}, st.Results...)
	return st
}

// ServiceTest returns the progress and the latest results of the service check.
func (d *Daemon) ServiceTest() ServiceTestState { return d.svcTest.snapshot() }

// StartServiceTest asks the bypass providers (one, or all when providerID is
// empty) for the hosts of every known service and works out what each of them
// proxies. It runs in the background; poll ServiceTest for progress. Like the
// bypass test it refuses while the bypass is on.
func (d *Daemon) StartServiceTest(providerID string) error {
	d.mu.Lock()
	active := d.active
	d.mu.Unlock()
	if active {
		return ErrBypassActive
	}
	cfg := d.config().Clone()
	var provs []config.Provider
	for _, p := range cfg.Providers {
		if providerID == "" && p.Geo {
			provs = append(provs, p)
		}
		if providerID != "" && p.ID == providerID {
			if !p.Geo {
				return ErrNotBypassProvider
			}
			provs = append(provs, p)
		}
	}
	if len(provs) == 0 {
		return errors.New("нет провайдеров для проверки")
	}
	var extra []string
	for _, p := range config.Presets() {
		extra = append(extra, p.Domains...)
	}
	o := support.Options{Services: config.ServiceProbes(), Extra: extra, Providers: provs, Bootstrap: cfg.BootstrapDNS,
		Reach: func(ctx context.Context, provider, host string, ip netip.Addr) bool {
			c, cancel := context.WithTimeout(ctx, serviceReachTimeout)
			defer cancel()
			return probeReach(c, provider, host, ip).TLS == "ok"
		}}

	s := &d.svcTest
	s.mu.Lock()
	if s.state.Running {
		s.mu.Unlock()
		return ErrServiceTestRunning
	}
	parent := d.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.state.Running, s.state.Provider, s.state.Done, s.state.Total, s.state.Error = true, providerID, 0, 0, ""
	s.mu.Unlock()
	// The bypass may have been switched on after the check at the top: SetEnabled
	// cancels only a check that is already registered, so look again now.
	d.mu.Lock()
	nowActive := d.active
	d.mu.Unlock()
	if nowActive {
		cancel()
		s.mu.Lock()
		s.state.Running, s.cancel = false, nil
		s.mu.Unlock()
		return ErrBypassActive
	}

	o.Progress = func(done, total int) {
		s.mu.Lock()
		s.state.Done, s.state.Total = done, total
		s.mu.Unlock()
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer cancel()
		res, err := runSupport(ctx, o)
		now := time.Now()
		s.mu.Lock()
		defer s.mu.Unlock()
		s.state.Running = false
		if err != nil {
			s.state.Error = err.Error()
			return
		}
		s.state.Done = s.state.Total
		byID := map[string]config.Provider{}
		for _, p := range provs {
			byID[p.ID] = p
		}
		keep := s.state.Results[:0:0]
		for _, old := range s.state.Results { // results of providers not asked this time stay
			if _, again := byID[old.Provider]; !again {
				keep = append(keep, old)
			}
		}
		for _, r := range res {
			services, bad := []string{}, map[string]bool{}
			for _, n := range r.Unreachable {
				bad[n] = true
			}
			for _, n := range r.Unchecked { // TLS was not tried: neither "opens" nor "does not"
				bad[n] = true
			}
			for _, n := range r.Services {
				if !bad[n] {
					services = append(services, n)
				}
			}
			unreachable := append([]string{}, r.Unreachable...)
			keep = append(keep, ServiceTestResult{Provider: r.Provider, Name: byID[r.Provider].Name, Services: services, Unreachable: unreachable, Hosts: r.Hosts, Errors: r.Errors, At: now})
		}
		s.state.Results = keep
		s.save()
	}()
	return nil
}

// CancelServiceTest stops a running check.
func (d *Daemon) CancelServiceTest() {
	d.svcTest.mu.Lock()
	cancel := d.svcTest.cancel
	d.svcTest.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
