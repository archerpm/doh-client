package resolver

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/miekg/dns"
)

// A tunnel switch landing between the snapshot and the exchange must not leak a
// SERVFAIL to the client: the query is retried once on the new state.
func TestConfigSwitchRacingQueryIsRetried(t *testing.T) {
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "1.2.3.4", 300), nil }}
	r := setup(t, map[string]*fake{"a": a}, nil)
	var loads atomic.Int32
	r.afterStateLoad = func() {
		if loads.Add(1) == 1 { // first snapshot only: swap the state under the query
			old := r.st.Load()
			cp := *old
			r.st.Store(&cp)
		}
	}
	m := r.Resolve(context.Background(), query("race.test", dns.TypeA), "127.0.0.1")
	if m.Rcode != dns.RcodeSuccess || len(m.Answer) != 1 {
		t.Fatalf("racing switch leaked an error to the client: %v", m)
	}
	if loads.Load() != 2 {
		t.Fatalf("expected exactly one retry, snapshots=%d", loads.Load())
	}
}
