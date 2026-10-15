package sysdns

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
)

// DNSInterface is a read-only observation of an adapter, link or resolver file.
type DNSInterface struct {
	Key      string   `json:"key"`
	Name     string   `json:"name"`
	Expected []string `json:"expected"`
	Actual   []string `json:"actual"`
	Mismatch bool     `json:"mismatch"`
	Error    string   `json:"error,omitempty"`
}
type Inspection struct {
	Expected   []string       `json:"expected"`
	Actual     []string       `json:"actual"`
	Interfaces []DNSInterface `json:"interfaces"`
	Mismatch   bool           `json:"mismatch"`
	Complete   bool           `json:"complete"`
	Error      string         `json:"error,omitempty"`
}

// Inspect compares live DNS settings with the expected client servers.
// Empty expected means observe only (hosts mode or an inactive client).
// Existing platform readers have their own command timeout; cancellation stops
// collecting immediately, and any in-flight reader finishes without writes.
func Inspect(ctx context.Context, expected []string) Inspection {
	return inspectWith(ctx, expected, inspectPlatform)
}

type inspectReader func(context.Context, []string, func(DNSInterface) bool) error

func inspectWith(ctx context.Context, expected []string, read inspectReader) Inspection {
	out := Inspection{Expected: append([]string{}, expected...), Actual: []string{}, Interfaces: []DNSInterface{}}
	if ctx.Err() != nil {
		out.Error = "проверка DNS прервана: " + ctx.Err().Error()
		return out
	}
	events := make(chan DNSInterface)
	done := make(chan error, 1)
	go func() {
		done <- read(ctx, append([]string{}, expected...), func(it DNSInterface) bool {
			select {
			case events <- it:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	finish := func() Inspection {
		sort.Slice(out.Interfaces, func(i, j int) bool { return out.Interfaces[i].Key < out.Interfaces[j].Key })
		sort.Strings(out.Actual)
		return out
	}
	for {
		select {
		case it := <-events:
			it.Expected = append([]string{}, it.Expected...)
			it.Actual = append([]string{}, it.Actual...)
			it.Mismatch = len(it.Expected) > 0 && it.Error == "" && !sameDNS(it.Expected, it.Actual)
			out.Interfaces = append(out.Interfaces, it)
			out.Mismatch = out.Mismatch || it.Mismatch
			for _, s := range it.Actual {
				found := false
				for _, a := range out.Actual {
					if a == s {
						found = true
						break
					}
				}
				if !found {
					out.Actual = append(out.Actual, s)
				}
			}
		case err := <-done:
			out.Complete = err == nil
			if err != nil {
				out.Error = fmt.Sprintf("не удалось прочитать системный DNS: %v", err)
			}
			if len(out.Interfaces) == 0 && out.Error == "" {
				out.Complete = false
				out.Error = "нет данных о системном DNS"
			}
			for _, it := range out.Interfaces {
				if it.Error != "" {
					out.Complete = false
				}
			}
			return finish()
		case <-ctx.Done():
			out.Error = "проверка DNS прервана: " + ctx.Err().Error()
			return finish()
		}
	}
}

func sameDNS(a, b []string) bool {
	normalize := func(values []string) []string {
		seen := map[string]bool{}
		out := []string{}
		for _, s := range values {
			if ip, err := netip.ParseAddr(s); err == nil {
				s = ip.Unmap().String()
			}
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		return out
	}
	return equalSet(normalize(a), normalize(b))
}
