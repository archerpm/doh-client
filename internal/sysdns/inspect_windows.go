//go:build windows

package sysdns

import "context"

func inspectPlatform(ctx context.Context, expected []string, emit func(DNSInterface) bool) error {
	ads, err := adapters()
	if err != nil {
		return err
	}
	for _, ad := range ads {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !emit(inspectAdapter(ad, expected)) {
			return ctx.Err()
		}
	}
	return nil
}

func inspectAdapter(ad adapter, expected []string) DNSInterface {
	want := append([]string{}, expected...)
	_, v6 := split(expected)
	// ensure installs ::1 for IPv6 when only IPv4 listens; GetAdaptersAddresses
	// can expose IPv4 DNS alone, so both live representations are accepted.
	if len(expected) > 0 && len(v6) == 0 && ad.idx6 != 0 && !sameDNS(ad.current, expected) {
		want = append(want, "::1")
	}
	// ...and 127.0.0.1 for IPv4 when only IPv6 listens.
	if v4, _ := split(expected); len(expected) > 0 && len(v4) == 0 && !sameDNS(ad.current, expected) {
		want = append(want, "127.0.0.1")
	}
	// An adapter without IPv6 (binding disabled) can never carry ::1:
	// ensure skips it too, so do not report that as a mismatch.
	if ad.idx6 == 0 {
		if v4, v6 := split(want); len(v4) > 0 && len(v6) > 0 {
			want = v4
		}
	}
	return DNSInterface{Key: ad.guid, Name: ad.name, Expected: want, Actual: ad.current}
}
