// Package sysdns points the operating system's DNS at the local resolver and
// restores the original settings. The original settings are written to disk
// before anything is changed, so they survive crashes and power loss.
package sysdns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Item is the saved DNS state of one interface / network service.
type Item struct {
	Key     string   `json:"key"`              // stable id: adapter GUID, service name, link name
	Name    string   `json:"name"`             // human readable
	Device  string   `json:"device,omitempty"` // macOS: BSD device, survives a service rename
	V4      []string `json:"v4,omitempty"`
	V6      []string `json:"v6,omitempty"`
	Auto4   bool     `json:"auto4"` // DNS came from DHCP / not statically configured
	Auto6   bool     `json:"auto6"`
	Domains []string `json:"domains,omitempty"`
}

// Backup is the on-disk state.
type Backup struct {
	Platform   string           `json:"platform"`
	Saved      time.Time        `json:"saved"`
	Items      map[string]*Item `json:"items"`
	ResolvConf *FileBackup      `json:"resolv_conf,omitempty"`
}

// FileBackup preserves a file or symlink.
type FileBackup struct {
	Path    string `json:"path"`
	Link    string `json:"link,omitempty"`
	Content []byte `json:"content,omitempty"`
	Missing bool   `json:"missing,omitempty"`
}

// Status describes the current state for the UI.
type Status struct {
	Applied    bool     `json:"applied"`
	Interfaces []string `json:"interfaces"`
	Method     string   `json:"method"`
	Warnings   []string `json:"warnings,omitempty"`
}

type platform interface {
	// ensure points every active interface at servers. For each interface not yet
	// in b it must record the original state in b and call save() BEFORE changing it.
	ensure(servers []string, b *Backup, save func() error) (warnings []string, err error)
	restore(b *Backup) error
	flush()
	method() string
}

// Manager is safe for concurrent use.
type Manager struct {
	mu       sync.Mutex
	path     string
	p        platform
	warnings []string
}

// New creates a manager storing its backup at path.
func New(path string) *Manager { return &Manager{path: path, p: newPlatform()} }

func (m *Manager) load() (*Backup, error) {
	b, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var bk Backup
	if err := json.Unmarshal(b, &bk); err != nil {
		return nil, fmt.Errorf("corrupt dns backup %s: %w", m.path, err)
	}
	if bk.Items == nil {
		bk.Items = map[string]*Item{}
	}
	return &bk, nil
}

func (m *Manager) save(b *Backup) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	// A planted tmp (symlink) must not be followed: drop it, then create exclusively.
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil { // must hit the disk before we touch system DNS
		f.Close()
		return err
	}
	f.Close()
	if err := os.Rename(tmp, m.path); err != nil {
		return err
	}
	syncDir(filepath.Dir(m.path)) // make the rename itself durable (power loss)
	return nil
}

func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync() // not supported for directories on Windows; NTFS journals the rename
	d.Close()
}

// Apply points the system at servers (e.g. 127.0.0.1, ::1). Idempotent; call it
// periodically to catch new interfaces or settings overwritten by the OS.
func (m *Manager) Apply(servers []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := m.load()
	if err != nil {
		return err
	}
	if b == nil {
		b = &Backup{Platform: platformName, Saved: time.Now(), Items: map[string]*Item{}}
	}
	w, err := m.p.ensure(servers, b, func() error { return m.save(b) })
	m.warnings = w
	if err != nil {
		return err
	}
	if err := m.save(b); err != nil {
		return err
	}
	m.p.flush()
	return nil
}

// Restore puts the original settings back and deletes the backup.
func (m *Manager) Restore() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := m.load()
	if err != nil || b == nil {
		return err
	}
	if err := m.p.restore(b); err != nil {
		return err
	}
	m.p.flush()
	m.warnings = nil
	return os.Remove(m.path)
}

// HasBackup reports whether a backup exists (system DNS currently modified).
func (m *Manager) HasBackup() bool {
	_, err := os.Stat(m.path)
	return err == nil
}

// Status returns what is currently applied.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Method: m.p.method(), Warnings: m.warnings}
	b, _ := m.load()
	if b != nil {
		st.Applied = true
		for _, it := range b.Items {
			st.Interfaces = append(st.Interfaces, it.Name)
		}
		if b.ResolvConf != nil {
			st.Interfaces = append(st.Interfaces, b.ResolvConf.Path)
		}
	}
	return st
}

// FlushOSCache clears the OS resolver cache.
func (m *Manager) FlushOSCache() { m.p.flush() }

// cmdTimeout bounds every system command so a hung netsh/resolvectl/networksetup
// cannot block shutdown past the service manager's kill deadline.
var cmdTimeout = 30 * time.Second

func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, s)
	}
	return s, nil
}

func equalSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[strings.ToLower(x)]++
	}
	for _, x := range b {
		m[strings.ToLower(x)]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func split(servers []string) (v4, v6 []string) {
	for _, s := range servers {
		if strings.Contains(s, ":") {
			v6 = append(v6, s)
		} else {
			v4 = append(v4, s)
		}
	}
	return
}
