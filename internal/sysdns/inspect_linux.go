//go:build linux

package sysdns

import (
	"context"
	"os"
	"strings"
)

func inspectPlatform(ctx context.Context, expected []string, emit func(DNSInterface) bool) error {
	if usesResolved() {
		// Global servers can bypass link DNS even when all links match.
		text, err := run("resolvectl", "dns")
		global := inspectGlobalDNS(text)
		if err != nil {
			return err
		}
		if len(global) > 0 && !emit(DNSInterface{Key: "global", Name: "systemd-resolved (Global)", Expected: expected, Actual: global}) {
			return ctx.Err()
		}
		for _, li := range links() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			actual, err := resolvectlList("dns", li.Name)
			it := DNSInterface{Key: li.Name, Name: li.Name, Expected: expected, Actual: actual}
			if err != nil {
				it.Error = "не удалось прочитать DNS: " + err.Error()
			}
			if err == nil && len(actual) == 0 {
				continue
			}
			if !emit(it) {
				return ctx.Err()
			}
		}
		return nil
	}
	b, err := os.ReadFile(resolvConf)
	if err != nil {
		return err
	}
	emit(DNSInterface{Key: resolvConf, Name: resolvConf, Expected: expected, Actual: inspectNameservers(string(b))})
	return ctx.Err()
}

func inspectNameservers(text string) []string {
	out := []string{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			out = append(out, f[1])
		}
	}
	return out
}

func inspectGlobalDNS(text string) []string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Global:") {
			return strings.Fields(strings.TrimPrefix(line, "Global:"))
		}
	}
	return []string{}
}
