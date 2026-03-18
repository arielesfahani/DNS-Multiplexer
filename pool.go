package main

import (
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

// Resolver represents an upstream DNS resolver.
type Resolver struct {
	Addr string // "IP:port" for UDP mode
	URL  string // full HTTPS URL for DoH mode
}

func (r Resolver) String() string {
	if r.URL != "" {
		return r.URL
	}
	return r.Addr
}

type resolverStats struct {
	sent    uint64
	ok      uint64
	fail    uint64
	latency int64 // average latency in nanoseconds
}

// ResolverPool manages a set of upstream resolvers with health tracking.
type ResolverPool struct {
	resolvers      []Resolver
	mode           string // "round-robin" or "random"
	doh            bool
	mu             sync.RWMutex
	healthy        map[Resolver]bool
	healthyCache   []Resolver
	stats          map[Resolver]*resolverStats
	failStreak     map[Resolver]int
	rrIndex        uint64
	onResolverDown func() // called (in a goroutine) when a resolver is marked unhealthy
	healthDomain   string // domain to use for health checks (e.g. t.example.com)
}

func NewResolverPool(resolvers []Resolver, mode string, doh bool) *ResolverPool {
	p := &ResolverPool{
		resolvers:    resolvers,
		mode:         mode,
		doh:          doh,
		healthy:      make(map[Resolver]bool, len(resolvers)),
		healthyCache: make([]Resolver, len(resolvers)),
		stats:        make(map[Resolver]*resolverStats, len(resolvers)),
		failStreak:   make(map[Resolver]int, len(resolvers)),
		healthDomain: "google.com", // default
	}
	copy(p.healthyCache, resolvers)
	for _, r := range resolvers {
		p.healthy[r] = true
		p.stats[r] = &resolverStats{}
	}
	return p
}

func (p *ResolverPool) SetHealthDomain(domain string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if domain != "" {
		p.healthDomain = domain
	}
}

func (p *ResolverPool) rebuildHealthyCache() {
	cache := make([]Resolver, 0, len(p.resolvers))
	for _, r := range p.resolvers {
		if p.healthy[r] {
			cache = append(cache, r)
		}
	}
	if len(cache) == 0 {
		cache = make([]Resolver, len(p.resolvers))
		copy(cache, p.resolvers)
	}
	p.healthyCache = cache
}

func (p *ResolverPool) GetNext() Resolver {
	p.mu.RLock()
	healthy := p.healthyCache
	p.mu.RUnlock()

	if len(healthy) == 0 {
		return p.resolvers[rand.Intn(len(p.resolvers))]
	}

	// Priority-based selection (Latency-aware)
	// We pick 3 random healthy ones and select the one with the best stats.
	// This "Power of Two Choices" variation is more robust than strict sorting.
	candidates := 3
	if len(healthy) < candidates {
		candidates = len(healthy)
	}

	var best Resolver
	var bestScore float64 = -1

	for i := 0; i < candidates; i++ {
		r := healthy[rand.Intn(len(healthy))]
		p.mu.RLock()
		s := p.stats[r]
		p.mu.RUnlock()

		ok := atomic.LoadUint64(&s.ok)
		sent := atomic.LoadUint64(&s.sent)
		lat := atomic.LoadInt64(&s.latency)

		// Calculate a score: (Success Rate) / (Log(Latency))
		// Lower latency and higher success rate = higher score.
		successRate := 1.0
		if sent > 0 {
			successRate = float64(ok) / float64(sent)
		}

		latencyMs := float64(lat) / 1e6
		if latencyMs < 1 {
			latencyMs = 1
		}

		score := successRate / (latencyMs / 100.0) // Normalize latency for scoring
		if score > bestScore {
			bestScore = score
			best = r
		}
	}

	if bestScore > -1 {
		return best
	}

	if p.mode == "random" {
		return healthy[rand.Intn(len(healthy))]
	}
	idx := atomic.AddUint64(&p.rrIndex, 1) - 1
	return healthy[idx%uint64(len(healthy))]
}

// MarkSuccess records a successful query and its latency.
func (p *ResolverPool) MarkSuccessWithLatency(r Resolver, latency time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.stats[r]
	if !ok {
		return
	}
	atomic.AddUint64(&s.ok, 1)
	p.failStreak[r] = 0

	// Use an exponentially weighted moving average for latency
	oldLat := atomic.LoadInt64(&s.latency)
	newLat := latency.Nanoseconds()
	if oldLat == 0 {
		atomic.StoreInt64(&s.latency, newLat)
	} else {
		// Weight towards new: 0.2 * new + 0.8 * old
		atomic.StoreInt64(&s.latency, (newLat*2+oldLat*8)/10)
	}

	if !p.healthy[r] {
		p.healthy[r] = true
		p.rebuildHealthyCache()
	}
}

// SendQuery sends a DNS query to a resolver using the appropriate transport.
func (p *ResolverPool) SendQuery(data []byte, r Resolver) ([]byte, error) {
	if p.doh {
		return sendQueryDoH(data, r.URL, upstreamTimeout)
	}
	return sendQueryUDP(data, r.Addr, upstreamTimeout)
}

func (p *ResolverPool) MarkSent(r Resolver) {
	p.mu.RLock()
	s := p.stats[r]
	p.mu.RUnlock()
	atomic.AddUint64(&s.sent, 1)
}

func (p *ResolverPool) MarkSuccess(r Resolver) {
	p.MarkSuccessWithLatency(r, 100*time.Millisecond) // fallback latency
}

func (p *ResolverPool) MarkFailure(r Resolver) {
	p.mu.Lock()
	s := p.stats[r]
	atomic.AddUint64(&s.fail, 1)
	p.failStreak[r]++
	// Generous threshold: during internet shutdowns DNS servers fail temporarily
	// but may come back, so we allow many consecutive failures before marking down.
	var cb func()
	if p.failStreak[r] >= 10 && p.healthy[r] {
		p.healthy[r] = false
		p.rebuildHealthyCache()
		cb = p.onResolverDown
		slog.Warn("Resolver marked unhealthy", "resolver", r, "streak", p.failStreak[r])
	}
	p.mu.Unlock()
	if cb != nil {
		go cb()
	}
}

// SetOnResolverDown registers a callback invoked (in a new goroutine) whenever
// a resolver is marked unhealthy. Used by AutoScanner to trigger rescanning.
func (p *ResolverPool) SetOnResolverDown(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onResolverDown = fn
}

func (p *ResolverPool) HealthCheck() {
	p.mu.RLock()
	domain := p.healthDomain
	p.mu.RUnlock()

	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(domain), dns.TypeA)
	msg.RecursionDesired = true
	query, err := msg.Pack()
	if err != nil {
		return
	}

	type result struct {
		r     Resolver
		alive bool
	}

	ch := make(chan result, len(p.resolvers))
	for _, r := range p.resolvers {
		go func(r Resolver) {
			_, err := p.SendQuery(query, r)
			ch <- result{r, err == nil}
		}(r)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for range p.resolvers {
		res := <-ch
		p.healthy[res.r] = res.alive
		if res.alive {
			p.failStreak[res.r] = 0
		}
	}
	p.rebuildHealthyCache()
}

func (p *ResolverPool) HealthyCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.healthyCache)
}

