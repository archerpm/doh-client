package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/doh"
	"github.com/archerpm/doh-client/internal/resolver"
	"github.com/archerpm/doh-client/internal/sysdns"
	"github.com/miekg/dns"
)

func TestDetectTransitions(t *testing.T) {
	provider := func(down bool) healthSnapshot {
		return healthSnapshot{Providers: map[string]providerObservation{"comss": {Name: "Comss.one", Down: down}}}
	}
	dns := func(mismatch bool) healthSnapshot {
		return healthSnapshot{DNSKnown: true, DNSMismatch: mismatch}
	}
	type step struct {
		snapshot healthSnapshot
		kinds    []string
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{"debounce and no duplicates", []step{
			{provider(false), nil}, {provider(true), nil},
			{provider(true), []string{"provider_down"}}, {provider(true), nil},
			{provider(false), []string{"provider_up"}}, {provider(false), nil},
			{provider(true), nil}, {provider(true), []string{"provider_down"}},
		}},
		{"flapping resets debounce", []step{
			{provider(false), nil}, {provider(true), nil}, {provider(false), nil},
			{provider(true), nil}, {provider(false), nil},
			{provider(true), nil}, {provider(true), []string{"provider_down"}},
		}},
		{"first down observation stays silent", []step{
			{provider(true), nil}, {provider(true), nil}, {provider(true), nil},
			{provider(false), []string{"provider_up"}},
		}},
		{"DNS lost and restored", []step{
			{dns(false), nil}, {dns(true), []string{"dns_lost"}}, {dns(true), nil},
			{dns(false), []string{"dns_restored"}}, {dns(false), nil},
		}},
		{"first DNS mismatch is silent", []step{
			{dns(true), nil}, {dns(true), nil}, {dns(false), []string{"dns_restored"}},
		}},
		{"DNS baseline starts with first valid inspection", []step{
			{healthSnapshot{}, nil}, {dns(true), nil}, {dns(true), nil},
			{dns(false), []string{"dns_restored"}},
		}},
		{"disabled DNS does not report recovery", []step{
			{dns(false), nil}, {dns(true), []string{"dns_lost"}},
			{healthSnapshot{}, nil}, {dns(false), nil}, {dns(true), []string{"dns_lost"}},
		}},
		{"provider removal and readdition are silent", []step{
			{provider(false), nil}, {provider(true), nil}, {healthSnapshot{}, nil},
			{provider(true), nil}, {provider(true), nil}, {provider(false), []string{"provider_up"}},
		}},
		{"debounce is per provider", []step{
			{healthSnapshot{Providers: map[string]providerObservation{"a": {}, "b": {}}}, nil},
			{healthSnapshot{Providers: map[string]providerObservation{"a": {Down: true}, "b": {}}}, nil},
			{healthSnapshot{Providers: map[string]providerObservation{"a": {Down: true}, "b": {Down: true}}}, []string{"provider_down"}},
			{healthSnapshot{Providers: map[string]providerObservation{"a": {}, "b": {Down: true}}}, []string{"provider_up", "provider_down"}},
		}},
		{"all first observations silent", []step{
			{healthSnapshot{Providers: provider(true).Providers, DNSKnown: true, DNSMismatch: true}, nil},
		}},
	}
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var state transitionState
			for i, s := range tt.steps {
				next, events := detectTransitions(state, s.snapshot, at)
				// Replaying the same inputs must give the same result without mutation.
				replayed, again := detectTransitions(state, s.snapshot, at)
				if !reflect.DeepEqual(next, replayed) || !reflect.DeepEqual(events, again) {
					t.Fatalf("step %d: detector mutated its inputs", i)
				}
				var kinds []string
				for _, e := range events {
					kinds = append(kinds, e.Kind)
					if !e.Time.Equal(at) || e.Text == "" {
						t.Fatalf("step %d: invalid event: %+v", i, e)
					}
					if (e.Kind == "provider_down" || e.Kind == "provider_up") != (e.Provider != "") {
						t.Fatalf("step %d: invalid provider: %+v", i, e)
					}
					if e.Provider == "comss" && e.Kind == "provider_down" && e.Text != "Провайдер Comss.one недоступен" {
						t.Fatalf("unexpected text: %q", e.Text)
					}
				}
				if !reflect.DeepEqual(kinds, s.kinds) {
					t.Fatalf("step %d: got %v, want %v", i, kinds, s.kinds)
				}
				state = next
			}
		})
	}
}

