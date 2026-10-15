package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/sysdns"
)

type fakeSystemDNS struct {
	applied  [][]string
	applyErr error
	restored int
	onApply  func()
}

func (f *fakeSystemDNS) Apply(servers []string) error {
	f.applied = append(f.applied, append([]string(nil), servers...))
	if f.onApply != nil {
		f.onApply()
	}
	return f.applyErr
}
func (f *fakeSystemDNS) Restore() error      { f.restored++; return nil }
func (*fakeSystemDNS) HasBackup() bool       { return false }
func (*fakeSystemDNS) Status() sysdns.Status { return sysdns.Status{} }
func (*fakeSystemDNS) FlushOSCache()         {}

func controlDaemon(t *testing.T) *Daemon {
	t.Helper()
	dir := t.TempDir()
	d := New(config.NewStore(filepath.Join(dir, "config.json")), filepath.Join(dir, "backup.json"))
	cfg := config.Default()
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := d.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := d.router.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	d.setCfg(cfg)
	d.ctx, d.cancel = context.WithCancel(context.Background())
	d.sys = &fakeSystemDNS{}
	t.Cleanup(d.cancel)
	t.Cleanup(d.srv.Stop)
	return d
}

func TestControlRejectsStoppedDaemon(t *testing.T) {
	for _, operation := range []string{"enable", "disable", "config", "reapply"} {
		t.Run(operation, func(t *testing.T) {
			d := controlDaemon(t)
			before, err := os.ReadFile(d.store.Path())
			if err != nil {
				t.Fatal(err)
			}
			cfg := d.Config()
			d.Stop()
			var opErr error
			switch operation {
			case "enable":
				opErr = d.SetEnabled(true)
			case "disable":
				opErr = d.SetEnabled(false)
			case "config":
				next := cfg.Clone()
				next.Rules = []config.Rule{{Domain: "new.test", Enabled: true}}
				opErr = d.UpdateConfig(next)
			case "reapply":
				opErr = d.ReapplyDNS()
			}
			if !errors.Is(opErr, context.Canceled) {
				t.Fatalf("operation after Stop: %v", opErr)
			}
			after, err := os.ReadFile(d.store.Path())
			if err != nil || string(before) != string(after) || !reflect.DeepEqual(cfg, d.Config()) || d.Status().Active {
				t.Fatalf("stopped operation changed state: %v", err)
			}
			if len(d.sys.(*fakeSystemDNS).applied) != 0 {
				t.Fatal("stopped operation applied DNS")
			}
		})
	}
}

func TestControlChecksCancellationAfterOpLock(t *testing.T) {
	for _, operation := range []string{"enable", "config", "reapply"} {
		t.Run(operation, func(t *testing.T) {
			d := controlDaemon(t)
			d.opMu.Lock()
			started, done := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				switch operation {
				case "enable":
					done <- d.SetEnabled(true)
				case "config":
					done <- d.UpdateConfig(d.Config())
				case "reapply":
					done <- d.ReapplyDNS()
				}
			}()
			<-started
			select {
			case err := <-done:
				d.opMu.Unlock()
				t.Fatalf("operation did not wait for opMu: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			d.cancel()
			d.opMu.Unlock()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("queued operation: %v", err)
			}
		})
	}
}

