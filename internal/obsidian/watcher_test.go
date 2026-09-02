package obsidian

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── Fake Exportable ─────────────────────────────────────────────────────────

// fakeExporter is a test-only Exportable implementation.
// It counts calls, records GraphConfig values, and can inject errors on specific cycles.
type fakeExporter struct {
	mu           sync.Mutex
	callCount    int
	calls        chan int
	graphConfigs []GraphConfigMode
	errOnCycle   map[int]error // 1-indexed: return this error on the Nth call
	result       *ExportResult // returned on success (defaults to zero ExportResult)
	config       ExportConfig  // mutable config for SetGraphConfig access
}

func newFakeExporter() *fakeExporter {
	return &fakeExporter{
		errOnCycle: make(map[int]error),
		result:     &ExportResult{},
	}
}

func (f *fakeExporter) Export() (*ExportResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	if f.calls != nil {
		select {
		case f.calls <- f.callCount:
		default:
		}
	}
	f.graphConfigs = append(f.graphConfigs, f.config.GraphConfig)
	if err, ok := f.errOnCycle[f.callCount]; ok {
		return nil, err
	}
	r := *f.result
	return &r, nil
}

func (f *fakeExporter) SetGraphConfig(mode GraphConfigMode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.config.GraphConfig = mode
}

func (f *fakeExporter) GraphConfig() GraphConfigMode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.config.GraphConfig
}

func (f *fakeExporter) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

func (f *fakeExporter) GraphConfigs() []GraphConfigMode {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]GraphConfigMode, len(f.graphConfigs))
	copy(cp, f.graphConfigs)
	return cp
}

// ─── Task 3.1: TestWatcherRunsImmediatelyThenTicks ────────────────────────────

// TestWatcherRunsImmediatelyThenTicks verifies REQ-WATCH-03:
// - first cycle fires immediately (before first tick)
// - subsequent ticker events produce subsequent cycles
func TestWatcherRunsImmediatelyThenTicks(t *testing.T) {
	fe := newFakeExporter()
	fe.config.GraphConfig = GraphConfigForce

	w := NewWatcher(WatcherConfig{
		Exporter: fe,
		Interval: time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := w.Run(ctx)
	if err != context.Canceled {
		t.Errorf("Run() returned %v, want context.Canceled", err)
	}
	if count := fe.Count(); count != 1 {
		t.Fatalf("expected one immediate cycle before cancellation, got %d", count)
	}

	fe = newFakeExporter()
	fe.calls = make(chan int, 2)
	w = NewWatcher(WatcherConfig{
		Exporter: fe,
		Interval: time.Millisecond,
	})

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- w.Run(ctx)
	}()

	cycleTimeout := time.NewTimer(time.Second)
	defer cycleTimeout.Stop()
	for want := 1; want <= 2; want++ {
		select {
		case got := <-fe.calls:
			if got != want {
				t.Fatalf("cycle order: got %d, want %d", got, want)
			}
		case <-cycleTimeout.C:
			t.Fatalf("timed out waiting for cycle %d", want)
		}
	}

	cancel()
	shutdownTimeout := time.NewTimer(time.Second)
	defer shutdownTimeout.Stop()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Run() returned %v, want context.Canceled", err)
		}
	case <-shutdownTimeout.C:
		t.Fatal("Run() did not return after cancellation")
	}
}

// ─── Task 3.3: TestWatcherLogsCycleResults ────────────────────────────────────

// TestWatcherLogsCycleResults verifies REQ-WATCH-04:
// each cycle must produce a log line with created=, updated=, deleted=, skipped=, hubs=
func TestWatcherLogsCycleResults(t *testing.T) {
	fe := newFakeExporter()
	fe.result = &ExportResult{
		Created:     5,
		Updated:     2,
		Deleted:     1,
		Skipped:     100,
		HubsCreated: 3,
	}

	var mu sync.Mutex
	var logLines []string
	captureLogf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		mu.Lock()
		logLines = append(logLines, line)
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	w := NewWatcher(WatcherConfig{
		Exporter: fe,
		Interval: 10 * time.Millisecond,
		Logf:     captureLogf,
	})

	_ = w.Run(ctx)

	mu.Lock()
	lines := make([]string, len(logLines))
	copy(lines, logLines)
	mu.Unlock()

	if len(lines) == 0 {
		t.Fatal("no log lines captured")
	}

	for _, line := range lines {
		// Skip error lines
		if strings.Contains(line, "error") || strings.Contains(line, "Error") {
			continue
		}
		// Each success line must contain all required fields
		for _, field := range []string{"created=", "updated=", "deleted=", "skipped=", "hubs="} {
			if !strings.Contains(line, field) {
				t.Errorf("log line missing %q: %q", field, line)
			}
		}
	}

	t.Logf("captured %d log lines", len(lines))
	if len(lines) > 0 {
		t.Logf("sample log line: %q", lines[0])
	}
}

