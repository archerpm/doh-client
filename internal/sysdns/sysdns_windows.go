//go:build windows

package sysdns

import (
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const platformName = "windows"

func newPlatform() platform { return winDNS{} }

func hideWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

type winDNS struct{}

func (winDNS) method() string { return "netsh (per adapter, IPv4+IPv6)" }

type adapter struct {
	guid    string
	name    string
	idx4    uint32
	idx6    uint32
	current []string
}

func adapters() ([]adapter, error) {
	size := uint32(16 * 1024)
	var buf []byte
	for i := 0; i < 5; i++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC,
			windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST,
			0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			return nil, err
		}
		if i == 4 {
			return nil, err
		}
	}
	var out []adapter
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		// Skip loopback and tunnel pseudo-interfaces (Teredo, 6to4, ISATAP, IP-HTTPS):
		// they carry no resolver config of their own. VPN adapters (TAP, Wintun) are
		// Ethernet/virtual types and are still covered.
		if a.OperStatus != windows.IfOperStatusUp || a.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK || a.IfType == windows.IF_TYPE_TUNNEL {
			continue
		}
		ad := adapter{
			guid: windows.BytePtrToString(a.AdapterName),
			name: windows.UTF16PtrToString(a.FriendlyName),
			idx4: a.IfIndex,
			idx6: a.Ipv6IfIndex,
		}
		for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
			if ip, ok := sockaddrIP(d.Address); ok {
				ad.current = append(ad.current, ip)
			}
		}
		out = append(out, ad)
	}
	return out, nil
}

func sockaddrIP(sa windows.SocketAddress) (string, bool) {
	if sa.Sockaddr == nil {
		return "", false
	}
	switch sa.Sockaddr.Addr.Family {
	case windows.AF_INET:
		p := (*windows.RawSockaddrInet4)(unsafe.Pointer(sa.Sockaddr))
		return netip.AddrFrom4(p.Addr).String(), true
	case windows.AF_INET6:
		p := (*windows.RawSockaddrInet6)(unsafe.Pointer(sa.Sockaddr))
		return netip.AddrFrom16(p.Addr).String(), true
	}
	return "", false
}

func regKey(v6 bool, guid string) string {
	svc := "Tcpip"
	if v6 {
		svc = "Tcpip6"
	}
	return `SYSTEM\CurrentControlSet\Services\` + svc + `\Parameters\Interfaces\` + guid
}

// staticServers reads the statically configured NameServer list (empty = DHCP).
func staticServers(v6 bool, guid string) []string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, regKey(v6, guid), registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	s, _, err := k.GetStringValue("NameServer")
	if err != nil {
		return nil
	}
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

func writeStatic(v6 bool, guid string, servers []string) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, regKey(v6, guid), registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("NameServer", strings.Join(servers, ","))
}

// netshRetryDelay: right after a switch (DHCP renew, link flap) an adapter is often busy for a moment.
var netshRetryDelay = 700 * time.Millisecond

// netshSet applies the servers with one more try after a short pause.
func netshSet(family string, idx uint32, servers []string) error {
	start := time.Now()
	err := netshSetOnce(family, idx, servers)
	if err == nil || time.Since(start) > netshRetryWithin {
		return err // a hung netsh is not tried again: that would only double the wait
	}
	time.Sleep(netshRetryDelay)
	return netshSetOnce(family, idx, servers)
}

// netshRetryWithin: only a failure that came back this fast is tried once more.
const netshRetryWithin = 5 * time.Second

func netshSetOnce(family string, idx uint32, servers []string) error {
	name := "name=" + strconv.FormatUint(uint64(idx), 10)
	if len(servers) == 0 {
		_, err := run("netsh", "interface", family, "set", "dnsservers", name, "source=dhcp")
		return err
	}
	if _, err := run("netsh", "interface", family, "set", "dnsservers", name, "source=static",
		"address="+servers[0], "register=primary", "validate=no"); err != nil {
		return err
	}
	for i, s := range servers[1:] {
		if _, err := run("netsh", "interface", family, "add", "dnsservers", name,
			"address="+s, "index="+strconv.Itoa(i+2), "validate=no"); err != nil {
			return err
		}
	}
	return nil
}

func (winDNS) ensure(servers []string, b *Backup, save func() error) ([]string, error) {
	v4, v6 := split(servers)
	if len(v6) == 0 {
		// No IPv6 listener: still replace the adapters' IPv6 DNS, otherwise
		// router-advertised IPv6 resolvers keep answering outside DoH. Nothing
		// listens on ::1, so Windows fails over to our IPv4 server at once.
		v6 = []string{"::1"}
	}
	if len(v4) == 0 {
		// Same for an IPv6-only listener: an empty v4 list would switch the adapters
		// back to DHCP and keep the provider's IPv4 resolvers outside DoH. Nothing
		// listens on 127.0.0.1, so Windows fails over to our IPv6 server at once.
		v4 = []string{"127.0.0.1"}
	}
	ads, err := adapters()
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, a := range ads {
		if _, ok := b.Items[a.guid]; !ok {
			if eff := append(append([]string{}, v4...), v6...); equalSet(a.current, eff) || equalSet(a.current, v4) {
				continue // already ours (e.g. leftover); nothing sensible to back up
			}
			s4, s6 := staticServers(false, a.guid), staticServers(true, a.guid)
			b.Items[a.guid] = &Item{Key: a.guid, Name: a.name, V4: s4, V6: s6, Auto4: len(s4) == 0, Auto6: len(s6) == 0}
			if err := save(); err != nil {
				return nil, err
			}
		}
		cur4 := staticServers(false, a.guid)
		if !equalSet(cur4, v4) {
			if err := netshSet("ipv4", a.idx4, v4); err != nil {
				errs = append(errs, fmt.Errorf("адаптер %q (ipv4): %w", a.name, err))
			}
		}
		if a.idx6 != 0 && len(v6) > 0 && !equalSet(staticServers(true, a.guid), v6) {
			if err := netshSet("ipv6", a.idx6, v6); err != nil {
				errs = append(errs, fmt.Errorf("адаптер %q (ipv6): %w", a.name, err))
			}
		}
	}
	return nil, errors.Join(errs...)
}

func (winDNS) restore(b *Backup) error {
	ads, _ := adapters()
	byGUID := map[string]adapter{}
	for _, a := range ads {
		byGUID[a.guid] = a
	}
	var errs []error
	for guid, it := range b.Items {
		a, present := byGUID[guid]
		// Registry first: persistent config, also covers disconnected adapters.
		// A missing key means the adapter was removed — nothing to restore.
		for _, v6 := range []bool{false, true} {
			vals := it.V4
			if v6 {
				vals = it.V6
			}
			if err := writeStatic(v6, guid, vals); err != nil && !errors.Is(err, registry.ErrNotExist) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
				errs = append(errs, fmt.Errorf("%s registry (ipv6=%v): %w", it.Name, v6, err))
			}
		}
		if !present {
			continue
		}
		if err := netshSet("ipv4", a.idx4, it.V4); err != nil {
			errs = append(errs, fmt.Errorf("адаптер %q (ipv4): %w", it.Name, err))
		}
		if a.idx6 != 0 {
			if err := netshSet("ipv6", a.idx6, it.V6); err != nil {
				errs = append(errs, fmt.Errorf("адаптер %q (ipv6): %w", it.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (winDNS) flush() { _, _ = run("ipconfig", "/flushdns") }
