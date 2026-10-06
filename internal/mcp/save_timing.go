package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// saveTiming is invocation-local; diagnostic records contain no memory data or
// identifiers. Exactly "1" opts in, leaving the default path uninstrumented.
type saveTiming struct {
	started    time.Time
	save       time.Duration
	candidates store.CandidateTimings
	saved      bool
}

func beginSaveTiming() *saveTiming {
	if os.Getenv("ENGRAM_MEM_SAVE_TIMING") != "1" {
		return nil
	}
	return &saveTiming{started: time.Now()}
}

func (t *saveTiming) finish() {
	if t == nil {
		return
	}
	status := "not_saved"
	if t.saved {
		status = "saved"
	}
	record := struct {
		Status            string  `json:"status"`
		TotalMS           float64 `json:"total_ms"`
		SaveMS            float64 `json:"save_ms"`
		CandidateLookupMS float64 `json:"candidate_lookup_ms"`
		RelationInsertMS  float64 `json:"relation_insert_ms"`
	}{status, milliseconds(time.Since(t.started)), milliseconds(t.save), milliseconds(t.candidates.Lookup), milliseconds(t.candidates.Inserts)}
	data, err := json.Marshal(record)
	if err == nil {
		fmt.Fprintf(os.Stderr, "engram: mem_save_timing %s\n", data)
	}
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
