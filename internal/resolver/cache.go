package resolver

import (
	"container/list"
	"strconv"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	maxNegTTL = 300
	staleTTL  = 30            // RFC 8767 §5.11: TTL of an answer served past its expiry
	maxStale  = 1 * time.Hour // how long past its expiry an answer may still be served
)

// Cache is a TTL-aware LRU of DNS responses.
type Cache struct {
	mu    sync.Mutex
	max   int
	ll    *list.List
	items map[string]*list.Element
	now   func() time.Time
}

type cacheItem struct {
	key    string
	msg    *dns.Msg
	stored time.Time
	exp    time.Time
}

func NewCache(max int) *Cache {
	if max <= 0 {
		max = 4096
	}
	return &Cache{max: max, ll: list.New(), items: map[string]*list.Element{}, now: time.Now}
}

func cacheKey(name string, qtype, qclass uint16, q *dns.Msg) string {
	do, cd := false, q.CheckingDisabled
	if o := q.IsEdns0(); o != nil {
		do = o.Do()
	}
	k := name + "|" + strconv.Itoa(int(qtype)) + "|" + strconv.Itoa(int(qclass))
	if do {
		k += "|do"
	}
	if cd {
		k += "|cd"
	}
	return k
}

// Get returns a copy with TTLs decreased by the time spent in cache.
func (c *Cache) Get(key string) (*dns.Msg, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	it := el.Value.(*cacheItem)
	now := c.now()
	if !now.Before(it.exp) {
		// An expired answer is no hit, but it stays for GetStale until maxStale has passed.
		if now.Sub(it.exp) > maxStale {
			c.ll.Remove(el)
			delete(c.items, key)
		}
		return nil, false
	}
	c.ll.MoveToFront(el)
	m := it.msg.Copy()
	elapsed := uint32(now.Sub(it.stored) / time.Second)
	adjust := func(rrs []dns.RR) {
		for _, rr := range rrs {
			h := rr.Header()
			if h.Rrtype == dns.TypeOPT {
				continue
			}
			if h.Ttl > elapsed {
				h.Ttl -= elapsed
			} else {
				h.Ttl = 1
			}
		}
	}
	adjust(m.Answer)
	adjust(m.Ns)
	adjust(m.Extra)
	return m, true
}

// Delete drops the entry for key, if any.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.ll.Remove(el)
		delete(c.items, key)
	}
}

// GetStale returns an expired answer for use when the provider cannot be reached
// (RFC 8767): records carry a short TTL so that clients ask again soon. Nothing
// older than maxStale past its expiry is returned.
func (c *Cache) GetStale(key string) (*dns.Msg, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	it := el.Value.(*cacheItem)
	if c.now().Sub(it.exp) > maxStale {
		c.ll.Remove(el)
		delete(c.items, key)
		return nil, false
	}
	m := it.msg.Copy()
	// The signatures may have expired too: do not vouch for the data (AD) any more.
	m.AuthenticatedData = false
	for _, sec := range [][]dns.RR{m.Answer, m.Ns, m.Extra} {
		for _, rr := range sec {
			if h := rr.Header(); h.Rrtype != dns.TypeOPT {
				h.Ttl = staleTTL
			}
		}
	}
	return m, true
}

// Put stores m. Only NOERROR and NXDOMAIN are cached; truncated messages are not.
func (c *Cache) Put(key string, m *dns.Msg, minTTL, maxTTL uint32) {
	if m.Truncated || (m.Rcode != dns.RcodeSuccess && m.Rcode != dns.RcodeNameError) {
		return
	}
	ttl, ok := msgTTL(m)
	if !ok || ttl == 0 { // TTL 0 means "do not cache"; MinTTL must not override it
		// A final answer that cannot be kept still replaces the old one: the old one
		// must not come back later as a "stale" answer.
		c.Delete(key)
		return
	}
	neg := m.Rcode == dns.RcodeNameError || !hasType(m)
	if neg && ttl > maxNegTTL {
		ttl = maxNegTTL
	}
	if ttl < minTTL {
		ttl = minTTL
	}
	if ttl > maxTTL {
		ttl = maxTTL
	}
	if ttl == 0 {
		return
	}
	cp := m.Copy()
	// Clamp stored TTLs so clients never see more than we cache for.
	for _, sec := range [][]dns.RR{cp.Answer, cp.Ns, cp.Extra} {
		for _, rr := range sec {
			if h := rr.Header(); h.Rrtype != dns.TypeOPT && h.Ttl > ttl {
				h.Ttl = ttl
			}
		}
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value = &cacheItem{key: key, msg: cp, stored: now, exp: now.Add(time.Duration(ttl) * time.Second)}
		c.ll.MoveToFront(el)
		return
	}
	c.items[key] = c.ll.PushFront(&cacheItem{key: key, msg: cp, stored: now, exp: now.Add(time.Duration(ttl) * time.Second)})
	for c.ll.Len() > c.max {
		el := c.ll.Back()
		c.ll.Remove(el)
		delete(c.items, el.Value.(*cacheItem).key)
	}
}

// msgTTL returns how long m may be cached. Positive answers: the minimum TTL of
// the answer RRs. Negative answers (NXDOMAIN, or NOERROR without an RR of the
// queried type — NODATA, possibly behind a CNAME chain): min(SOA TTL, SOA MINIMUM)
// bounded by the CNAME TTLs (RFC 2308 §5); without an SOA they are not cached.
func msgTTL(m *dns.Msg) (uint32, bool) {
	var qtype uint16
	if len(m.Question) == 1 {
		qtype = m.Question[0].Qtype
	}
	var ttl uint32
	have := false
	lower := func(t uint32) {
		if !have || t < ttl {
			ttl, have = t, true
		}
	}
	positive := false
	for _, rr := range m.Answer {
		h := rr.Header()
		if h.Rrtype == qtype || qtype == dns.TypeANY {
			positive = true
		}
		lower(h.Ttl)
	}
	if m.Rcode == dns.RcodeSuccess && positive {
		return ttl, true
	}
	for _, rr := range m.Ns {
		if soa, ok := rr.(*dns.SOA); ok {
			lower(soa.Hdr.Ttl)
			lower(soa.Minttl)
			return ttl, true
		}
	}
	return 0, false
}

func hasType(m *dns.Msg) bool {
	if len(m.Question) != 1 {
		return len(m.Answer) > 0
	}
	for _, rr := range m.Answer {
		if rr.Header().Rrtype == m.Question[0].Qtype {
			return true
		}
	}
	return false
}

// Reset clears the cache; max>0 changes capacity.
func (c *Cache) Reset(max int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if max > 0 {
		c.max = max
	}
	c.ll.Init()
	c.items = map[string]*list.Element{}
}

func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
