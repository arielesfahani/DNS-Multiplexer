package main

import (
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// AutoScanner periodically scans resolvers using findns (preferred) or the
// built-in connectivity scanner (fallback) and keeps the resolver pool updated.
//
// On startup it signals readiness once enough resolvers are found.
// When a resolver goes down, TriggerRescan() can be called to immediately
// search for a replacement.
type AutoScanner struct {
	pool         *ResolverPool
	allResolvers []Resolver // full list for fallback scanning
	scanDomain   string
	doh          bool
	pubkey       []byte // server public key for HMAC verify (fallback)
	pubkeyHex    string // hex-encoded pubkey for findns
	interval     time.Duration
	topN         int // target count of verified resolvers to keep
	maxSteps     int // max resolvers to test per scan round (0 = all, fallback only)
	workers      int // scan concurrency
	stopCh       chan struct{}
	rescanCh     chan struct{} // trigger rescan when a resolver fails
	readyCh      chan struct{} // closed when first batch of resolvers is ready
	readyOnce    sync.Once

	// findns integration
	findns       *FindNSScanner
	resolverFile string // path to resolvers file for findns -i
}

func NewAutoScanner(pool *ResolverPool, allResolvers []Resolver, scanDomain string, doh bool,
	pubkey []byte, interval time.Duration, topN, maxSteps, workers int) *AutoScanner {

	pubkeyHex := ""
	if len(pubkey) > 0 {
		pubkeyHex = fmt.Sprintf("%x", pubkey)
	}

	return &AutoScanner{
		pool:         pool,
		allResolvers: allResolvers,
		scanDomain:   scanDomain,
		doh:          doh,
		pubkey:       pubkey,
		pubkeyHex:    pubkeyHex,
		interval:     interval,
		topN:         topN,
		maxSteps:     maxSteps,
		workers:      workers,
		stopCh:       make(chan struct{}),
		rescanCh:     make(chan struct{}, 1),
		readyCh:      make(chan struct{}),
	}
}

// SetFindNS configures the findns scanner for use instead of the built-in scanner.
func (as *AutoScanner) SetFindNS(scanner *FindNSScanner, resolverFile string) {
	as.findns = scanner
	as.resolverFile = resolverFile
}

// Start launches the initial scan and periodic rescanning in the background.
func (as *AutoScanner) Start() {
	mode := "built-in"
	if as.findns != nil && as.findns.IsAvailable() {
		mode = "findns"
	}
	slog.Info("Auto-scanner: starting",
		"mode", mode,
		"resolvers", len(as.allResolvers),
		"domain", as.scanDomain,
		"workers", as.workers,
		"top_n", as.topN,
	)
	go as.run()
}

// WaitReady blocks until the initial scan has found enough resolvers.
func (as *AutoScanner) WaitReady() {
	<-as.readyCh
}

// TriggerRescan requests a background rescan to find replacement resolvers.
func (as *AutoScanner) TriggerRescan() {
	select {
	case as.rescanCh <- struct{}{}:
		slog.Info("Auto-scanner: rescan triggered by resolver failure")
	default:
	}
}

func (as *AutoScanner) Stop() {
	close(as.stopCh)
}

func (as *AutoScanner) run() {
	as.initialScan()
	as.loop()
}

func (as *AutoScanner) loop() {
	ticker := time.NewTicker(as.interval)
	defer ticker.Stop()

	// Maintenance ticker: check pool health more frequently
	maintainTicker := time.NewTicker(10 * time.Second)
	defer maintainTicker.Stop()

	for {
		select {
		case <-ticker.C:
			slog.Info("Auto-scanner: scheduled periodic scan")
			as.scanAndUpdate()
		case <-as.rescanCh:
			slog.Info("Auto-scanner: emergency rescan triggered")
			as.scanAndUpdate()
		case <-maintainTicker.C:
			// If pool is thinning out (less than half of target), trigger a scan
			healthy := as.pool.HealthyCount()
			if as.topN > 0 && healthy < (as.topN/2) && healthy < len(as.allResolvers) {
				slog.Warn("Auto-scanner: pool is thinning, triggering preemptive scan", "healthy", healthy, "target", as.topN)
				as.TriggerRescan()
			}
		case <-as.stopCh:
			return
		}
	}
}

// initialScan runs the first scan and signals readyCh when resolvers are found.
func (as *AutoScanner) initialScan() {
	start := time.Now()

	// Try findns first
	if as.findns != nil && as.findns.IsAvailable() {
		slog.Info("Auto-scan: initial scan using findns", "domain", as.scanDomain)

		resolvers, err := as.findns.Scan(as.resolverFile)
		if err != nil {
			slog.Warn("findns initial scan failed, falling back to built-in scanner", "err", err)
		} else if len(resolvers) > 0 {
			as.pool.UpdateResolvers(resolvers)
			slog.Info("Auto-scan: findns initial scan complete",
				"elapsed", time.Since(start).Round(time.Second),
				"resolvers", len(resolvers),
			)
			as.readyOnce.Do(func() { close(as.readyCh) })
			return
		} else {
			slog.Warn("findns initial scan (primary list) found 0 verified resolvers. Triggering Deep Global Scan...")
			// Trigger deep scan of the full internal list (7800+ resolvers)
			deepResolvers, deepErr := as.findns.Scan("")
			if deepErr == nil && len(deepResolvers) > 0 {
				as.pool.UpdateResolvers(deepResolvers)
				slog.Info("Auto-scan: findns DEEP GLOBAL scan complete",
					"elapsed", time.Since(start).Round(time.Second),
					"resolvers", len(deepResolvers),
				)
				as.readyOnce.Do(func() { close(as.readyCh) })
				return
			}
			slog.Warn("findns Deep Global Scan failed or found no resolvers, falling back to built-in scanner")
		}
	}

	// Fallback: built-in verify scanner
	as.initialScanBuiltin()
}

// scanAndUpdate runs a periodic rescan round.
func (as *AutoScanner) scanAndUpdate() {
	start := time.Now()

	// Try findns first
	if as.findns != nil && as.findns.IsAvailable() {
		resolvers, err := as.findns.Scan(as.resolverFile)
		if err != nil {
			slog.Warn("findns periodic scan failed, falling back to built-in", "err", err)
		} else if len(resolvers) > 0 {
			as.pool.UpdateResolvers(resolvers)
			slog.Info("Auto-scan: findns periodic scan complete",
				"elapsed", time.Since(start).Round(time.Second),
				"resolvers", len(resolvers),
			)
			return
		}
	}

	// Fallback: built-in scanner
	as.scanAndUpdateBuiltin()
}

// ─── Built-in scanner fallback (kept from original) ──────────────────────────

// shuffledResolvers returns a shuffled copy of allResolvers, capped at maxSteps.
func (as *AutoScanner) shuffledResolvers() []Resolver {
	shuffled := make([]Resolver, len(as.allResolvers))
	copy(shuffled, as.allResolvers)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	if as.maxSteps > 0 && as.maxSteps < len(shuffled) {
		shuffled = shuffled[:as.maxSteps]
	}
	return shuffled
}

func (as *AutoScanner) initialScanBuiltin() {
	shuffled := as.shuffledResolvers()
	start := time.Now()
	slog.Info("Auto-scan: initial verify scan (built-in)", "testing", len(shuffled), "workers", as.workers)

	results := verifyResolversWithEarlyReady(shuffled, as.scanDomain, as.doh, as.workers,
		as.topN, as.pubkey, func(ready []Resolver) {
			as.pool.UpdateResolvers(ready)
			slog.Info("Auto-scan: reached target, services can start", "verified", len(ready))
			as.readyOnce.Do(func() { close(as.readyCh) })
		})

	elapsed := time.Since(start)
	as.processResults(results, elapsed)

	as.readyOnce.Do(func() {
		slog.Warn("Auto-scan: initial scan complete without reaching target, starting with available resolvers")
		close(as.readyCh)
	})
}

func (as *AutoScanner) scanAndUpdateBuiltin() {
	shuffled := as.shuffledResolvers()
	start := time.Now()
	slog.Info("Auto-scan starting (built-in)", "testing", len(shuffled), "workers", as.workers)

	results := verifyResolversQuiet(shuffled, as.scanDomain, as.doh, as.workers, as.pubkey)
	elapsed := time.Since(start)

	as.processResults(results, elapsed)
}

// processResults sorts results, selects the best verified resolvers, and updates the pool.
func (as *AutoScanner) processResults(results []VerifyResult, elapsed time.Duration) {
	// Sort: verified first, then by latency ascending
	sortVerifyResults(results)

	// Collect verified resolvers, capped at topN
	var qualified []Resolver
	for _, r := range results {
		if r.Verified {
			qualified = append(qualified, r.Resolver)
			if as.topN > 0 && len(qualified) >= as.topN {
				break
			}
		}
	}

	// Fallback: if no verified resolvers, take best working ones
	if len(qualified) == 0 {
		for _, r := range results {
			if r.Status == "WORKING" {
				qualified = append(qualified, r.Resolver)
				if as.topN > 0 && len(qualified) >= as.topN {
					break
				}
			}
		}
	}

	if len(qualified) == 0 {
		slog.Warn("Auto-scan: no working resolvers found, keeping current pool")
		return
	}

	as.pool.UpdateResolvers(qualified)

	// Count statuses
	var working, verified, timeouts, errors int
	for _, r := range results {
		switch r.Status {
		case "WORKING":
			working++
			if r.Verified {
				verified++
			}
		case "TIMEOUT":
			timeouts++
		default:
			errors++
		}
	}

	// Log the top resolvers
	limit := len(qualified)
	if limit > 10 {
		limit = 10
	}
	var topList []string
	for i := 0; i < limit && i < len(results); i++ {
		r := results[i]
		if r.Verified {
			topList = append(topList, fmt.Sprintf("%s(%dms)", r.Resolver.String(), r.LatencyMs))
		}
	}

	slog.Info("Auto-scan complete",
		"elapsed", elapsed.Round(time.Second),
		"working", working,
		"verified", verified,
		"timeout", timeouts,
		"error", errors,
		"selected", len(qualified),
	)
	if len(topList) > 0 {
		slog.Info("Top verified resolvers", "list", strings.Join(topList, ", "))
	}
}

// sortVerifyResults sorts by verified-first, then latency ascending.
func sortVerifyResults(results []VerifyResult) {
	for i := 1; i < len(results); i++ {
		for j := i; j > 0; j-- {
			swap := false
			if results[j].Verified && !results[j-1].Verified {
				swap = true
			} else if results[j].Verified == results[j-1].Verified && results[j].LatencyMs < results[j-1].LatencyMs {
				swap = true
			}
			if swap {
				results[j], results[j-1] = results[j-1], results[j]
			}
		}
	}
}
