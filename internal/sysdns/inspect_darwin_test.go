//go:build darwin

package sysdns

import "testing"

func TestInspectMacResolvers(t *testing.T) {
	for _, tc := range []struct {
		input string
		count int
	}{
		{"", 0}, {"resolver #1\n nameserver[0] : 192.0.2.1\n", 1},
		{"DNS configuration\nresolver #1\n nameserver[0] : 127.0.0.1\n nameserver[1] : ::1\n if_index : 6 (en0)\nresolver #2\n flags : Request A records\nDNS configuration (for scoped queries)\nresolver #1\n nameserver[0] : 192.0.2.1\n", 2},
	} {
		got := inspectMacResolvers(tc.input)
		if len(got) != tc.count {
			t.Fatalf("%+v", got)
		}
		if len(got) > 1 && (got[0].Key == got[1].Key || got[0].Name != "6 (en0)") {
			t.Fatalf("%+v", got)
		}
	}
}
