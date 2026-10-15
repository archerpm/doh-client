package config

import (
	"regexp"
	"testing"
)

func TestTestedSupportMatchesCatalogue(t *testing.T) {
	known := map[string]bool{}
	for _, s := range ServiceProbes() {
		known[s.Name] = true
	}
	builtin := map[string]bool{}
	for _, p := range builtinProviders() {
		builtin[p.ID] = true
	}
	if len(testedSupport) > 0 && !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(SupportTestedAt) {
		t.Fatalf("support data without a valid test date: %q", SupportTestedAt)
	}
	for id, names := range testedSupport {
		if !builtin[id] {
			t.Errorf("tested support for unknown provider %q", id)
		}
		seen := map[string]bool{}
		for _, n := range names {
			if !known[n] {
				t.Errorf("%s: %q is not in the service catalogue (regenerate with tools/probesupport)", id, n)
			}
			if seen[n] {
				t.Errorf("%s: %q listed twice", id, n)
			}
			seen[n] = true
		}
	}
	// the data reaches the providers, with its date
	for _, p := range BuiltinProviders() {
		if names, ok := testedSupport[p.ID]; ok && (len(p.Tested) != len(names) || p.TestedAt != SupportTestedAt) {
			t.Errorf("%s: tested data not applied: %+v", p.ID, p)
		}
	}
}
