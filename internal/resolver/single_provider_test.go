package resolver

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/doh"
	"github.com/miekg/dns"
)

func TestApplyCannotSplitSnapshotOrCachePublication(t *testing.T) {
	for _, point := range []string{"between state and generation", "between generation check and Put"} {
		t.Run(point, func(t *testing.T) {
			a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "192.0.2.1", 60), nil }}
			b := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "192.0.2.2", 60), nil }}
			r := setup(t, map[string]*fake{"a": a, "b": b}, nil)
			entered, release := make(chan struct{}), make(chan struct{})
			var once atomic.Bool
			hook := func() {
				if once.Swap(true) {
					return
				}
				if r.mu.TryLock() {
					r.mu.Unlock()
					t.Error("critical section is not protected by Apply/FlushCache mutex")
				}
				close(entered)
				<-release
			}
			if point == "between state and generation" {
				r.afterStateLoad = hook
			} else {
				r.beforeCachePut = hook
			}
			answered := make(chan struct{})
			go func() {
				defer close(answered)
				r.Resolve(context.Background(), query("switch.test", dns.TypeA), "test")
			}()
			<-entered
			cfg := r.Config().Clone()
			cfg.ActiveProvider = "b"
			started, applied := make(chan struct{}), make(chan error, 1)
			go func() { close(started); applied <- r.Apply(cfg) }()
			<-started
			select {
			case err := <-applied:
				close(release)
				<-answered
				t.Fatalf("Apply completed in the protected window: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			close(release)
			<-answered
			if err := <-applied; err != nil {
				t.Fatal(err)
			}
			// The racing query is retried on the new state, so the cache may hold the
			// new provider's answer, but never the old one (checked below via the answer).
			m := r.Resolve(context.Background(), query("switch.test", dns.TypeA), "test")
			if got := AddrsOf(m); len(got) != 1 || got[0].String() != "192.0.2.2" || b.calls.Load() != 1 {
				t.Fatalf("new provider contaminated by old cache: %v, calls=%d", got, b.calls.Load())
			}
		})
	}
}

func TestResolveProviderRejectsInactiveAndUsesInstalledDefinition(t *testing.T) {
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "192.0.2.1", 60), nil }}
	b := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "192.0.2.2", 60), nil }}
	r := setup(t, map[string]*fake{"a": a, "b": b}, nil)
	for _, id := range []string{"b", "cloudflare", "google", "unknown"} {
		if _, _, err := r.ResolveProvider(context.Background(), config.Provider{ID: id, URL: "https://other.test"}, "example.test", dns.TypeA); err == nil {
			t.Fatalf("accepted inactive %s", id)
		}
	}
	if a.calls.Load() != 0 || b.calls.Load() != 0 {
		t.Fatal("inactive diagnostics sent DNS")
	}
	m, _, err := r.ResolveProvider(context.Background(), config.Provider{ID: "a", URL: "https://other.test"}, "example.test", dns.TypeA)
	if err != nil || len(m.Answer) != 1 || a.calls.Load() != 1 {
		t.Fatalf("installed provider: %v %v", m, err)
	}
}

func TestRealTrafficHealthRcodes(t *testing.T) {
	var rcode atomic.Int32
	rcode.Store(dns.RcodeServerFailure)
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return reply(q, int(rcode.Load())), nil }}
	r := setup(t, map[string]*fake{"a": a}, nil)
	for i := 0; i < downAfterFails; i++ {
		r.Resolve(context.Background(), query("health.test", dns.TypeA), "test")
	}
	h := r.Health()[0]
	if h.OK || !h.Down || h.Successes != 0 || h.Errors != downAfterFails || h.LastError == "" {
		t.Fatalf("SERVFAIL health: %+v", h)
	}
	for _, code := range []int{dns.RcodeNameError, dns.RcodeSuccess} {
		rcode.Store(int32(code))
		r.Resolve(context.Background(), query("health.test", dns.TypeA), "test")
		h = r.Health()[0]
		if !h.OK || h.Down || h.Fails != 0 {
			t.Fatalf("rcode %d health: %+v", code, h)
		}
	}
}

func TestHealthStaysDownUntilSuccess(t *testing.T) {
	for _, probe := range []bool{false, true} {
		h := NewHealthTable()
		now := time.Unix(1000, 0)
		h.now = func() time.Time { return now }
		for i := 0; i < downAfterFails; i++ {
			h.Report("a", false, 0, errors.New("offline"))
		}
		now = now.Add(time.Hour)
		if got := h.Snapshot()[0]; !got.Down || got.OK {
			t.Fatalf("timer reported recovery: %+v", got)
		}
		if probe {
			h.Probe("a", true, time.Millisecond, nil)
		} else {
			h.Report("a", true, time.Millisecond, nil)
		}
		if got := h.Snapshot()[0]; got.Down || !got.OK || got.Fails != 0 {
			t.Fatalf("success did not recover: %+v", got)
		}
	}
}

func TestApplyNormalizesWithoutMutatingCaller(t *testing.T) {
	r := New()
	var built []string
	r.SetExchangerFactory(func(p config.Provider, _ *doh.Bootstrap, _ time.Duration) (Exchanger, error) {
		built = append(built, p.ID)
		return &fake{}, nil
	})
	cfg := config.Default()
	if err := r.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	if len(built) != 1 || built[0] != "comss" || len(r.Config().DefaultChain) != 1 || cfg.ActiveProvider != "" {
		t.Fatalf("built=%v config=%+v", built, r.Config())
	}
}

func TestApplyWaitsForOldTunnelExchanges(t *testing.T) {
	for _, operation := range []string{"traffic", "diagnostics", "health"} {
		t.Run(operation, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var oldRunning atomic.Bool
			a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) {
				oldRunning.Store(true)
				defer oldRunning.Store(false)
				close(entered)
				<-release
				return aResp(q, "192.0.2.1", 60), nil
			}}
			b := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) {
				if oldRunning.Load() {
					t.Error("two tunnels exchanging simultaneously")
				}
				return aResp(q, "192.0.2.2", 60), nil
			}}
			r := setup(t, map[string]*fake{"a": a, "b": b}, nil)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				switch operation {
				case "traffic":
					r.Resolve(context.Background(), query("example.test", dns.TypeA), "test")
				case "diagnostics":
					_, _, _ = r.ResolveProvider(context.Background(), config.Provider{ID: "a"}, "example.test", dns.TypeA)
				case "health":
					r.CheckHealth(context.Background(), "example.test")
				}
			}()
			<-entered
			cfg := r.Config().Clone()
			cfg.ActiveProvider = "b"
			applied := make(chan error, 1)
			go func() { applied <- r.Apply(cfg) }()
			select {
			case err := <-applied:
				close(release)
				<-finished
				t.Fatalf("Apply passed an old exchange: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			close(release)
			<-finished
			if err := <-applied; err != nil {
				t.Fatal(err)
			}
			r.Resolve(context.Background(), query("example.test", dns.TypeA), "test")
			for _, h := range r.Health() {
				if h.ID != "b" {
					t.Fatalf("old health returned after Apply: %+v", h)
				}
			}
		})
	}
}