func TestDetectTransitionsCopiesSnapshot(t *testing.T) {
	current := healthSnapshot{Providers: map[string]providerObservation{"a": {Down: true}}}
	next, _ := detectTransitions(transitionState{}, current, time.Time{})
	current.Providers["a"] = providerObservation{}
	if !next.snapshot.Providers["a"].Down {
		t.Fatal("retained snapshot aliases caller's map")
	}
}

func TestEventBuffer(t *testing.T) {
	var b eventBuffer
	if got := b.Snapshot(); got != nil {
		t.Fatalf("empty snapshot = %v", got)
	}
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 75; i++ {
		b.Add(Event{Time: at.Add(time.Duration(i) * time.Second), Kind: "provider_down", Provider: "comss", Text: fmt.Sprint(i)})
		got := b.Snapshot()
		wantSize := i + 1
		if wantSize > 30 {
			wantSize = 30
		}
		if len(got) != wantSize {
			t.Fatalf("after %d additions: got %d events, want %d", i+1, len(got), wantSize)
		}
		for j, e := range got {
			want := i + 1 - wantSize + j
			if e.Text != fmt.Sprint(want) || !e.Time.Equal(at.Add(time.Duration(want)*time.Second)) {
				t.Fatalf("event %d after %d additions: %+v", j, i+1, e)
			}
		}
	}
	got := b.Snapshot()
	got[0].Text = "changed by consumer"
	if b.Snapshot()[0].Text != "45" {
		t.Fatal("snapshot aliases ring storage")
	}
}

func TestEventBufferConcurrent(t *testing.T) {
	var b eventBuffer
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				b.Add(Event{Kind: "provider_up", Provider: "comss", Text: "available"})
				for _, e := range b.Snapshot() {
					if e.Kind != "provider_up" || e.Text != "available" {
						t.Errorf("torn event: %+v", e)
					}
				}
			}
		}()
	}
	wg.Wait()
	if len(b.Snapshot()) != 30 {
		t.Fatal("ring did not retain 30 events")
	}
}

