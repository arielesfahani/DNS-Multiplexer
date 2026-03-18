package main

import (
	"log/slog"
	"os"
	"sync"
	"time"
)

// ResolverReloader watches a resolvers file for changes and hot-reloads the pool.
// It polls the file's mod time at a configurable interval and reloads when changed.
type ResolverReloader struct {
	filePath    string
	pool        *ResolverPool
	doh         bool
	interval    time.Duration
	lastModTime time.Time
	stopCh      chan struct{}
	mu          sync.Mutex
}

// NewResolverReloader creates a new file watcher for the resolvers file.
func NewResolverReloader(filePath string, pool *ResolverPool, doh bool, interval time.Duration) *ResolverReloader {
	return &ResolverReloader{
		filePath: filePath,
		pool:     pool,
		doh:      doh,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start begins polling the resolvers file for changes in the background.
func (r *ResolverReloader) Start() {
	if r.filePath == "" {
		return
	}

	// Record initial mod time
	if info, err := os.Stat(r.filePath); err == nil {
		r.lastModTime = info.ModTime()
	}

	slog.Info("Resolver hot-reload enabled", "file", r.filePath, "interval", r.interval)
	go r.loop()
}

// Stop stops the file watcher.
func (r *ResolverReloader) Stop() {
	close(r.stopCh)
}

// Reload forces an immediate reload of the resolvers file.
func (r *ResolverReloader) Reload() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doReload()
}

func (r *ResolverReloader) loop() {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.checkAndReload()
		case <-r.stopCh:
			return
		}
	}
}

func (r *ResolverReloader) checkAndReload() {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, err := os.Stat(r.filePath)
	if err != nil {
		slog.Debug("Resolver reload: cannot stat file", "file", r.filePath, "err", err)
		return
	}

	if info.ModTime().Equal(r.lastModTime) {
		return
	}

	r.lastModTime = info.ModTime()
	r.doReload()
}

func (r *ResolverReloader) doReload() {
	parsed := parseResolvers(r.filePath, nil, r.doh)
	if len(parsed) == 0 {
		slog.Warn("Resolver reload: file produced no resolvers, keeping current pool", "file", r.filePath)
		return
	}

	r.pool.UpdateResolvers(parsed)
	slog.Info("Resolvers reloaded from file", "file", r.filePath, "count", len(parsed))
}
