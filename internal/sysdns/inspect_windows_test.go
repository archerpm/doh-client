//go:build windows

package sysdns

import "testing"

func TestInspectAdapter(t *testing.T) {
	for _, tc := range []struct {
		name             string
		actual, expected []string
		idx6             uint32
		match            bool
	}{
		{"ipv4 only", []string{"127.0.0.1"}, []string{"127.0.0.1"}, 1, true},
		{"ipv6 guard", []string{"127.0.0.1", "::1"}, []string{"127.0.0.1"}, 1, true},
		{"unexpected resolver", []string{"192.168.1.1"}, []string{"127.0.0.1"}, 1, false},
		{"dual listener", []string{"127.0.0.1", "::1"}, []string{"127.0.0.1", "::1"}, 1, true},
		{"no ipv6 on adapter", []string{"127.0.0.1"}, []string{"127.0.0.1", "::1"}, 0, true},
		{"ipv6 adapter still needs ::1", []string{"127.0.0.1"}, []string{"127.0.0.1", "::1"}, 1, false},
		{"no ipv6 but wrong resolver", []string{"8.8.8.8"}, []string{"127.0.0.1", "::1"}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := inspectAdapter(adapter{guid: "id", name: "link", current: tc.actual, idx6: tc.idx6}, tc.expected)
			if sameDNS(it.Expected, it.Actual) != tc.match {
				t.Fatalf("%+v", it)
			}
		})
	}
}
