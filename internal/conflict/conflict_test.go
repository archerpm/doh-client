package conflict

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMatch(t *testing.T) {
	got := Match([]string{"explorer.exe", "SING-BOX.exe", "sing-box", "GammaTunnel.exe", "chrome.exe"})
	if want := []string{"GammaTunnel", "sing-box"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Match = %v, want %v", got, want)
	}
	if got := Match([]string{"svchost.exe", "", "singing"}); got != nil {
		t.Fatalf("no tools running, got %v", got)
	}
	if got := Match([]string{"/Applications/GammaTunnel.app/Contents/MacOS/GammaTunnel"}); len(got) != 1 {
		t.Fatalf("macOS path: %v", got)
	}
}

func TestWarnings(t *testing.T) {
	w := Warnings([]string{"sing-box"})
	if len(w) != 1 || !strings.Contains(w[0], "sing-box") {
		t.Fatalf("Warnings = %v", w)
	}
	if Warnings(nil) != nil {
		t.Fatal("no names, no warnings")
	}
}

func TestDetectorCachesAndHidesScanErrors(t *testing.T) {
	calls := 0
	procs := []string{"sing-box"}
	var err error
	d := &Detector{ttl: time.Hour, list: func() ([]string, error) { calls++; return procs, err }}
	for i := 0; i < 3; i++ {
		if n := d.Names(); len(n) != 1 || n[0] != "sing-box" {
			t.Fatalf("Names = %v", n)
		}
	}
	if calls != 1 {
		t.Fatalf("scanned %d times within ttl, want 1", calls)
	}
	d.at = time.Time{}
	err = errors.New("denied")
	if n := d.Names(); n != nil {
		t.Fatalf("failed scan must report nothing, got %v", n)
	}
}

func TestProcessNamesRuns(t *testing.T) {
	if _, err := processNames(); err != nil {
		t.Fatal(err)
	}
}
