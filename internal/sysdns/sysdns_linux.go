//go:build linux

package sysdns

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const platformName = "linux"

var resolvConf = "/etc/resolv.conf"

const marker = "# managed by doh-client"

func newPlatform() platform { return linuxDNS{} }

type linuxDNS struct{}

// usesResolved: systemd-resolved is active and /etc/resolv.conf points at its stub.
func usesResolved() bool {
	if _, err := exec.LookPath("resolvectl"); err != nil {
		return false
	}
	if _, err := os.Stat("/run/systemd/resolve/resolv.conf"); err != nil {
		return false
	}
	if t, err := os.Readlink(resolvConf); err == nil && strings.Contains(t, "/run/systemd/resolve/") {
		return true
	}
	b, _ := os.ReadFile(resolvConf)
	return bytes.Contains(b, []byte("127.0.0.53"))
}

func (linuxDNS) method() string {
	if usesResolved() {
		return "systemd-resolved (resolvectl, per link)"
	}
	return resolvConf
}

// resolvectlList parses "Link 2 (eth0): a b c" -> [a b c].
func resolvectlList(sub, iface string) ([]string, error) {
	out, err := run("resolvectl", sub, iface)
	if err != nil {
		return nil, err
	}
	if i := strings.Index(out, "):"); i >= 0 {
		out = out[i+2:]
	} else if i := strings.Index(out, ":"); i >= 0 {
		out = out[i+1:]
	}
	var r []string
	for _, f := range strings.Fields(out) {
		r = append(r, f)
	}
	return r, nil
}

func withRouteAll(doms []string) []string {
	out := make([]string, 0, len(doms)+1)
	for _, d := range doms {
		if d != "~." {
			out = append(out, d)
		}
	}
	return append(out, "~.")
}

func withoutRouteAll(doms []string) []string {
	out := make([]string, 0, len(doms))
	for _, d := range doms {
		if d != "~." {
			out = append(out, d)
		}
	}
	return out
}

// globalHasDNS reports whether the "Global" section of `resolvectl status` lists servers.
func globalHasDNS(status string) bool {
	inGlobal := false
	for _, ln := range strings.Split(status, "\n") {
		t := strings.TrimSpace(ln)
		switch {
		case t == "Global":
			inGlobal = true
		case strings.HasPrefix(t, "Link "):
			inGlobal = false
		case inGlobal && (strings.HasPrefix(t, "DNS Servers:") || strings.HasPrefix(t, "Current DNS Server:")):
			return true
		}
	}
	return false
}

// hadRouteAll: the link already routed all names before we touched it (stored in
// Item.Auto4 for resolved links, which has no DHCP/static distinction to keep).
func hadRouteAll(doms []string) bool {
	for _, d := range doms {
		if d == "~." {
			return true
		}
	}
	return false
}

func links() []net.Interface {
	ifs, _ := net.Interfaces()
	var out []net.Interface
	for _, i := range ifs {
		if i.Flags&net.FlagUp != 0 && i.Flags&net.FlagLoopback == 0 {
			out = append(out, i)
		}
	}
	return out
}

// allLinks lists every existing interface except loopback, up or down.
func allLinks() []net.Interface {
	ifs, _ := net.Interfaces()
	var out []net.Interface
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback == 0 {
			out = append(out, i)
		}
	}
	return out
}

func (l linuxDNS) ensure(servers []string, b *Backup, save func() error) ([]string, error) {
	if usesResolved() {
		return l.ensureResolved(servers, b, save)
	}
	return nil, l.ensureFile(servers, b, save)
}

func (linuxDNS) ensureResolved(servers []string, b *Backup, save func() error) ([]string, error) {
	var warnings []string
	if out, err := run("resolvectl", "status", "--no-pager"); err == nil && globalHasDNS(out) {
		warnings = append(warnings, "в resolved.conf задан глобальный DNS — часть запросов может идти мимо клиента")
	}
	var errs []error
	for _, li := range links() {
		cur, err := resolvectlList("dns", li.Name)
		if err != nil {
			continue
		}
		if len(cur) == 0 {
			if _, ok := b.Items[li.Name]; !ok {
				continue // link without DNS does not resolve anything
			}
		}
		if equalSet(cur, servers) {
			// Servers are ours; make sure the routing domain is too (it may have
			// failed earlier, or the network manager changed only domains).
			if it, ok := b.Items[li.Name]; ok {
				doms, err := resolvectlList("domain", li.Name)
				if err == nil && !hadRouteAll(doms) {
					if !it.Auto4 {
						it.Domains = withoutRouteAll(doms)
						if err := save(); err != nil {
							return warnings, err
						}
					}
					if _, err := run("resolvectl", append([]string{"domain", li.Name}, withRouteAll(it.Domains)...)...); err != nil {
						errs = append(errs, err)
					}
				}
			}
			continue
		}
		if _, ok := b.Items[li.Name]; !ok {
			doms, derr := resolvectlList("domain", li.Name)
			if derr != nil {
				// An unread list would be saved as empty and lost for good: leave the link alone.
				errs = append(errs, fmt.Errorf("%s: не удалось прочитать домены поиска, интерфейс не изменён: %w", li.Name, derr))
				continue
			}
			b.Items[li.Name] = &Item{Key: li.Name, Name: li.Name, V4: cur, Domains: doms, Auto4: hadRouteAll(doms)}
			if err := save(); err != nil {
				return warnings, err
			}
		} else {
			// The network manager re-pushed its own servers: refresh the backup.
			it := b.Items[li.Name]
			it.V4 = cur
			if doms, err := resolvectlList("domain", li.Name); err == nil {
				// what the network manager pushed now; drop our "~." only if we added it
				if it.Auto4 {
					it.Domains = doms
				} else {
					it.Domains = withoutRouteAll(doms)
				}
			}
			if err := save(); err != nil {
				return warnings, err
			}
		}
		if _, err := run("resolvectl", append([]string{"dns", li.Name}, servers...)...); err != nil {
			errs = append(errs, err)
			continue
		}
		// "~." makes this link the default route for every name; the link's own
		// search domains are kept so short host names still work.
		if _, err := run("resolvectl", append([]string{"domain", li.Name}, withRouteAll(b.Items[li.Name].Domains)...)...); err != nil {
			errs = append(errs, err)
		}
	}
	return warnings, errors.Join(errs...)
}

