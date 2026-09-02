// Package selftest runs isolated local checks included in the Engram binary.
package selftest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

const (
	SuiteAll         = "all"
	SuiteReliability = "reliability"
	SuitePerformance = "performance"
)

// Options controls an isolated self-test run.
type Options struct {
	Suite string
	Quick bool
}

// Report is the stable top-level JSON shape emitted by `engram test --json`.
type Report struct {
	SchemaVersion string     `json:"schema_version"`
	Suite         string     `json:"suite"`
	Quick         bool       `json:"quick"`
	Passed        bool       `json:"passed"`
	DurationMS    int64      `json:"duration_ms"`
	Scenarios     []Scenario `json:"scenarios"`
}

// Scenario records one self-test result. Performance scenarios include their
// operation count and throughput in Metrics; no timing threshold determines pass or fail.
type Scenario struct {
	Name       string             `json:"name"`
	Suite      string             `json:"suite"`
	Passed     bool               `json:"passed"`
	DurationMS int64              `json:"duration_ms"`
	Error      string             `json:"error,omitempty"`
	Metrics    map[string]float64 `json:"metrics,omitempty"`
}

var (
	newTempDir = os.MkdirTemp
	removeAll  = os.RemoveAll
)

// Run executes the selected local-only self-test suite. Every run owns a new
// temporary directory and never reads the configured Engram data directory.
func Run(options Options) Report {
	suite := normalizeSuite(options.Suite)
	started := time.Now()
	report := Report{
		SchemaVersion: "engram-self-test/v1",
		Suite:         suite,
		Quick:         options.Quick,
		Passed:        true,
		Scenarios:     make([]Scenario, 0, 3),
	}

	root, err := newTempDir("", "engram-self-test-*")
	if err != nil {
		report.Passed = false
		report.Scenarios = append(report.Scenarios, failedScenario("setup", suite, started, fmt.Errorf("create temporary directory: %w", err)))
		report.DurationMS = durationMS(time.Since(started))
		return report
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		_ = removeAll(root)
		report.Passed = false
		report.Scenarios = append(report.Scenarios, failedScenario("setup", suite, started, fmt.Errorf("resolve temporary directory: %w", err)))
		report.DurationMS = durationMS(time.Since(started))
		return report
	}
	root = absoluteRoot
	defer func() { _ = removeAll(root) }()

	if suite == SuiteAll || suite == SuiteReliability {
		report.add(runScenario("database_save_search_context", SuiteReliability, func() error {
			return runDatabaseSaveSearchContext(filepath.Join(root, "reliability-core"))
		}))
		report.add(runScenario("concurrent_local_writes", SuiteReliability, func() error {
			return runConcurrentLocalWrites(filepath.Join(root, "reliability-concurrent"), options.Quick)
		}))
	}
	if suite == SuiteAll || suite == SuitePerformance {
		report.add(runPerformanceScenario("store_search", func() (map[string]float64, error) {
			return runStoreSearch(filepath.Join(root, "performance-search"), options.Quick)
		}))
	}

	report.DurationMS = durationMS(time.Since(started))
	return report
}

func (r *Report) add(scenario Scenario) {
	r.Scenarios = append(r.Scenarios, scenario)
	if !scenario.Passed {
		r.Passed = false
	}
}

func normalizeSuite(suite string) string {
	switch strings.ToLower(strings.TrimSpace(suite)) {
	case "", SuiteAll:
		return SuiteAll
	case SuiteReliability:
		return SuiteReliability
	case SuitePerformance:
		return SuitePerformance
	default:
		return strings.ToLower(strings.TrimSpace(suite))
	}
}

func runScenario(name, suite string, fn func() error) Scenario {
	started := time.Now()
	if err := fn(); err != nil {
		return failedScenario(name, suite, started, err)
	}
	return Scenario{Name: name, Suite: suite, Passed: true, DurationMS: durationMS(time.Since(started))}
}

func failedScenario(name, suite string, started time.Time, err error) Scenario {
	return Scenario{Name: name, Suite: suite, DurationMS: durationMS(time.Since(started)), Error: err.Error()}
}

func runPerformanceScenario(name string, fn func() (map[string]float64, error)) Scenario {
	started := time.Now()
	metrics, err := fn()
	if err != nil {
		return failedScenario(name, SuitePerformance, started, err)
	}
	return Scenario{
		Name:       name,
		Suite:      SuitePerformance,
		Passed:     true,
		DurationMS: durationMS(time.Since(started)),
		Metrics:    metrics,
	}
}