// ─── Task 3.5: TestWatcherContinuesOnError ────────────────────────────────────

// TestWatcherContinuesOnError verifies REQ-WATCH-04 error branch:
// when a cycle errors, the loop continues and the next cycle runs.
func TestWatcherContinuesOnError(t *testing.T) {
	fe := newFakeExporter()
	fe.errOnCycle[1] = errors.New("transient DB error")

	var mu sync.Mutex
	var logLines []string
	captureLogf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		mu.Lock()
		logLines = append(logLines, line)
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()

	w := NewWatcher(WatcherConfig{
		Exporter: fe,
		Interval: 10 * time.Millisecond,
		Logf:     captureLogf,
	})

	_ = w.Run(ctx)

	count := fe.Count()
	if count < 2 {
		t.Errorf("expected at least 2 cycles (error on 1, continue to 2+), got %d", count)
	}

	// At least one log line must contain the error message
	mu.Lock()
	lines := make([]string, len(logLines))
	copy(lines, logLines)
	mu.Unlock()

	foundError := false
	for _, line := range lines {
		if strings.Contains(line, "transient DB error") {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Errorf("expected error log line containing 'transient DB error', got lines: %v", lines)
	}

	t.Logf("total cycles after error-on-1: %d", count)
}

// ─── Task 3.7: TestWatcherGracefulShutdown ────────────────────────────────────

// TestWatcherGracefulShutdown verifies REQ-WATCH-05:
// canceling ctx mid-sleep causes Run() to return ctx.Err() without hanging.
func TestWatcherGracefulShutdown(t *testing.T) {
	fe := newFakeExporter()

	ctx, cancel := context.WithCancel(context.Background())

	w := NewWatcher(WatcherConfig{
		Exporter: fe,
		Interval: 1 * time.Second, // long interval so we're in the sleep when canceled
	})

	done := make(chan error, 1)
	go func() {
		done <- w.Run(ctx)
	}()

	// Let the first cycle complete, then cancel
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// Should return context.Canceled (or nil if impl returns nil on clean exit)
		if err != nil && err != context.Canceled {
			t.Errorf("Run() returned %v, want context.Canceled or nil", err)
		}
		t.Logf("Run() returned: %v", err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run() did not return after ctx cancel — potential goroutine leak")
	}
}

// ─── Task 3.9: TestWatcherFirstCycleGraphConfigOnly ──────────────────────────

// TestWatcherFirstCycleGraphConfigOnly verifies REQ-WATCH-06:
// - cycle 1 has the original GraphConfig value
// - cycle 2+ has GraphConfigSkip
func TestWatcherFirstCycleGraphConfigOnly(t *testing.T) {
	fe := newFakeExporter()
	fe.config.GraphConfig = GraphConfigForce // original value

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()

	w := NewWatcher(WatcherConfig{
		Exporter: fe,
		Interval: 10 * time.Millisecond,
	})

	_ = w.Run(ctx)

	configs := fe.GraphConfigs()
	if len(configs) < 2 {
		t.Fatalf("expected at least 2 cycles, got %d", len(configs))
	}

	// Cycle 1: must use original value (GraphConfigForce)
	if configs[0] != GraphConfigForce {
		t.Errorf("cycle 1 GraphConfig: got %q, want %q", configs[0], GraphConfigForce)
	}

	// Cycle 2+: must use GraphConfigSkip
	for i := 1; i < len(configs); i++ {
		if configs[i] != GraphConfigSkip {
			t.Errorf("cycle %d GraphConfig: got %q, want %q (GraphConfigSkip)", i+1, configs[i], GraphConfigSkip)
		}
	}

	t.Logf("GraphConfig per cycle: %v", configs)
}
