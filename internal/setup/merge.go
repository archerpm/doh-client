// Package setup holds the pure parts of the one-click setup: merging preset rule
// groups into a config without touching what the user already configured.
package setup

import "github.com/archerpm/doh-client/internal/config"

// Group is a set of domains routed through one provider chain (a preset).
type Group struct {
	Domains   []string
	Providers []string
}

// MergeRules adds a rule for every group domain that has none yet and returns how
// many were added. Existing rules (enabled or not) are never changed: the user's
// choice wins. Rules never carry their own provider: one tunnel serves everything,
// so a group's Providers are only a recommendation and are ignored here.
func MergeRules(c *config.Config, groups []Group) int {
	have := map[string]bool{}
	for _, r := range c.Rules {
		have[r.Domain] = true
	}
	added := 0
	for _, g := range groups {
		for _, d := range g.Domains {
			dom, err := config.NormalizeDomain(d)
			if err != nil || have[dom] {
				continue
			}
			have[dom] = true
			c.Rules = append(c.Rules, config.Rule{Domain: dom, Providers: []string{}, Enabled: true})
			added++
		}
	}
	return added
}