func TestEventJSON(t *testing.T) {
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, provider := range []string{"", "comss"} {
		e := Event{Time: at, Kind: "dns_lost", Provider: provider, Text: "DNS системы больше не указывает на клиент"}
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		_, hasProvider := fields["provider"]
		if hasProvider != (provider != "") || fields["time"] != "2026-10-14T12:00:00Z" || fields["kind"] != e.Kind || fields["text"] != e.Text {
			t.Fatalf("unexpected Event JSON: %s", data)
		}
	}
	data, err := json.Marshal(Status{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["events"]; ok {
		t.Fatal("empty events must be omitted from status")
	}
}

func TestEnableFailureEventInStatus(t *testing.T) {
	busy, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	dir := t.TempDir()
	d := New(config.NewStore(filepath.Join(dir, "config.json")), filepath.Join(dir, "backup.json"))
	cfg := config.Default()
	cfg.Listen = []string{busy.LocalAddr().String()}
	d.setCfg(cfg)
	if err := d.SetEnabled(true); err == nil {
		t.Fatal("expected bind failure")
	}
	st := d.Status()
	if st.Active || len(st.Events) != 1 || st.Events[0].Kind != "enable_failed" || st.Events[0].Time.IsZero() {
		t.Fatalf("failed enable status: %+v", st)
	}
	st.Events[0].Text = "modified"
	if d.Status().Events[0].Text == "modified" {
		t.Fatal("status events alias the buffer")
	}
}

type eventProbe struct{ fail *bool }

func (p eventProbe) Exchange(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
	if *p.fail {
		return nil, errors.New("unavailable")
	}
	m := new(dns.Msg)
	m.SetReply(q)
	return m, nil
}

func TestObserveHealthEvents(t *testing.T) {
	dir := t.TempDir()
	d := New(config.NewStore(filepath.Join(dir, "config.json")), filepath.Join(dir, "backup.json"))
	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{ID: "a", Name: "Test provider", URL: "https://a.test/dns-query", Enabled: true},
		{ID: "disabled", URL: "https://disabled.test/dns-query", Enabled: false},
	}
	cfg.ActiveProvider = "a"
	cfg.DefaultChain = []string{"a"}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	d.setCfg(cfg)
	d.ctx = context.Background()
	fail := false
	d.router.SetExchangerFactory(func(config.Provider, *doh.Bootstrap, time.Duration) (resolver.Exchanger, error) {
		return eventProbe{fail: &fail}, nil
	})
	if err := d.router.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	d.opMu.Lock()
	defer d.opMu.Unlock()
	d.observeHealthEventsLocked(false, config.ModeProxy, dnsProbe{}) // unchecked health is not a baseline
	d.router.CheckHealth(d.ctx, "example.com")
	d.observeHealthEventsLocked(false, config.ModeProxy, dnsProbe{})
	if len(d.events.Snapshot()) != 0 {
		t.Fatal("first checked observation must be silent")
	}
	fail = true
	for i := 0; i < 3; i++ {
		d.router.CheckHealth(d.ctx, "example.com")
	}
	d.observeHealthEventsLocked(false, config.ModeProxy, dnsProbe{})
	d.observeHealthEventsLocked(true, config.ModeHosts, dnsProbe{}) // hosts mode must not inspect OS DNS
	fail = false
	d.router.CheckHealth(d.ctx, "example.com")
	d.observeHealthEventsLocked(false, config.ModeProxy, dnsProbe{})
	d.observeHealthEventsLocked(false, config.ModeProxy, dnsProbe{})
	var kinds []string
	for _, e := range d.events.Snapshot() {
		kinds = append(kinds, e.Kind)
	}
	want := []string{"provider_down", "provider_up"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("maintenance events = %v, want %v", kinds, want)
	}
	if !d.lastDNSCheck.IsZero() || d.healthEvents.snapshot.DNSKnown {
		t.Fatal("OS DNS was observed outside active proxy mode")
	}
}

func TestObserveDNSProbes(t *testing.T) {
	tests := []struct {
		name     string
		probes   []dnsProbe
		known    []bool
		mismatch []bool
		kinds    []string
	}{
		{
			name:     "inconclusive probes preserve a mismatch",
			probes:   []dnsProbe{{done: true, mismatch: true}, {done: true}, {}, {mismatch: true, complete: true}, {done: true, complete: true}, {done: true, complete: true}},
			known:    []bool{true, true, true, true, true, true},
			mismatch: []bool{true, true, true, true, false, false},
			kinds:    []string{"dns_restored"},
		},
		{
			name:     "first conclusive probe is a silent baseline",
			probes:   []dnsProbe{{done: true}, {}, {done: true, complete: true}, {done: true, mismatch: true}, {done: true, mismatch: true}},
			known:    []bool{false, false, true, true, true},
			mismatch: []bool{false, false, false, true, true},
			kinds:    []string{"dns_lost"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := New(nil, filepath.Join(t.TempDir(), "backup.json"))
			d.setCfg(&config.Config{})
			d.opMu.Lock()
			defer d.opMu.Unlock()
			for i, probe := range tt.probes {
				d.observeHealthEventsLocked(true, config.ModeProxy, probe)
				snapshot := d.healthEvents.snapshot
				if snapshot.DNSKnown != tt.known[i] || snapshot.DNSMismatch != tt.mismatch[i] {
					t.Fatalf("probe %d: DNS known=%v mismatch=%v", i, snapshot.DNSKnown, snapshot.DNSMismatch)
				}
			}
			var kinds []string
			for _, e := range d.events.Snapshot() {
				kinds = append(kinds, e.Kind)
			}
			if !reflect.DeepEqual(kinds, tt.kinds) {
				t.Fatalf("events = %v, want %v", kinds, tt.kinds)
			}
		})
	}
	for _, mode := range []config.Mode{config.ModeProxy, config.ModeHosts} {
		d := New(nil, filepath.Join(t.TempDir(), "backup.json"))
		d.setCfg(&config.Config{})
		d.opMu.Lock()
		d.observeHealthEventsLocked(mode == config.ModeHosts, mode, dnsProbe{done: true, mismatch: true})
		known := d.healthEvents.snapshot.DNSKnown
		d.opMu.Unlock()
		if known || len(d.events.Snapshot()) != 0 {
			t.Fatalf("inactive proxy or hosts mode consumed a DNS probe: %s", mode)
		}
	}
}

// Bind only a local listener; tests never apply or restore system DNS settings.
func newDNSMaintenanceDaemon(t *testing.T, addr string) *Daemon {
	t.Helper()
	dir := t.TempDir()
	d := New(config.NewStore(filepath.Join(dir, "config.json")), filepath.Join(dir, "backup.json"))
	d.setCfg(config.Default())
	d.ctx, d.cancel = context.WithCancel(context.Background())
	t.Cleanup(d.cancel)
	if err := d.srv.Start([]string{addr}); err != nil {
		t.Skipf("local DNS listener unavailable: %v", err)
	}
	t.Cleanup(d.srv.Stop)
	d.active, d.activeMode = true, config.ModeProxy
	return d
}

func TestMaintainDNSInspectOutsideLocks(t *testing.T) {
	d := newDNSMaintenanceDaemon(t, "127.0.0.1:53")
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d.inspectDNS = func(ctx context.Context, servers []string) sysdns.Inspection {
		if !d.opMu.TryLock() {
			t.Error("Inspect called under opMu")
		} else {
			d.opMu.Unlock()
		}
		if !d.mu.TryLock() {
			t.Error("Inspect called under mu")
		} else {
			d.mu.Unlock()
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
			t.Error("Inspect must have a five-second timeout")
		}
		if !reflect.DeepEqual(servers, []string{"127.0.0.1"}) {
			t.Errorf("Inspect servers = %v", servers)
		}
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return sysdns.Inspection{Mismatch: true}
	}
	t.Cleanup(func() {
		d.cancel()
		close(release)
		<-finished
	})
	go func() {
		defer close(finished)
		lastHosts := time.Now()
		d.maintainTick(&lastHosts)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Inspect did not start")
	}
	operations := make(chan error, 1)
	go func() {
		if err := d.UpdateConfig(d.Config()); err != nil {
			operations <- err
			return
		}
		operations <- d.SetEnabled(false)
	}()
	select {
	case err := <-operations:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("UpdateConfig/disable waited for Inspect")
	}
}

func TestMaintainDiscardsStaleDNSProbe(t *testing.T) {
	for _, change := range []string{"disabled", "hosts mode", "restarted proxy"} {
		t.Run(change, func(t *testing.T) {
			d := newDNSMaintenanceDaemon(t, "127.0.0.1:53")
			d.healthEvents.snapshot = healthSnapshot{DNSKnown: true}
			d.inspectDNS = func(context.Context, []string) sysdns.Inspection {
				d.opMu.Lock()
				defer d.opMu.Unlock()
				d.srv.Stop() // prevent any maintenance repair from touching OS DNS
				d.mu.Lock()
				switch change {
				case "disabled":
					d.active = false
				case "hosts mode":
					d.activeMode = config.ModeHosts
				case "restarted proxy":
					d.activeGeneration++
				}
				d.mu.Unlock()
				d.healthEvents.snapshot.DNSKnown = false
				return sysdns.Inspection{Mismatch: true, Complete: true}
			}
			lastHosts := time.Now()
			d.maintainTick(&lastHosts)
			if d.healthEvents.snapshot.DNSKnown || d.healthEvents.snapshot.DNSMismatch || !d.lastDNSCheck.IsZero() || len(d.events.Snapshot()) != 0 {
				t.Fatal("stale probe changed DNS observation or inspection schedule")
			}
		})
	}
}

func TestMaintainDNSProbeSchedule(t *testing.T) {
	for _, name := range []string{"inactive", "hosts", "non-DNS port", "recent check", "due", "inconclusive"} {
		t.Run(name, func(t *testing.T) {
			addr := "127.0.0.1:53"
			if name == "non-DNS port" {
				addr = "127.0.0.1:0"
			}
			d := newDNSMaintenanceDaemon(t, addr)
			wantCalls := 0
			switch name {
			case "inactive":
				d.active = false
			case "hosts":
				d.activeMode = config.ModeHosts
			case "recent check":
				d.lastDNSCheck = time.Now()
			case "due", "inconclusive":
				d.lastDNSCheck = time.Now().Add(-dnsInspectEvery)
				wantCalls = 1
			}
			previousCheck := d.lastDNSCheck
			calls := 0
			d.inspectDNS = func(context.Context, []string) sysdns.Inspection {
				calls++
				d.srv.Stop() // no OS DNS writes in this test
				return sysdns.Inspection{Complete: name != "inconclusive"}
			}
			// Cancellation skips repairs for cases that must not inspect.
			if wantCalls == 0 {
				d.cancel()
			}
			// Race a mu-protected schedule reader with the maintenance writer.
			stopReader, readerDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(readerDone)
				for {
					select {
					case <-stopReader:
						return
					default:
						d.mu.Lock()
						_ = d.lastDNSCheck.IsZero()
						d.mu.Unlock()
					}
				}
			}()
			lastHosts := time.Now()
			d.maintainTick(&lastHosts)
			close(stopReader)
			<-readerDone
			if calls != wantCalls {
				t.Fatalf("Inspect calls = %d, want %d", calls, wantCalls)
			}
			if wantCalls == 1 {
				if !d.lastDNSCheck.After(previousCheck) {
					t.Fatal("completed probe did not advance inspection schedule")
				}
				if d.healthEvents.snapshot.DNSKnown != (name == "due") || len(d.events.Snapshot()) != 0 {
					t.Fatal("first probe must be silent; inconclusive probe must remain unknown")
				}
			} else if !d.lastDNSCheck.Equal(previousCheck) {
				t.Fatal("skipped probe changed inspection schedule")
			}
		})
	}
}

func TestNoProviderUpWithoutSuccessfulHealth(t *testing.T) {
	h := resolver.NewHealthTable()
	var state transitionState
	at := time.Unix(1000, 0)
	observe := func() []Event {
		info := h.Snapshot()[0]
		snapshot := healthSnapshot{Providers: map[string]providerObservation{"a": {Down: info.Down}}}
		var events []Event
		state, events = detectTransitions(state, snapshot, at)
		return events
	}
	h.Report("a", true, time.Millisecond, nil)
	observe()
	for i := 0; i < 3; i++ {
		h.Report("a", false, 0, errors.New("offline"))
	}
	observe()
	observe() // provider debounce
	for i := 0; i < 5; i++ {
		at = at.Add(time.Minute)
		for _, event := range observe() {
			if event.Kind == "provider_up" {
				t.Fatalf("recovery without success: %+v", event)
			}
		}
	}
	h.Probe("a", true, time.Millisecond, nil)
	var kinds []string
	for _, event := range observe() {
		kinds = append(kinds, event.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{"provider_up"}) {
		t.Fatalf("successful recovery: %v", kinds)
	}
}
