package probe

import "testing"

func TestVerdicts(t *testing.T) {
	answer := Answer{Provider: "p", Type: "A", Rcode: "NOERROR", Addrs: []string{"192.0.2.1"}, Status: "ok"}
	for _, tc := range []struct {
		name, want string
		e          Evidence
	}{
		{"mismatch precedes transport", "os_dns_mismatch", Evidence{Matched: true, OSMismatch: true, Answers: []Answer{answer}, Connections: []Connection{{TLS: "ok"}}}},
		{"no rule", "no_rule", Evidence{}},
		{"no rule but reachable", "transport_ok", Evidence{Answers: []Answer{answer}, Connections: []Connection{{TCP: "ok", TLS: "ok"}}}},
		{"no rule and unreachable", "no_rule", Evidence{Answers: []Answer{answer}, Connections: []Connection{{TCP: "failed", TLS: "skipped"}}}},
		{"blocked", "provider_blocked", Evidence{Matched: true, Answers: []Answer{{Status: "ok", Rcode: "NOERROR", Blocked: true, Addrs: []string{"0.0.0.0"}}}}},
		{"unreachable", "provider_unreachable", Evidence{Matched: true, Answers: []Answer{{Status: "failed"}}}},
		{"servfail", "provider_unreachable", Evidence{Matched: true, Answers: []Answer{{Status: "ok", Rcode: "SERVFAIL"}}}},
		{"empty fallback after failure", "no_answer", Evidence{Matched: true, Answers: []Answer{{Type: "A", Status: "failed"}, {Type: "A", Status: "ok", Rcode: "NOERROR"}}}},
		{"empty fallback after sinkhole", "no_answer", Evidence{Matched: true, Answers: []Answer{{Type: "A", Status: "ok", Rcode: "NOERROR", Blocked: true}, {Type: "A", Status: "ok", Rcode: "NOERROR"}}}},
		{"empty AAAA does not erase A sinkhole", "provider_blocked", Evidence{Matched: true, Answers: []Answer{{Type: "A", Status: "ok", Rcode: "NOERROR", Blocked: true}, {Type: "AAAA", Status: "ok", Rcode: "NOERROR"}}}},
		{"empty", "no_answer", Evidence{Matched: true, Answers: []Answer{{Status: "ok", Rcode: "NOERROR"}}}},
		{"nxdomain", "no_answer", Evidence{Matched: true, Answers: []Answer{{Status: "ok", Rcode: "NXDOMAIN"}}}},
		{"tls", "transport_ok", Evidence{Matched: true, Answers: []Answer{answer}, Connections: []Connection{{TCP: "ok", TLS: "ok"}}}},
		{"tls failure", "tls_failed", Evidence{Matched: true, Answers: []Answer{answer}, Connections: []Connection{{TCP: "ok", TLS: "failed"}}}},
		{"tcp failure", "tcp_failed", Evidence{Matched: true, Answers: []Answer{answer}, Connections: []Connection{{TCP: "failed", TLS: "skipped"}}}},
		{"pending", "inconclusive", Evidence{Matched: true, Answers: []Answer{{Status: "pending"}}}},
		{"no probes", "inconclusive", Evidence{Matched: true, Answers: []Answer{answer}}},
		{"pending address", "inconclusive", Evidence{Matched: true, Answers: []Answer{answer}, Connections: []Connection{{TCP: "pending"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.e)
			if got.Code != tc.want || got.Stage == "" || got.Action.Code == "" || got.Action.Text == "" {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestRouted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []Answer
		rule    bool
		want    int
	}{
		{"pending first", []Answer{{Provider: "a", Type: "A", Status: "pending"}, {Provider: "b", Type: "A", Status: "ok", Rcode: "NOERROR"}}, true, 1},
		{"sinkhole fallback", []Answer{{Provider: "a", Type: "A", Status: "ok", Rcode: "NOERROR", Blocked: true}, {Provider: "b", Type: "A", Status: "ok", Rcode: "NOERROR"}}, true, 2},
		{"default sinkhole stops", []Answer{{Provider: "a", Type: "A", Status: "ok", Rcode: "NOERROR", Blocked: true}, {Provider: "b", Type: "A", Status: "ok", Rcode: "NOERROR"}}, false, 1},
		{"nxdomain stops", []Answer{{Provider: "a", Type: "A", Status: "ok", Rcode: "NXDOMAIN"}, {Provider: "b", Type: "A", Status: "ok", Rcode: "NOERROR"}}, true, 1},
		{"error falls through", []Answer{{Provider: "a", Type: "A", Status: "failed"}, {Provider: "b", Type: "A", Status: "ok", Rcode: "NOERROR"}}, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Routed([]string{"a", "b"}, tc.answers, "A", tc.rule); len(got) != tc.want {
				t.Fatalf("%+v", got)
			}
		})
	}
}
