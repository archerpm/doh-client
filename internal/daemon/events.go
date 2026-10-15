package daemon

import (
	"sort"
	"sync"
	"time"

	"github.com/archerpm/doh-client/internal/config"
)

// Event describes a health transition for status consumers.
type Event struct {
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"` // provider_down | provider_up | dns_lost | dns_restored | enable_failed
	Provider string    `json:"provider,omitempty"`
	Text     string    `json:"text"`
}

const eventCapacity = 30

// eventBuffer is a zero-value-ready ring, ordered oldest to newest on Snapshot.
type eventBuffer struct {
	mu    sync.RWMutex
	items [eventCapacity]Event
	next  int
	size  int
}

func (b *eventBuffer) Add(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items[b.next] = e
	b.next = (b.next + 1) % eventCapacity
	if b.size < eventCapacity {
		b.size++
	}
}

func (b *eventBuffer) Snapshot() []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.size == 0 {
		return nil
	}
	out := make([]Event, b.size)
	start := (b.next - b.size + eventCapacity) % eventCapacity
	for i := range out {
		out[i] = b.items[(start+i)%eventCapacity]
	}
	return out
}

type providerObservation struct {
	Name string
	Down bool
}

type healthSnapshot struct {
	Providers   map[string]providerObservation
	DNSKnown    bool
	DNSMismatch bool
}

// transitionState remembers both the raw observation and the debounced state.
// Down at the first observation is a silent baseline; recovery is still useful.
type transitionState struct {
	snapshot     healthSnapshot
	providerDown map[string]bool
}

// resetProvider starts a new baseline for a different active provider while
// preserving observations of the system DNS. Requires the daemon's opMu.
func (s *transitionState) resetProvider() {
	s.snapshot.Providers = nil
	s.providerDown = nil
}

// detectTransitions is pure: it neither does I/O nor mutates its inputs. Callers
// supply the observation time and retain the returned state for the next check.
func detectTransitions(previous transitionState, current healthSnapshot, at time.Time) (transitionState, []Event) {
	next := transitionState{snapshot: current, providerDown: make(map[string]bool, len(current.Providers))}
	next.snapshot.Providers = make(map[string]providerObservation, len(current.Providers))
	var events []Event
	add := func(kind, provider, text string) {
		events = append(events, Event{Time: at, Kind: kind, Provider: provider, Text: text})
	}
	ids := make([]string, 0, len(current.Providers))
	for id := range current.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := current.Providers[id]
		next.snapshot.Providers[id] = p
		old, known := previous.snapshot.Providers[id]
		down := previous.providerDown[id]
		name := p.Name
		if name == "" {
			name = id
		}
		switch {
		case !known:
			down = p.Down // first observation (including newly added providers) is silent
		case p.Down && old.Down && !down:
			down = true
			add("provider_down", id, "Провайдер "+name+" недоступен")
		case !p.Down && down:
			down = false
			add("provider_up", id, "Провайдер "+name+" снова доступен")
		}
		next.providerDown[id] = down
	}
	if current.DNSKnown && previous.snapshot.DNSKnown && current.DNSMismatch != previous.snapshot.DNSMismatch {
		if current.DNSMismatch {
			add("dns_lost", "", "DNS системы больше не указывает на клиент")
		} else {
			add("dns_restored", "", "DNS системы снова указывает на клиент")
		}
	}
	return next, events
}

const dnsInspectEvery = 30 * time.Second

// dnsProbe carries an inspection performed outside opMu.
type dnsProbe struct {
	done, mismatch, complete bool
	at                       time.Time
}

// observeHealthEventsLocked requires opMu and runs before maintenance repairs DNS.
// Inconclusive DNS inspections preserve the last observation, avoiding false recovery.
func (d *Daemon) observeHealthEventsLocked(active bool, mode config.Mode, probe dnsProbe) {
	cfg := d.config()
	observed := make(map[string]providerObservation)
	names := make(map[string]string)
	for _, p := range cfg.Providers {
		if p.Enabled {
			names[p.ID] = p.Name
		}
	}
	for _, h := range d.router.Health() {
		if name, enabled := names[h.ID]; enabled && h.Checked {
			observed[h.ID] = providerObservation{Name: name, Down: h.Down}
		}
	}
	current := healthSnapshot{Providers: observed}
	if active && mode == config.ModeProxy {
		current.DNSKnown = d.healthEvents.snapshot.DNSKnown
		current.DNSMismatch = d.healthEvents.snapshot.DNSMismatch
		if probe.done && (probe.mismatch || probe.complete) {
			current.DNSKnown, current.DNSMismatch = true, probe.mismatch
		}
	}
	var events []Event
	d.healthEvents, events = detectTransitions(d.healthEvents, current, time.Now())
	for _, e := range events {
		d.events.Add(e)
	}
}

func (d *Daemon) recordEnableFailure() {
	d.events.Add(Event{Time: time.Now(), Kind: "enable_failed", Text: "Не удалось включить обход"})
}