// managedServers parses nameserver lines of a file written by us.
func managedServers(b []byte) ([]string, bool) {
	if !bytes.Contains(b, []byte(marker)) {
		return nil, false
	}
	var ns []string
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "nameserver" {
			ns = append(ns, f[1])
		}
	}
	return ns, true
}

// keptLines are resolver settings carried over from the original file
// (search domains and options), so short names keep resolving.
func keptLines(orig []byte) []string {
	var out []string
	for _, l := range strings.Split(string(orig), "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && (f[0] == "search" || f[0] == "domain" || f[0] == "options" || f[0] == "sortlist") {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

func (linuxDNS) ensureFile(servers []string, b *Backup, save func() error) error {
	cur, rerr := os.ReadFile(resolvConf)
	if ns, ok := managedServers(cur); ok && equalSet(ns, servers) {
		return nil
	}
	_, managed := managedServers(cur)
	if !managed {
		fb := &FileBackup{Path: resolvConf}
		if t, err := os.Readlink(resolvConf); err == nil {
			fb.Link = t
		} else if errors.Is(rerr, os.ErrNotExist) {
			fb.Missing = true
		} else if rerr != nil {
			return rerr
		} else {
			fb.Content = cur
		}
		// A missing file is not a better original than one we already saved
		// (e.g. we died halfway through a restore).
		if !(fb.Missing && b.ResolvConf != nil) {
			b.ResolvConf = fb // latest unmanaged content wins (NetworkManager may rewrite it)
			if err := save(); err != nil {
				return err
			}
		}
	}
	var orig []byte
	if b.ResolvConf != nil {
		orig = b.ResolvConf.Content
		if b.ResolvConf.Link != "" {
			target := b.ResolvConf.Link
			if !filepath.IsAbs(target) { // relative links resolve against the link's directory
				target = filepath.Join(filepath.Dir(resolvConf), target)
			}
			orig, _ = os.ReadFile(target)
		}
	}
	var sb strings.Builder
	sb.WriteString(marker + "\n")
	for _, s := range servers {
		sb.WriteString("nameserver " + s + "\n")
	}
	kept := keptLines(orig)
	hasOpts := false
	for _, l := range kept {
		sb.WriteString(l + "\n")
		if strings.HasPrefix(l, "options") {
			hasOpts = true
		}
	}
	if !hasOpts {
		sb.WriteString("options edns0 trust-ad\n")
	}
	return replaceFile(resolvConf, []byte(sb.String()), "")
}

// replaceFile atomically puts content (or a symlink to link) at path: the old
// file stays in place until the rename, so there is no moment without one.
func replaceFile(path string, content []byte, link string) error {
	tmp := path + ".doh-client.tmp"
	_ = os.Remove(tmp)
	var err error
	if link != "" {
		err = os.Symlink(link, tmp)
	} else {
		err = os.WriteFile(tmp, content, 0o644)
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (linuxDNS) restore(b *Backup) error {
	var errs []error
	// An interface that is administratively down still keeps its resolved settings:
	// it must be restored too, or its original servers are lost with the backup.
	present := map[string]bool{}
	for _, li := range allLinks() {
		present[li.Name] = true
	}
	for name, it := range b.Items {
		if !present[name] {
			continue
		}
		args := append([]string{"dns", name}, it.V4...)
		if len(it.V4) == 0 {
			args = append(args, "")
		}
		if _, err := run("resolvectl", args...); err != nil {
			errs = append(errs, err)
		}
		dargs := append([]string{"domain", name}, it.Domains...)
		if len(it.Domains) == 0 {
			dargs = append(dargs, "")
		}
		if _, err := run("resolvectl", dargs...); err != nil {
			errs = append(errs, err)
		}
	}
	if fb := b.ResolvConf; fb != nil {
		var err error
		switch {
		// Always the real resolv.conf, never a path taken from the backup file.
		case fb.Link != "":
			err = replaceFile(resolvConf, nil, fb.Link)
		case fb.Missing:
			if cur, rerr := os.ReadFile(resolvConf); rerr == nil {
				if _, ours := managedServers(cur); ours {
					err = os.Remove(resolvConf)
				}
			}
		default:
			err = replaceFile(resolvConf, fb.Content, "")
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (linuxDNS) flush() {
	if usesResolved() {
		_, _ = run("resolvectl", "flush-caches")
	}
}
