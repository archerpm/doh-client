//go:build darwin

package sysdns

import (
	"errors"
	"net"
	"strings"
)

const platformName = "darwin"

func newPlatform() platform { return macDNS{} }

type macDNS struct{}

func (macDNS) method() string { return "networksetup (per network service)" }

// services lists network services; disabled ones ("*" prefix) only if withDisabled.
func services(withDisabled bool) ([]string, error) {
	out, err := run("/usr/sbin/networksetup", "-listallnetworkservices")
	if err != nil {
		return nil, err
	}
	var s []string
	for i, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if i == 0 || l == "" { // header
			continue
		}
		if strings.HasPrefix(l, "*") {
			if !withDisabled {
				continue
			}
			l = strings.TrimSpace(strings.TrimPrefix(l, "*"))
		}
		s = append(s, l)
	}
	return s, nil
}

// serviceDevices maps network service name -> BSD device (en0, ...) from
// `networksetup -listnetworkserviceorder`. The device survives a rename of the
// service; services without hardware (VPN, ...) are omitted.
func serviceDevices() (map[string]string, error) {
	out, err := run("/usr/sbin/networksetup", "-listnetworkserviceorder")
	if err != nil {
		return nil, err
	}
	return parseServiceOrder(out), nil
}

func parseServiceOrder(out string) map[string]string {
	m := map[string]string{}
	name := ""
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "(Hardware Port:"):
			if i := strings.Index(l, "Device:"); i >= 0 && name != "" {
				dev := strings.TrimSpace(strings.TrimRight(l[i+len("Device:"):], ")"))
				if dev != "" {
					m[name] = dev
				}
			}
		case strings.HasPrefix(l, "("):
			if j := strings.Index(l, ")"); j >= 0 {
				name = strings.TrimSpace(l[j+1:])
			}
		}
	}
	return m
}

func getDNS(svc string) ([]string, error) {
	out, err := run("/usr/sbin/networksetup", "-getdnsservers", svc)
	if err != nil {
		return nil, err
	}
	var ips []string
	for _, l := range strings.Split(out, "\n") {
		if ip := net.ParseIP(strings.TrimSpace(l)); ip != nil {
			ips = append(ips, ip.String())
		}
	}
	return ips, nil // "There aren't any DNS Servers set" -> empty (DHCP)
}

func setDNS(svc string, servers []string) error {
	args := []string{"-setdnsservers", svc}
	if len(servers) == 0 {
		args = append(args, "Empty")
	} else {
		args = append(args, servers...)
	}
	_, err := run("/usr/sbin/networksetup", args...)
	return err
}

func (macDNS) ensure(servers []string, b *Backup, save func() error) ([]string, error) {
	svcs, err := services(false)
	if err != nil {
		return nil, err
	}
	devs, _ := serviceDevices() // best effort: only needed to survive a rename
	var errs []error
	for _, s := range svcs {
		cur, err := getDNS(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if equalSet(cur, servers) {
			continue
		}
		if _, ok := b.Items[s]; !ok {
			b.Items[s] = &Item{Key: s, Name: s, Device: devs[s], V4: cur, Auto4: len(cur) == 0}
			if err := save(); err != nil {
				return nil, err
			}
		}
		if err := setDNS(s, servers); err != nil {
			errs = append(errs, err)
		}
	}
	return nil, errors.Join(errs...)
}

func (macDNS) restore(b *Backup) error {
	// Include disabled services: a service switched off while we were active
	// still carries our 127.0.0.1 and must get its original DNS back.
	svcs, err := services(true)
	if err != nil {
		return err // keep the backup; nothing can be verified
	}
	present := map[string]bool{}
	for _, s := range svcs {
		present[s] = true
	}
	devs, _ := serviceDevices()
	byDevice := map[string]string{}
	for s, d := range devs {
		if present[s] {
			byDevice[d] = s
		}
	}
	var errs []error
	for k, it := range b.Items {
		target := k
		if !present[k] {
			// renamed while we were active: find it again by device
			if s, ok := byDevice[it.Device]; ok && it.Device != "" && b.Items[s] == nil {
				target = s
			} else {
				continue // service removed; nothing to restore
			}
		}
		if err := setDNS(target, it.V4); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (macDNS) flush() {
	_, _ = run("/usr/bin/dscacheutil", "-flushcache")
	_, _ = run("/usr/bin/killall", "-HUP", "mDNSResponder")
}
