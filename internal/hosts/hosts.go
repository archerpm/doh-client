// Package hosts manages a marked block inside the system hosts file.
package hosts

import (
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	begin = "# >>> doh-client BEGIN (managed, do not edit) >>>"
	end   = "# <<< doh-client END <<<"
)

// Path returns the system hosts file path.
func Path() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return filepath.Join(root, "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

// Render produces the new file content with the managed block replaced by entries.
// entries maps domain -> addresses; an empty map removes the block.
func Render(orig []byte, entries map[string][]netip.Addr) []byte {
	nl := "\n"
	if bytes.Contains(orig, []byte("\r\n")) || (len(orig) == 0 && runtime.GOOS == "windows") {
		nl = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(string(orig), "\r\n", "\n"), "\n")
	var keep []string
	in := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case t == begin:
			in = true
		case t == end:
			in = false
		case !in:
			keep = append(keep, l)
		}
	}
	// trim trailing empty lines
	for len(keep) > 0 && strings.TrimSpace(keep[len(keep)-1]) == "" {
		keep = keep[:len(keep)-1]
	}
	if len(entries) > 0 {
		doms := make([]string, 0, len(entries))
		for d := range entries {
			doms = append(doms, d)
		}
		sort.Strings(doms)
		keep = append(keep, "", begin)
		for _, d := range doms {
			for _, a := range entries[d] {
				keep = append(keep, fmt.Sprintf("%s %s", a.String(), d))
			}
		}
		keep = append(keep, end)
	}
	return []byte(strings.Join(keep, nl) + nl)
}

// Write replaces the managed block in the hosts file. The new content goes to a
// temporary file next to it and is renamed over the original, so an interruption
// never leaves a truncated hosts file. Where rename is impossible (bind-mounted
// /etc/hosts in containers) it falls back to an in-place write. Before the first
// change the original file is copied to backupPath (if set) for manual recovery.
func Write(path string, entries map[string][]netip.Addr) error {
	orig, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	out := Render(orig, entries)
	if bytes.Equal(out, orig) {
		return nil
	}
	if BackupPath != "" && len(entries) > 0 && !bytes.Contains(orig, []byte(begin)) {
		_ = os.MkdirAll(filepath.Dir(BackupPath), 0o755)
		_ = os.WriteFile(BackupPath, orig, 0o600)
	}
	tmp := path + ".doh-client.tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err == nil {
		_, werr := f.Write(out)
		serr := f.Sync()
		cerr := f.Close()
		if werr == nil && serr == nil && cerr == nil {
			if err = os.Rename(tmp, path); err == nil {
				return nil
			}
		}
		_ = os.Remove(tmp)
	}
	// Fallback: in place (bind mounts, read-only parent directory).
	return os.WriteFile(path, out, mode)
}

// BackupPath receives a copy of the original hosts file before the first change.
var BackupPath string

// Managed returns the domains currently in the managed block.
func Managed(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	in := false
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == begin:
			in = true
		case t == end:
			in = false
		case in:
			if f := strings.Fields(t); len(f) >= 2 {
				out = append(out, f[1])
			}
		}
	}
	return out
}
