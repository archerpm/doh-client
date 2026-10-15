package config

import (
	"net/netip"
	"sort"
	"strings"
)

// MaxListDomains bounds how many domains one published list may contribute.
const MaxListDomains = 20000

// ParseDomainList reads a published domain list. It accepts one domain per line
// and hosts-file lines ("1.2.3.4 example.com"); "#" starts a comment. Entries
// that are not valid domains, plain addresses and "localhost" are skipped. The
// result is normalized, de-duplicated and sorted.
func ParseDomainList(data []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if _, err := netip.ParseAddr(fields[0]); err == nil {
			fields = fields[1:] // hosts line: the names follow the address
		} else if len(fields) > 1 {
			continue // neither a hosts line nor a bare domain: not a list entry
		}
		for _, f := range fields {
			if _, err := netip.ParseAddr(f); err == nil || strings.EqualFold(f, "localhost") {
				continue
			}
			d, err := NormalizeDomain(f)
			if err != nil || seen[d] || !strings.Contains(d, ".") {
				continue
			}
			seen[d] = true
			out = append(out, d)
			if len(out) >= MaxListDomains {
				sort.Strings(out)
				return out
			}
		}
	}
	sort.Strings(out)
	return out
}

// Covers reports whether a rule for one of the given domains already matches host
// (a rule for "example.com" matches example.com and every subdomain).
func Covers(domains map[string]bool, host string) bool {
	for h := host; h != ""; {
		if domains[h] {
			return true
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	return false
}
