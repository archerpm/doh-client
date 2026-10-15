package daemon

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestProviderList(t *testing.T) {
	orig := fetchListBody
	t.Cleanup(func() { fetchListBody = orig })

	var asked []string
	fetchListBody = func(_ context.Context, url string) ([]byte, error) {
		asked = append(asked, url)
		return []byte("# GeoHide\nClaude.ai\n1.2.3.4 cursor.com\nclaude.ai\n"), nil
	}
	d := &Daemon{}
	got, err := d.ProviderList(context.Background(), "geohide", "google-ai")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "geohide" || got.List != "google-ai" || got.Name != "Google AI" || !reflect.DeepEqual(got.Domains, []string{"claude.ai", "cursor.com"}) {
		t.Fatalf("%+v", got)
	}
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "https://raw.githubusercontent.com/Internet-Helper/GeoHideDNS/") {
		t.Fatalf("fetched %v", asked)
	}

	// unknown provider / list / provider without lists: nothing is fetched
	asked = nil
	for _, c := range [][2]string{{"nope", "x"}, {"geohide", "nope"}, {"cloudflare", "x"}, {"", ""}} {
		if _, err := d.ProviderList(context.Background(), c[0], c[1]); !errors.Is(err, ErrNoProviderList) {
			t.Errorf("%v: err = %v", c, err)
		}
	}
	if len(asked) != 0 {
		t.Fatalf("fetched %v for unknown lists", asked)
	}

	// download errors and empty lists are reported with the list name
	fetchListBody = func(context.Context, string) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := d.ProviderList(context.Background(), "geohide", "supercell"); err == nil || !strings.Contains(err.Error(), "Supercell") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("fetch error: %v", err)
	}
	fetchListBody = func(context.Context, string) ([]byte, error) { return []byte("# nothing\n127.0.0.1 localhost\n"), nil }
	if _, err := d.ProviderList(context.Background(), "geohide", "supercell"); err == nil || !strings.Contains(err.Error(), "нет доменов") {
		t.Errorf("empty list: %v", err)
	}
}

func TestFetchListBodyRejectsUnsafeURLs(t *testing.T) {
	for _, u := range []string{"http://example.com/x", "file:///etc/passwd", "https://user:pw@example.com/x", "https:///x", "ftp://example.com/x", "://bad"} {
		if _, err := fetchListBody(context.Background(), u); err == nil {
			t.Errorf("%q was fetched", u)
		}
	}
}
