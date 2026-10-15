package resolver

import (
	"sync"
	"time"
)

// Entry is one logged query.
type Entry struct {
	Seq      uint64        `json:"seq"`
	Time     time.Time     `json:"time"`
	Client   string        `json:"client"`
	Name     string        `json:"name"`
	Type     string        `json:"type"`
	Rcode    string        `json:"rcode"`
	Answers  []string      `json:"answers,omitempty"`
	Provider string        `json:"provider"`
	Rule     string        `json:"rule,omitempty"`
	Cached   bool          `json:"cached"`
	Synth    bool          `json:"synth"`
	Duration time.Duration `json:"duration_ns"`
	Error    string        `json:"error,omitempty"`
	Tried    []string      `json:"tried,omitempty"`
}

// QueryLog is a fixed-size ring buffer.
type QueryLog struct {
	mu   sync.Mutex
	buf  []Entry
	next int
	full bool
	seq  uint64
}

func NewQueryLog(n int) *QueryLog { return &QueryLog{buf: make([]Entry, n)} }

func (l *QueryLog) Add(e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e.Seq = l.seq
	l.buf[l.next] = e
	l.next = (l.next + 1) % len(l.buf)
	if l.next == 0 {
		l.full = true
	}
}

// After returns up to limit entries with Seq > after, oldest first.
func (l *QueryLog) After(after uint64, limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if after > l.seq {
		after = 0 // cursor from a previous service instance (restart reset seq)
	}
	n := l.next
	start := 0
	if l.full {
		n = len(l.buf)
		start = l.next
	}
	out := make([]Entry, 0, 64)
	for i := 0; i < n; i++ {
		e := l.buf[(start+i)%len(l.buf)]
		if e.Seq > after {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Resize changes capacity, keeping the newest entries (atomically).
func (l *QueryLog) Resize(n int) {
	if n <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if n == len(l.buf) {
		return
	}
	count, start := l.next, 0
	if l.full {
		count, start = len(l.buf), l.next
	}
	old := make([]Entry, 0, count)
	for i := 0; i < count; i++ {
		old = append(old, l.buf[(start+i)%len(l.buf)])
	}
	if len(old) > n {
		old = old[len(old)-n:]
	}
	l.buf = make([]Entry, n)
	l.next, l.full = 0, false
	for _, e := range old {
		l.buf[l.next] = e
		l.next = (l.next + 1) % n
		if l.next == 0 {
			l.full = true
		}
	}
}
