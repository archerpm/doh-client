package resolver

import "strings"

// Explanation describes routing without issuing queries or changing health.
type Explanation struct {
	Matched         string   `json:"matched"`
	Configured      []string `json:"configured"`
	Enabled         []string `json:"enabled"`
	Effective       []string `json:"effective"`
	DefaultFallback bool     `json:"default_fallback"`
	BlockAAAA       bool     `json:"block_aaaa"`
	BlockHTTPS      bool     `json:"block_https"`
}

func (r *Router) Explain(name string) Explanation {
	e := Explanation{Configured: []string{}, Enabled: []string{}, Effective: []string{}}
	st := r.st.Load()
	if st == nil {
		return e
	}
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	matched, ruleChain, ok := st.matchRule(name)
	e.Matched = matched
	configured, chain := st.cfg.DefaultChain, st.chain
	if ok {
		e.BlockAAAA, e.BlockHTTPS = st.cfg.BlockAAAAForRule, st.cfg.BlockHTTPSRR
		// Apply uses the last enabled duplicate rule, so mirror that here.
		for _, rl := range st.cfg.Rules {
			if rl.Enabled && rl.Domain == matched && len(rl.Providers) > 0 {
				configured = rl.Providers
			} else if rl.Enabled && rl.Domain == matched {
				configured = st.cfg.DefaultChain
			}
		}
		if len(ruleChain) > 0 {
			chain = ruleChain
		} else {
			e.DefaultFallback = len(enabledOnly(configured, st.upstreams)) == 0
		}
	}
	e.Configured = append(e.Configured, configured...)
	e.Enabled = enabledOnly(configured, st.upstreams)
	e.Effective = r.health.Order(chain)
	return e
}
