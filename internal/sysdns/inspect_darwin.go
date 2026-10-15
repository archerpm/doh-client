//go:build darwin

package sysdns

import (
	"context"
	"strconv"
	"strings"
)

func inspectPlatform(ctx context.Context, expected []string, emit func(DNSInterface) bool) error {
	svcs, err := services(false)
	if err != nil {
		return err
	}
	for _, svc := range svcs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		actual, err := getDNS(svc)
		it := DNSInterface{Key: svc, Name: svc, Expected: expected, Actual: actual}
		if err != nil {
			it.Error = "не удалось прочитать DNS: " + err.Error()
		}
		// Empty static DNS means DHCP. scutil below supplies live resolver servers.
		if len(actual) == 0 && err == nil {
			continue
		}
		if !emit(it) {
			return ctx.Err()
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	out, err := run("/usr/sbin/scutil", "--dns")
	if err != nil {
		return err
	}
	for _, it := range inspectMacResolvers(out) {
		it.Expected = expected
		if !emit(it) {
			return ctx.Err()
		}
	}
	return nil
}

// scutil includes DHCP, VPN and scoped resolvers which networksetup omits.
func inspectMacResolvers(text string) []DNSInterface {
	out := []DNSInterface{}
	current := DNSInterface{}
	flush := func() {
		if len(current.Actual) > 0 {
			out = append(out, current)
		}
	}
	section := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "DNS configuration") {
			section++
			continue
		}
		if strings.HasPrefix(line, "resolver #") {
			flush()
			current = DNSInterface{Key: strconv.Itoa(section) + "/" + line, Name: line, Actual: []string{}}
		} else if strings.HasPrefix(line, "nameserver[") {
			if _, ip, ok := strings.Cut(line, " : "); ok {
				current.Actual = append(current.Actual, strings.TrimSpace(ip))
			}
		} else if strings.HasPrefix(line, "if_index") {
			if _, name, ok := strings.Cut(line, " : "); ok {
				current.Name = strings.TrimSpace(name)
			}
		}
	}
	flush()
	return out
}
