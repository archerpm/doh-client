// Package conflict finds running programs known to take over DNS on the machine
// (VPN/proxy cores with a TUN adapter and their own resolver), so the client can
// warn that its answers may be bypassed. It only looks at process names.
package conflict

import (
	"strings"
	"sync"
	"time"
)

// tool is a program to warn about; needles are lower-case parts of a process name.
type tool struct {
	name    string
	needles []string
}

var tools = []tool{
	{"GammaTunnel", []string{"gammatunnel"}},
	{"sing-box", []string{"sing-box", "singbox"}},
}

// Match returns the display names of known tools among the process names,
// each once, in table order.
func Match(procs []string) []string {
	var out []string
	for _, t := range tools {
		if found(procs, t.needles) {
			out = append(out, t.name)
		}
	}
	return out
}

func found(procs, needles []string) bool {
	for _, p := range procs {
		p = strings.ToLower(p)
		for _, n := range needles {
			if strings.Contains(p, n) {
				return true
			}
		}
	}
	return false
}

// Warnings turns detected tool names into user-facing messages.
func Warnings(names []string) []string {
	var out []string
	for _, n := range names {
		out = append(out, "Запущен "+n+": он может перехватывать DNS-запросы мимо клиента — "+
			"если домены не открываются через туннель, отключите его DNS/TUN или остановите программу")
	}
	return out
}

// Detector lists matching processes, caching the result so that frequent status
// polls do not walk the process table each time.
type Detector struct {
	list func() ([]string, error) // process names; nil uses the OS
	ttl  time.Duration

	mu   sync.Mutex
	at   time.Time
	last []string
}

// NewDetector returns a detector that rescans at most once per ttl.
func NewDetector(ttl time.Duration) *Detector { return &Detector{ttl: ttl} }

// Names returns the display names of conflicting tools that are running now.
// A failed scan reports nothing: a warning must not be guessed.
func (d *Detector) Names() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.at.IsZero() && time.Since(d.at) < d.ttl {
		return append([]string(nil), d.last...)
	}
	list := d.list
	if list == nil {
		list = processNames
	}
	procs, err := list()
	d.at = time.Now()
	if err != nil {
		d.last = nil
	} else {
		d.last = Match(procs)
	}
	return append([]string(nil), d.last...)
}

// NewDetectorFunc is NewDetector with a custom process lister (for tests).
func NewDetectorFunc(ttl time.Duration, list func() ([]string, error)) *Detector {
	return &Detector{ttl: ttl, list: list}
}