func runDatabaseSaveSearchContext(dataDir string) error {
	s, err := openStore(dataDir)
	if err != nil {
		return err
	}
	defer s.Close()

	const sessionID = "selftest-reliability-session"
	const project = "selftest-reliability"
	if err := s.CreateSession(sessionID, project, dataDir); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: sessionID,
		Type:      "decision",
		Title:     "Self-test database migration",
		Content:   "Self-test verifies local database initialization, save, search, and context.",
		Project:   project,
		Scope:     "project",
	}); err != nil {
		return fmt.Errorf("save observation: %w", err)
	}

	results, err := s.Search("database migration", store.SearchOptions{Project: project, Scope: "project", Limit: 5})
	if err != nil {
		return fmt.Errorf("search observation: %w", err)
	}
	if len(results) == 0 || results[0].Title != "Self-test database migration" {
		return fmt.Errorf("search did not return the saved observation")
	}

	context, err := s.FormatContext(project, "project")
	if err != nil {
		return fmt.Errorf("format context: %w", err)
	}
	if !strings.Contains(context, "Self-test database migration") {
		return fmt.Errorf("context did not include the saved observation")
	}
	return nil
}

func runConcurrentLocalWrites(dataDir string, quick bool) error {
	s, err := openStore(dataDir)
	if err != nil {
		return err
	}
	defer s.Close()

	workers, writesPerWorker := 8, 50
	if quick {
		workers, writesPerWorker = 4, 10
	}
	const project = "selftest-concurrent"
	for worker := 0; worker < workers; worker++ {
		if err := s.CreateSession(fmt.Sprintf("selftest-concurrent-%d", worker), project, dataDir); err != nil {
			return fmt.Errorf("create session %d: %w", worker, err)
		}
	}

	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for write := 0; write < writesPerWorker; write++ {
				_, err := s.AddObservation(store.AddObservationParams{
					SessionID: fmt.Sprintf("selftest-concurrent-%d", worker),
					Type:      "note",
					Title:     fmt.Sprintf("Concurrent local write %d-%d", worker, write),
					Content:   fmt.Sprintf("Self-test concurrent local write %d-%d", worker, write),
					Project:   project,
					Scope:     "project",
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return fmt.Errorf("concurrent write: %w", err)
		}
	}

	stats, err := s.Stats()
	if err != nil {
		return fmt.Errorf("read stats: %w", err)
	}
	want := workers * writesPerWorker
	if stats.TotalObservations != want {
		return fmt.Errorf("stored %d concurrent observations, want %d", stats.TotalObservations, want)
	}
	return nil
}

func runStoreSearch(dataDir string, quick bool) (map[string]float64, error) {
	s, err := openStore(dataDir)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	records, searches := 1_000, 200
	if quick {
		records, searches = 100, 20
	}
	const sessionID = "selftest-performance-session"
	const project = "selftest-performance"
	if err := s.CreateSession(sessionID, project, dataDir); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	for i := 0; i < records; i++ {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: sessionID,
			Type:      "note",
			Title:     fmt.Sprintf("Self-test search record %04d", i),
			Content:   fmt.Sprintf("Self-test search corpus shared keyword record %04d", i),
			Project:   project,
			Scope:     "project",
		}); err != nil {
			return nil, fmt.Errorf("seed record %d: %w", i, err)
		}
	}

	started := time.Now()
	for i := 0; i < searches; i++ {
		results, err := s.Search("shared keyword", store.SearchOptions{Project: project, Scope: "project", Limit: 20})
		if err != nil {
			return nil, fmt.Errorf("search %d: %w", i, err)
		}
		if len(results) == 0 {
			return nil, fmt.Errorf("search %d returned no seeded records", i)
		}
	}
	duration := time.Since(started)
	throughput := 0.0
	if duration > 0 {
		throughput = float64(searches) / duration.Seconds()
	}
	return map[string]float64{
		"operations":                float64(searches),
		"throughput_ops_per_second": throughput,
	}, nil
}

func openStore(dataDir string) (*store.Store, error) {
	absoluteDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve temporary data directory: %w", err)
	}
	return store.New(store.FallbackConfig(absoluteDir))
}

func durationMS(duration time.Duration) int64 {
	return duration.Milliseconds()
}
