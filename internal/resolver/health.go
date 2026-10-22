package resolver

import (
	"sort"
	"sync"
	"time"

	"github.com/archerpm/doh-client/internal/doh"
)

const downAfterFails = 3

// HealthInfo is a provider's health snapshot.
type HealthInfo struct {
	ID        string    `json:"id"`
	OK        bool      `json:"ok"`
	Down      bool      `json:"down"`
	LatencyMS float64   `json:"latency_ms"` // EWMA
	Fails     int       `json:"consecutive_fails"`
	Successes uint64    `json:"successes"`
	Errors    uint64    `json:"errors"`
	LastError string    `json:"last_error,omitempty"`
	LastCheck time.Time `json:"last_check"`
	Checked   bool      `json:"checked"`

	// Transport counts connections, resumed TLS sessions, retries and failed queries.
	Transport doh.Stats `json:"transport"`
}

type health struct {
	HealthInfo
}

// HealthTable tracks upstream health from real traffic and probes.
type HealthTable struct {
	mu  sync.Mutex
	m   map[string]*health
	now func() time.Time
}

func NewHealthTable() *HealthTable { return &HealthTable{m: map[string]*health{}, now: time.Now} }

func (h *HealthTable) get(id string) *health {
	e, ok := h.m[id]
	if !ok {
		e = &health{HealthInfo: HealthInfo{ID: id}}
		h.m[id] = e
	}
	return e
}

// Report records the outcome of a real query.
func (h *HealthTable) Report(id string, ok bool, lat time.Duration, err error) {
	h.record(id, ok, lat, err)
}

// Probe records the outcome of a health probe.
func (h *HealthTable) Probe(id string, ok bool, lat time.Duration, err error) {
	h.record(id, ok, lat, err)
}

func (h *HealthTable) record(id string, ok bool, lat time.Duration, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.get(id)
	now := h.now()
	e.LastCheck = now
	e.Checked = true
	if ok {
		ms := float64(lat) / float64(time.Millisecond)
		if e.Successes == 0 {
			e.LatencyMS = ms
		} else {
			e.LatencyMS = 0.7*e.LatencyMS + 0.3*ms
		}
		e.Successes++
		e.Fails = 0
		e.OK = true
		e.LastError = ""
		return
	}
	e.Errors++
	e.Fails++
	e.OK = false
	if err != nil {
		e.LastError = err.Error()
	}
}

func (h *HealthTable) isDown(e *health) bool {
	return e.Fails >= downAfterFails
}

// Order returns chain with currently-down providers moved to the end (stable).
func (h *HealthTable) Order(chain []string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	up := make([]string, 0, len(chain))
	var down []string
	for _, id := range chain {
		if e, ok := h.m[id]; ok && h.isDown(e) {
			down = append(down, id)
		} else {
			up = append(up, id)
		}
	}
	return append(up, down...)
}

// Retain drops entries for providers no longer configured.
func (h *HealthTable) Retain(ids []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	keep := map[string]bool{}
	for _, id := range ids {
		keep[id] = true
	}
	for id := range h.m {
		if !keep[id] {
			delete(h.m, id)
		}
	}
}

func (h *HealthTable) Snapshot() []HealthInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]HealthInfo, 0, len(h.m))
	for _, e := range h.m {
		i := e.HealthInfo
		i.Down = h.isDown(e)
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}