func TestReapplyDNS(t *testing.T) {
	for _, name := range []string{"inactive", "hosts", "non-DNS port", "success", "apply failure"} {
		t.Run(name, func(t *testing.T) {
			addr := "127.0.0.1:53"
			if name == "non-DNS port" || name == "inactive" || name == "hosts" {
				addr = "127.0.0.1:0"
			}
			d := newDNSMaintenanceDaemon(t, addr)
			fake := &fakeSystemDNS{}
			d.sys = fake
			cfg := d.Config()
			cfg.Enabled = true
			d.setCfg(cfg)
			if err := d.store.Save(cfg); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(d.store.Path())
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "inactive":
				d.active = false
			case "hosts":
				d.activeMode = config.ModeHosts
			case "apply failure":
				fake.applyErr = errors.New("apply failed")
			case "success":
				d.lastErr = "системный DNS: previous failure"
			}
			generation, active := d.activeGeneration, d.active
			addrs := d.srv.Addrs()
			err = d.ReapplyDNS()
			if name == "success" {
				if err != nil || d.Status().LastError != "" {
					t.Fatalf("success: %v %+v", err, d.Status())
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
			if name == "apply failure" && (!errors.Is(err, fake.applyErr) || !strings.Contains(d.Status().LastError, "apply failed")) {
				t.Fatalf("lost apply failure: %v", err)
			}
			if name == "success" || name == "apply failure" {
				if !reflect.DeepEqual(fake.applied, [][]string{{"127.0.0.1"}}) {
					t.Fatalf("applied %v", fake.applied)
				}
			} else if len(fake.applied) != 0 {
				t.Fatalf("invalid mode applied DNS: %v", fake.applied)
			}
			after, readErr := os.ReadFile(d.store.Path())
			if readErr != nil || string(before) != string(after) || d.active != active || d.activeGeneration != generation || !reflect.DeepEqual(addrs, d.srv.Addrs()) || fake.restored != 0 {
				t.Fatalf("reapply changed activation, listener or saved config: %v", readErr)
			}
		})
	}
}

func TestReapplyDNSSerializesDisable(t *testing.T) {
	d := newDNSMaintenanceDaemon(t, "127.0.0.1:53")
	entered, release := make(chan struct{}), make(chan struct{})
	fake := &fakeSystemDNS{onApply: func() { close(entered); <-release }}
	d.sys = fake
	reapplyDone := make(chan error, 1)
	go func() { reapplyDone <- d.ReapplyDNS() }()
	<-entered
	disableDone := make(chan error, 1)
	go func() { disableDone <- d.SetEnabled(false) }()
	select {
	case err := <-disableDone:
		close(release)
		<-reapplyDone
		t.Fatalf("disable ran during reapply: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-reapplyDone; err != nil {
		t.Fatal(err)
	}
	if err := <-disableDone; err != nil {
		t.Fatal(err)
	}
	if d.Status().Active || len(d.srv.Addrs()) != 0 {
		t.Fatal("disable did not finish")
	}
}

func TestUpdateConfigResetsProviderEventsOnlyOnCommit(t *testing.T) {
	for _, change := range []string{"switch", "same provider", "failed save"} {
		t.Run(change, func(t *testing.T) {
			d := controlDaemon(t)
			cfg := d.Config()
			original := transitionState{
				snapshot:     healthSnapshot{Providers: map[string]providerObservation{cfg.ActiveProvider: {Down: true}}, DNSKnown: true, DNSMismatch: true},
				providerDown: map[string]bool{cfg.ActiveProvider: true},
			}
			d.healthEvents = original
			next := cfg.Clone()
			if change != "same provider" {
				next.ActiveProvider = "malw-cf"
				if next.ActiveProvider == cfg.ActiveProvider {
					next.ActiveProvider = "comss"
				}
			}
			if change == "failed save" {
				if err := os.Remove(d.store.Path()); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(d.store.Path(), 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := d.UpdateConfig(next)
			if change == "failed save" {
				if err == nil {
					t.Fatal("expected failed commit")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if change != "switch" {
				if !reflect.DeepEqual(original, d.healthEvents) {
					t.Fatal("non-commit or same ID reset events")
				}
				return
			}
			if d.healthEvents.snapshot.Providers != nil || d.healthEvents.providerDown != nil || !d.healthEvents.snapshot.DNSKnown || !d.healthEvents.snapshot.DNSMismatch {
				t.Fatalf("provider reset damaged DNS or retained history: %+v", d.healthEvents)
			}
			// A -> B -> A between ticks must start a new provider baseline.
			if err := d.UpdateConfig(cfg); err != nil {
				t.Fatal(err)
			}
			_, events := detectTransitions(d.healthEvents, healthSnapshot{Providers: map[string]providerObservation{cfg.ActiveProvider: {}}, DNSKnown: true}, time.Now())
			if len(events) != 1 || events[0].Kind != "dns_restored" {
				t.Fatalf("stale provider recovery or lost DNS history: %+v", events)
			}
		})
	}
}
