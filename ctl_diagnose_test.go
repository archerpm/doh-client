package main

import (
	"path/filepath"
	"testing"

	"github.com/archerpm/doh-client/internal/api"
)

func TestCtlDiagnoseArguments(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		code    int
		handled bool
	}{
		{nil, 0, false}, {[]string{"status"}, 0, false},
		{[]string{"diagnose"}, 2, true}, {[]string{"diagnose", "--json"}, 2, true},
		{[]string{"diagnose", "site.test", "--jsno"}, 2, true},
		{[]string{"diagnose", "site.test", "--json", "extra"}, 2, true},
	} {
		code, handled := ctlDiagnose(nil, tc.args)
		if code != tc.code || handled != tc.handled {
			t.Fatalf("%v: %d %v", tc.args, code, handled)
		}
	}
	cl := api.NewClient("127.0.0.1:9180", filepath.Join(t.TempDir(), "missing"))
	for _, args := range [][]string{{"diagnose", "site.test"}, {"diagnose", "site.test", "--json"}} {
		if code, handled := ctlDiagnose(cl, args); code != 3 || !handled {
			t.Fatalf("%d %v", code, handled)
		}
	}
}
