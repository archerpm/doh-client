//go:build linux

package sysdns

import (
	"reflect"
	"testing"
)

func TestInspectNameservers(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  []string
	}{
		{"# nameserver ignored\nsearch test\n", []string{}},
		{"nameserver 127.0.0.1 # local\n nameserver ::1\n", []string{"127.0.0.1", "::1"}},
	} {
		if got := inspectNameservers(tc.input); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%v", got)
		}
	}
}

func TestInspectGlobalDNS(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  []string
	}{
		{"Global:\nLink 2 (eth0): 127.0.0.1\n", []string{}},
		{"Global: 1.1.1.1 2001:db8::1\nLink 2 (eth0): 127.0.0.1\n", []string{"1.1.1.1", "2001:db8::1"}},
	} {
		if got := inspectGlobalDNS(tc.input); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%v", got)
		}
	}
}
