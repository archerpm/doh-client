package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/archerpm/doh-client/internal/config"
)

// maxListBytes bounds a downloaded list (GeoHide's hosts file is ~135 KB).
const maxListBytes = 4 << 20

// ProviderListResult is one fetched, parsed provider list.
type ProviderListResult struct {
	Provider string   `json:"provider"`
	List     string   `json:"list"`
	Name     string   `json:"name"`
	Domains  []string `json:"domains"`
}

// ErrNoProviderList: the provider is unknown or publishes no such list.
var ErrNoProviderList = errors.New("у провайдера нет такого опубликованного списка")

// fetchListBody downloads a list (replaceable in tests). The client goes direct:
// the service must not depend on the tunnel it is about to configure.
var fetchListBody = func(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("недопустимый адрес списка %q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("сервер списка ответил %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxListBytes {
		return nil, errors.New("список слишком большой")
	}
	return b, nil
}

// ProviderList fetches the domains a built-in provider publishes. The list is
// looked up in the built-in definitions, never in the saved config, so the URL
// cannot be chosen by a config write.
func (d *Daemon) ProviderList(ctx context.Context, providerID, listID string) (ProviderListResult, error) {
	for _, p := range config.BuiltinProviders() {
		if p.ID != providerID {
			continue
		}
		for _, l := range p.Lists {
			if l.ID != listID {
				continue
			}
			body, err := fetchListBody(ctx, l.URL)
			if err != nil {
				return ProviderListResult{}, fmt.Errorf("%s: %w", l.Name, err)
			}
			domains := config.ParseDomainList(body)
			if len(domains) == 0 {
				return ProviderListResult{}, fmt.Errorf("%s: в списке нет доменов", l.Name)
			}
			return ProviderListResult{Provider: p.ID, List: l.ID, Name: l.Name, Domains: domains}, nil
		}
	}
	return ProviderListResult{}, ErrNoProviderList
}
