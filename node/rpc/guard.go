package rpc

import (
	"net"
	"strings"
	"sync"
	"time"
)

// sourceGuard is the public listener's guard (SECURITY-GAPS.md; the project's order
// of 21:4x UK, 11 October 2026): a token bucket per SOURCE (the remote
// address's host, so every connection and every batch item from one host
// draws on one bucket) at a rate and burst that match the edge's, and a deny
// set of methods and namespaces that a public listener does not serve (the
// dry run igneum_verifyBlock, the node's admin and write methods), which are
// served on the mesh and the local IPC only. A guard is attached to the
// rpc.Server of a listener flagged public (node/rpcstack.go); a connection
// with no remote address (IPC) is never limited.
type sourceGuard struct {
	rate  float64 // tokens a second
	burst float64 // bucket size
	deny  map[string]bool
	denyN map[string]bool // namespaces (the part before the underscore)
	// trusted is the set of edge hosts whose forwarded client address is
	// honoured as the source (sourceOf; --rpc.public.trusted).
	trusted map[string]bool
	// exempt names the sources the bucket never charges (--rpc.public.exempt,
	// 16 October 2026): the mesh's own addresses and prefixes, whose calls
	// (the feed's, the filter's, the pool's template calls, a node's own
	// readers) are not the public allowance's and arrive from one address
	// at a rate the public bucket refuses; the deny set still holds for
	// them. Hosts exactly, prefixes as CIDR.
	exemptHosts map[string]bool
	exemptNets  []*net.IPNet

	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time // the clock (a test sets it)
	lastGC  time.Time
	// refusals is the per-source count of refusals since that source's last
	// report, and reportAt when it was last reported: the refusal is logged
	// ONCE a minute per source with the count, not once a call (the ninth
	// development chain's public node, 12 October 2026: one mesh host sent
	// 20,000 to 30,000 calls a second against the limit, a million refusal
	// lines a minute, 9.4 GB of log, the listener's time spent on the
	// lines; the limiter held throughout).
	refusals map[string]*refusalCount
}

type refusalCount struct {
	n        uint64
	reportAt time.Time
	method   string // the last refused method
}

// sourceGuardReportEvery is how often one source's refusals are logged.
const sourceGuardReportEvery = time.Minute

type bucket struct {
	tokens float64
	last   time.Time
}

const (
	// sourceGuardGCEvery bounds the bucket map: idle sources are dropped on
	// a sweep every so often, so a flood of distinct sources cannot grow
	// the map without bound between sweeps.
	sourceGuardGCEvery = time.Minute
	sourceGuardIdle    = 2 * time.Minute
	sourceGuardMaxKeys = 65536
)

func newSourceGuard(rate, burst float64, denyMethods, denyNamespaces, trustedEdges, exemptSources []string) *sourceGuard {
	g := &sourceGuard{rate: rate, burst: burst, deny: make(map[string]bool), denyN: make(map[string]bool), trusted: make(map[string]bool), exemptHosts: make(map[string]bool), buckets: make(map[string]*bucket), refusals: make(map[string]*refusalCount), now: time.Now}
	for _, m := range denyMethods {
		g.deny[m] = true
	}
	for _, n := range denyNamespaces {
		g.denyN[n] = true
	}
	for _, t := range trustedEdges {
		if t = strings.TrimSpace(t); t != "" {
			g.trusted[sourceKey(t)] = true
		}
	}
	for _, e := range exemptSources {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(e); err == nil {
			g.exemptNets = append(g.exemptNets, n)
			continue
		}
		g.exemptHosts[sourceKey(e)] = true
	}
	g.lastGC = g.now()
	return g
}

