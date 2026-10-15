package daemon

import (
	"context"
	"errors"
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
	Provider string    `json:"provider"`
	Name     string    `json:"name"`
	Services []string  `json:"services"`
	Hosts    int       `json:"hosts"`  // hosts asked
	Errors   int       `json:"errors"` // hosts the provider did not answer; many means the result is unreliable
	At       time.Time `json:"at"`
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
}

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
	o := support.Options{Services: config.ServiceProbes(), Extra: extra, Providers: provs, Bootstrap: cfg.BootstrapDNS}

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
			services := r.Services
			if services == nil {
				services = []string{}
			}
			keep = append(keep, ServiceTestResult{Provider: r.Provider, Name: byID[r.Provider].Name, Services: services, Hosts: r.Hosts, Errors: r.Errors, At: now})
		}
		s.state.Results = keep
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
