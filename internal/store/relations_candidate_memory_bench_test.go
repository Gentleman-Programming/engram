package store

import (
	"fmt"
	"os"
	"reflect"
	"testing"

	"modernc.org/libc"
	sqlitelib "modernc.org/sqlite/lib"
)

// SQLite's counters are library-wide: do not parallelize or run other SQLite
// work between the highwater reset and the reads after consuming the query.
func BenchmarkCandidateQuerySQLiteMemory(b *testing.B) {
	if os.Getenv(candidateMemoryCountersEnv) != "1" {
		b.Skip("SQLite allocator-memory benchmark requires " + candidateMemoryCountersEnv + "=1")
	}
	queries := candidateBenchmarkQueries()
	for _, size := range []int{1000, 15000} {
		for _, selective := range []bool{false, true} {
			project := "SingleProject"
			if selective {
				project = "SelectiveProject"
			}
			for _, baselineFirst := range []bool{true, false} {
				order := []string{"Baseline", "FilteredMaterializedRank"}
				if !baselineFirst {
					order[0], order[1] = order[1], order[0]
				}
				b.Run(fmt.Sprintf("Rows%d/Repeat20/%s/%sFirst", size, project, order[0]), func(b *testing.B) {
					b.StopTimer()
					s, id := candidateBenchCorpus(b, size, 20, selective)
					tls := libc.NewTLS()
					defer tls.Close()
					// Bound parity checking to five rows, outside all measurement windows.
					expected, err := candidateBenchmarkLookup(s, id, queries["Baseline"])
					if err != nil {
						b.Fatal(err)
					}
					if len(expected) != 5 {
						b.Fatalf("baseline returned %d candidates, want 5", len(expected))
					}
					for _, name := range order {
						got, err := candidateBenchmarkLookup(s, id, queries[name])
						if err != nil {
							b.Fatal(err)
						}
						if !reflect.DeepEqual(got, expected) {
							b.Fatalf("%s differs from baseline", name)
						}
					}
					var totals [2][4]int64
					for i := 0; i < b.N; i++ {
						for index, name := range order {
							before := sqlitelib.Xsqlite3_memory_used(tls)
							sqlitelib.Xsqlite3_memory_highwater(tls, 1)
							b.StartTimer()
							got, err := candidateBenchmarkLookup(s, id, queries[name])
							b.StopTimer()
							peak := sqlitelib.Xsqlite3_memory_highwater(tls, 0)
							after := sqlitelib.Xsqlite3_memory_used(tls)
							if err != nil {
								b.Fatal(err)
							}
							if before <= 0 || after <= 0 || peak < before || peak < after || peak <= before {
								b.Fatalf("%s SQLite allocator counters unavailable or inconsistent: before=%d peak=%d after=%d", name, before, peak, after)
							}
							if len(got) != 5 || !reflect.DeepEqual(got, expected) {
								b.Fatalf("%s measured result differs from five baseline candidates", name)
							}
							for j, value := range []int64{before, peak, after, peak - before} {
								totals[index][j] += value
							}
						}
					}
					for index, name := range order {
						for j, metric := range []string{"current-before", "peak", "current-after", "incremental-peak"} {
							b.ReportMetric(float64(totals[index][j])/float64(b.N), name+"-sqlite-allocator-"+metric+"-B")
						}
					}
				})
			}
		}
	}
}
