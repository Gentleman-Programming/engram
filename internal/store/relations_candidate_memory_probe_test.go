package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"modernc.org/libc"
	sqlitelib "modernc.org/sqlite/lib"
)

const candidateCounterProbeEnv = "ENGRAM_TEST_SQLITE_COUNTER_PROBE"
const candidateCounterProbePrefix = "sqlite_counter_probe "

// Every mode runs in a fresh test process: SQLite configuration never changes
// in the parent test runner or in a production binary.
func TestCandidateSQLiteCounterProbe(t *testing.T) {
	if mode := os.Getenv(candidateCounterProbeEnv); mode != "" {
		runCandidateCounterProbe(t, mode)
		return
	}
	for _, mode := range []string{"disabled", "enabled"} {
		t.Run(mode, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestCandidateSQLiteCounterProbe$", "-test.count=1")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(strings.ToUpper(entry), candidateCounterProbeEnv+"=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, candidateCounterProbeEnv+"="+mode)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("probe %s failed: %v\nstdout: %s\nstderr: %s", mode, err, stdout.String(), stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("probe %s unexpected stderr: %s", mode, stderr.String())
			}
			count := 0
			for _, line := range strings.Split(stdout.String(), "\n") {
				if !strings.HasPrefix(line, candidateCounterProbePrefix) {
					continue
				}
				count++
				var result candidateCounterProbeResult
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, candidateCounterProbePrefix)), &result); err != nil {
					t.Fatal(err)
				}
				if result.Mode != mode || result.ConfigResult != 0 || result.Candidates != 5 {
					t.Fatalf("unexpected probe result: %+v", result)
				}
				if mode == "disabled" {
					if result.Before != 0 || result.Peak != 0 || result.After != 0 {
						t.Fatalf("disabled counters must remain zero: %+v", result)
					}
				} else if result.Before <= 0 || result.After <= 0 || result.Peak <= result.Before || result.Peak < result.After {
					t.Fatalf("enabled counters are unavailable or inconsistent: %+v", result)
				}
				t.Logf("%s%s", candidateCounterProbePrefix, strings.TrimPrefix(line, candidateCounterProbePrefix))
			}
			if count != 1 {
				t.Fatalf("probe %s emitted %d measurement records; stdout: %s", mode, count, stdout.String())
			}
		})
	}
}

type candidateCounterProbeResult struct {
	Mode         string `json:"mode"`
	ConfigResult int32  `json:"config_result"`
	Before       int64  `json:"before_bytes"`
	Peak         int64  `json:"peak_bytes"`
	After        int64  `json:"after_bytes"`
	Candidates   int    `json:"candidates"`
}

func runCandidateCounterProbe(t *testing.T, mode string) {
	t.Helper()
	if mode != "enabled" && mode != "disabled" {
		t.Fatalf("unknown counter probe mode %q", mode)
	}
	tls := libc.NewTLS()
	defer tls.Close()
	rc := configureCandidateSQLiteMemstatus(tls, mode == "enabled")
	if rc != 0 {
		t.Fatalf("SQLite MEMSTATUS configuration failed before opening fixture: %d", rc)
	}
	// No connection has been opened before the configuration above. Fixture data
	// and all migrations are confined to candidateBenchCorpus's temporary folder.
	s, id := candidateBenchCorpus(t, 100, 3, false)
	before := sqlitelib.Xsqlite3_memory_used(tls)
	sqlitelib.Xsqlite3_memory_highwater(tls, 1)
	got, err := candidateBenchmarkLookup(s, id, candidateBenchmarkQueries()["FilteredMaterializedRank"])
	if err != nil {
		t.Fatal(err)
	}
	peak := sqlitelib.Xsqlite3_memory_highwater(tls, 0)
	after := sqlitelib.Xsqlite3_memory_used(tls)
	record, err := json.Marshal(candidateCounterProbeResult{Mode: mode, ConfigResult: rc, Before: before, Peak: peak, After: after, Candidates: len(got)})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", candidateCounterProbePrefix, record)
}

// Call only in a fresh test process before any SQLite connection is opened.
func configureCandidateSQLiteMemstatus(tls *libc.TLS, enabled bool) int32 {
	// The generated va adapter reads one promoted C int from an eight-byte slot.
	args := new([2]int32)
	if enabled {
		args[0] = 1
	}
	var pin runtime.Pinner
	pin.Pin(args)
	const sqliteConfigMemstatus = 9
	rc := sqlitelib.Xsqlite3_config(tls, sqliteConfigMemstatus, uintptr(unsafe.Pointer(args)))
	runtime.KeepAlive(args)
	pin.Unpin()
	return rc
}