func (p *ResolverPool) StatsString() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var result string
	for _, r := range p.resolvers {
		s := p.stats[r]
		status := "UP"
		if !p.healthy[r] {
			status = "DOWN"
		}
		result += fmt.Sprintf("  %40s [%4s] sent=%-6d ok=%-6d fail=%d lat=%dms\n",
			r.String(), status,
			atomic.LoadUint64(&s.sent),
			atomic.LoadUint64(&s.ok),
			atomic.LoadUint64(&s.fail),
			atomic.LoadInt64(&s.latency)/1e6)
	}
	return result
}

// ProbeResolvers tests all resolvers and returns only working ones.
func ProbeResolvers(pool *ResolverPool) []Resolver {
	slog.Info("Probing resolvers...", "count", len(pool.resolvers))

	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn("google.com"), dns.TypeA)
	msg.RecursionDesired = true
	query, _ := msg.Pack()

	type result struct {
		r     Resolver
		alive bool
	}

	ch := make(chan result, len(pool.resolvers))
	workers := len(pool.resolvers)
	if workers > 30 {
		workers = 30
	}
	sem := make(chan struct{}, workers)

	for _, r := range pool.resolvers {
		sem <- struct{}{}
		go func(r Resolver) {
			defer func() { <-sem }()
			_, err := pool.SendQuery(query, r)
			ch <- result{r, err == nil}
		}(r)
	}

	var working []Resolver
	for range pool.resolvers {
		res := <-ch
		if res.alive {
			slog.Info(fmt.Sprintf("  \033[32mUP\033[0m   %s", res.r))
			working = append(working, res.r)
		} else {
			slog.Info(fmt.Sprintf("  \033[31mDOWN\033[0m %s", res.r))
		}
	}

	if len(working) == 0 {
		slog.Warn("No working resolvers found! Keeping all.")
		return pool.resolvers
	}

	slog.Info("Probe complete", "working", len(working), "total", len(pool.resolvers))
	return working
}

// UpdateResolvers replaces the active resolver list with the given ordered
// resolvers. Stats are preserved for existing resolvers and initialized for
// new ones. Used by AutoScanner to prioritize the best resolvers.
func (p *ResolverPool) UpdateResolvers(ordered []Resolver) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.resolvers = ordered

	for _, r := range ordered {
		if _, ok := p.stats[r]; !ok {
			p.stats[r] = &resolverStats{}
			p.failStreak[r] = 0
		}
		if _, ok := p.healthy[r]; !ok {
			p.healthy[r] = true
		}
	}

	p.rebuildHealthyCache()
	slog.Info("Resolver pool updated", "count", len(ordered))
}

// PoolWithTimeout returns a duration used for health-check & stats loops.
const (
	HealthCheckInterval = 30 * time.Second
	StatsInterval       = 60 * time.Second
)
