package main

import (
	"testing"

	"github.com/archerpm/doh-client/internal/config"
)

func TestCloseHidesToTray(t *testing.T) {
	for state, want := range map[string]bool{
		"running":       true,
		"starting":      true,
		"stopped":       false,
		"not_installed": false,
		"no_access":     false,
		"unknown":       false,
		"":              false,
	} {
		if got := closeHidesToTray(state); got != want {
			t.Errorf("closeHidesToTray(%q) = %v, want %v", state, got, want)
		}
	}
}

func TestActionsFor(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  serviceActions
	}{
		{"running", serviceActions{Uninstall: true, Stop: true, Restart: true}},
		{"starting", serviceActions{Uninstall: true, Stop: true, Restart: true}},
		{"stopped", serviceActions{Uninstall: true, Start: true}},
		{"not_installed", serviceActions{Install: true}},
		{"no_access", serviceActions{Install: true}},
		{"unknown", serviceActions{Install: true}},
	} {
		if got := actionsFor(tc.state); got != tc.want {
			t.Errorf("actionsFor(%q) = %+v, want %+v", tc.state, got, tc.want)
		}
	}
}

func TestProviderItems(t *testing.T) {
	c := config.Config{
		ActiveProvider: "b",
		Providers:      []config.Provider{{ID: "a", Name: "Alpha"}, {ID: "b"}},
	}
	got := providerItems(c)
	want := []providerItem{{"a", "Alpha", false}, {"b", "b", true}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestTrayModelSignature(t *testing.T) {
	base := trayModel{Service: "running", Active: true, Providers: []providerItem{{"a", "Alpha", true}}}
	same := base
	if base.signature() != same.signature() {
		t.Fatal("equal models must have equal signatures")
	}
	for name, m := range map[string]trayModel{
		"service":   {Service: "stopped", Active: true, Providers: base.Providers},
		"active":    {Service: "running", Active: false, Providers: base.Providers},
		"autostart": {Service: "running", Active: true, Providers: base.Providers, Autostart: true},
		"provider":  {Service: "running", Active: true, Providers: []providerItem{{"a", "Alpha", false}}},
	} {
		if m.signature() == base.signature() {
			t.Errorf("%s change must change the signature", name)
		}
	}
}

func TestServiceTitle(t *testing.T) {
	if serviceTitle("running", true) == serviceTitle("running", false) {
		t.Error("active and inactive titles must differ")
	}
	if serviceTitle("not_installed", true) != "○ Служба не установлена" {
		t.Error("not installed title wrong")
	}
}
