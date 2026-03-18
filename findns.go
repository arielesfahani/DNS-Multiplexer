package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// findnsResult mirrors the JSON structure from findns scan output.
type findnsResult struct {
	Steps  []findnsStep  `json:"steps"`
	Passed []findnsEntry `json:"passed"`
	Failed []findnsEntry `json:"failed"`
}

type findnsStep struct {
	Name         string  `json:"name"`
	Tested       int     `json:"tested"`
	Passed       int     `json:"passed"`
	Failed       int     `json:"failed"`
	DurationSecs float64 `json:"duration_secs"`
}

type findnsEntry struct {
	IP      string        `json:"ip"`
	URL     string        `json:"url,omitempty"` // DoH mode
	Metrics findnsMetrics `json:"metrics,omitempty"`
}

type findnsMetrics struct {
	PingMs    float64 `json:"ping_ms,omitempty"`
	ResolveMs float64 `json:"resolve_ms,omitempty"`
	EDNSMax   int     `json:"edns_max,omitempty"`
	E2EMs     float64 `json:"e2e_ms,omitempty"`
}

// FindNSScanner shells out to the findns binary for resolver scanning.
type FindNSScanner struct {
	Binary  string // path to findns binary
	Domain  string // tunnel domain (e.g. t.example.com)
	Pubkey  string // server public key hex (for e2e testing)
	DoH     bool   // use --doh mode
	Workers int    // concurrency (low for e2e: 5-10)
	TopN    int    // --top N results
}

// Scan runs findns scan and returns verified resolvers sorted by performance.
// If inputFile is empty, findns uses its bundled 7800+ Iranian resolvers.
func (f *FindNSScanner) Scan(inputFile string) ([]Resolver, error) {
	if f.Binary == "" {
		return nil, fmt.Errorf("findns binary path is empty")
	}
	if f.Domain == "" {
		return nil, fmt.Errorf("findns scan requires a domain")
	}

	// Create temp output file
	tmpDir, err := os.MkdirTemp("", "dns-mux-findns-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir for findns output: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	outputFile := filepath.Join(tmpDir, "results.json")

	args := []string{"scan", "--domain", f.Domain, "-o", outputFile}

	if inputFile != "" {
		args = append(args, "-i", inputFile)
	}
	if f.Pubkey != "" {
		args = append(args, "--pubkey", f.Pubkey)
	}
	if f.DoH {
		args = append(args, "--doh")
	}
	if f.Workers > 0 {
		args = append(args, "--workers", fmt.Sprintf("%d", f.Workers))
	}
	if f.TopN > 0 {
		args = append(args, "--top", fmt.Sprintf("%d", f.TopN))
	}

	slog.Info("findns: starting scan",
		"binary", f.Binary,
		"domain", f.Domain,
		"doh", f.DoH,
		"workers", f.Workers,
		"input", inputFile,
	)

	cmd := exec.Command(f.Binary, args...)
	cmd.Stdout = os.Stderr // show findns progress on stderr
	cmd.Stderr = os.Stderr

	start := time.Now()
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("findns scan failed: %w", err)
	}
	elapsed := time.Since(start)

	// Parse output
	data, err := os.ReadFile(outputFile)
	if err != nil {
		return nil, fmt.Errorf("reading findns output: %w", err)
	}

	var result findnsResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parsing findns output: %w", err)
	}

	// Convert to Resolvers
	var resolvers []Resolver
	for _, entry := range result.Passed {
		if f.DoH && entry.URL != "" {
			resolvers = append(resolvers, Resolver{URL: entry.URL})
		} else if entry.IP != "" {
			resolvers = append(resolvers, Resolver{Addr: entry.IP + ":53"})
		}
	}

	// Log step summary
	for _, step := range result.Steps {
		slog.Info("findns step",
			"name", step.Name,
			"tested", step.Tested,
			"passed", step.Passed,
			"failed", step.Failed,
			"duration", fmt.Sprintf("%.1fs", step.DurationSecs),
		)
	}

	slog.Info("findns: scan complete",
		"elapsed", elapsed.Round(time.Second),
		"passed", len(resolvers),
		"total_tested", len(result.Passed)+len(result.Failed),
	)

	return resolvers, nil
}

// ExportLocalResolvers uses `findns local` to export bundled Iranian resolvers
// to a temp file and returns the path. Caller should defer os.Remove(path).
func (f *FindNSScanner) ExportLocalResolvers() (string, error) {
	tmpFile, err := os.CreateTemp("", "dns-mux-local-resolvers-*.txt")
	if err != nil {
		return "", err
	}
	tmpFile.Close()

	cmd := exec.Command(f.Binary, "local", "-o", tmpFile.Name())
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("findns local failed: %w", err)
	}

	slog.Info("findns: exported local resolvers", "path", tmpFile.Name())
	return tmpFile.Name(), nil
}

// IsAvailable checks if the findns binary exists and is executable.
func (f *FindNSScanner) IsAvailable() bool {
	if f.Binary == "" {
		return false
	}
	_, err := exec.LookPath(f.Binary)
	return err == nil
}

// ParseFindNSOutput reads a findns JSON results file and returns the passed resolvers.
func ParseFindNSOutput(path string, doh bool) ([]Resolver, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var result findnsResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parsing findns JSON: %w", err)
	}

	var resolvers []Resolver
	for _, entry := range result.Passed {
		if doh && entry.URL != "" {
			resolvers = append(resolvers, Resolver{URL: entry.URL})
		} else if entry.IP != "" {
			addr := entry.IP + ":53"
			if strings.Contains(entry.IP, ":") {
				addr = entry.IP // already has port
			}
			resolvers = append(resolvers, Resolver{Addr: addr})
		}
	}

	return resolvers, nil
}