// exempt reports whether a source key (a host) is never charged.
func (g *sourceGuard) exempt(key string) bool {
	if g.exemptHosts[key] {
		return true
	}
	if len(g.exemptNets) == 0 {
		return false
	}
	ip := net.ParseIP(key)
	if ip == nil {
		return false
	}
	for _, n := range g.exemptNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// sourceOf is the source a call is charged to: the peer's address, or,
// when the peer is a trusted edge (--rpc.public.trusted) and it forwarded
// a client address, that client's: the LAST address of X-Forwarded-For,
// the one the edge itself appended (an edge that appends to a
// client-supplied header leaves the client's own entries before it; an
// edge that resets the header leaves the one entry). The first entry is the
// client's to write and, with an exempt list (F-209), would let a client
// name an exempt host and run uncharged (the security review's read of the cut).
// The header is never read from an untrusted peer, so a client cannot name
// its own source; and the edge's address is never a key of its own, so every
// public client behind one edge draws its own bucket here as it does at
// the edge (the ninth development chain, 12 October 2026: the mesh host
// forwarding the public filter, the feed and the faucet relay was one
// source to this guard, one 20-a-second bucket for every wallet and probe).
func (g *sourceGuard) sourceOf(remote, forwardedFor string) string {
	if g == nil || len(g.trusted) == 0 || forwardedFor == "" {
		return remote
	}
	if !g.trusted[sourceKey(remote)] {
		return remote
	}
	last := forwardedFor
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	last = strings.TrimSpace(last)
	if last == "" {
		return remote
	}
	return last
}

// sourceKey is the host of a remote address (the port dropped, so one host's
// connections share a bucket); an address with no host (IPC) is "".
func sourceKey(remote string) string {
	if remote == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(remote); err == nil {
		return h
	}
	return remote
}

// errRateLimited and errNotPublic are the guard's two refusals, as JSON-RPC
// errors (the codes are in the server-error range).
type rateLimitedError struct{}

func (e *rateLimitedError) ErrorCode() int { return -32005 }
func (e *rateLimitedError) Error() string {
	return "rate limited: this source exceeded the public listener's rate"
}

type notPublicError struct{ method string }

func (e *notPublicError) ErrorCode() int { return -32004 }
func (e *notPublicError) Error() string {
	return "the method " + e.method + " is not served on the public listener"
}

// check admits or refuses one call from remote: the deny set first (a denied
// method costs no token), then the source's bucket.
func (g *sourceGuard) check(remote, method string) error {
	if g == nil {
		return nil
	}
	if g.deny[method] {
		return &notPublicError{method}
	}
	if i := strings.IndexByte(method, '_'); i > 0 && g.denyN[method[:i]] {
		return &notPublicError{method}
	}
	key := sourceKey(remote)
	if key == "" || g.rate <= 0 || g.exempt(key) {
		return nil
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Sub(g.lastGC) > sourceGuardGCEvery || len(g.buckets) > sourceGuardMaxKeys {
		for k, b := range g.buckets {
			if now.Sub(b.last) > sourceGuardIdle {
				delete(g.buckets, k)
			}
		}
		if len(g.buckets) > sourceGuardMaxKeys {
			// still over after the sweep: start over rather than grow
			g.buckets = make(map[string]*bucket)
		}
		g.lastGC = now
	}
	b := g.buckets[key]
	if b == nil {
		b = &bucket{tokens: g.burst, last: now}
		g.buckets[key] = b
	} else {
		b.tokens += now.Sub(b.last).Seconds() * g.rate
		if b.tokens > g.burst {
			b.tokens = g.burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return &rateLimitedError{}
	}
	b.tokens--
	return nil
}

// noteRefusal counts a refusal for the source and says whether this one is
// to be logged: the first of a source, then one a minute, with the count
// since the last line (returned) and the last refused method.
func (g *sourceGuard) noteRefusal(remote, method string) (report bool, count uint64) {
	if g == nil {
		return true, 1
	}
	key := sourceKey(remote)
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refusals == nil {
		g.refusals = make(map[string]*refusalCount)
	}
	r := g.refusals[key]
	if r == nil {
		if len(g.refusals) > sourceGuardMaxKeys {
			g.refusals = make(map[string]*refusalCount)
		}
		g.refusals[key] = &refusalCount{reportAt: now, method: method}
		return true, 1
	}
	r.n++
	r.method = method
	if now.Sub(r.reportAt) < sourceGuardReportEvery {
		return false, r.n
	}
	count = r.n
	r.n = 0
	r.reportAt = now
	return true, count
}

// SetSourceGuard makes this server a PUBLIC listener's: every call is
// admitted through a per-source token bucket (rate a second, burst) and the
// denied methods and namespaces are refused. A rate of zero disables the
// bucket; an empty deny set denies nothing; the trusted edges (hosts) are
// the peers whose forwarded client address is the source (sourceOf).
func (s *Server) SetSourceGuard(rate, burst float64, denyMethods, denyNamespaces, trustedEdges, exemptSources []string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.guard = newSourceGuard(rate, burst, denyMethods, denyNamespaces, trustedEdges, exemptSources)
}
