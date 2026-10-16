package cloudstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"os"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
)

const dashboardPGDSN = "postgres://engram1832@127.0.0.1:15432/engram1832_measurement?sslmode=disable"
const dashboardPGSession = `{"id":"s","project":"bench","started_at":"2026-01-01T00:00:00Z","directory":"/synthetic"}`
const dashboardPGObservation = `{"sync_id":"o","session_id":"s","project":"bench","scope":"project","type":"decision","title":"fixed","content":"synthetic","created_at":"2026-01-01T00:00:00Z"}`
const dashboardPGChunk = `{"sessions":[` + dashboardPGSession + `],"observations":[` + dashboardPGObservation + `]}`

// Baseline instrumentation only. No production seam, global statistics reset,
// inherited database, server lifecycle operation or new consistency policy.
type dashboardPGFixture struct {
	cs      *CloudStore
	db      *sql.DB
	builds  atomic.Int64
	buildNS atomic.Int64
	entered chan struct{}
	release chan struct{}
}

func dashboardPGOpen(tb testing.TB, axis string, history int) *dashboardPGFixture {
	tb.Helper()
	raw := os.Getenv("ENGRAM_DASHBOARD_BENCH_DSN")
	if raw == "" {
		tb.Skip("set ENGRAM_DASHBOARD_BENCH_DSN to the documented owned fixture")
	}
	if raw != dashboardPGDSN {
		tb.Fatal("measurement DSN is not the exact owned fixture")
	}
	u, err := url.Parse(raw)
	if err != nil {
		tb.Fatal(err)
	}
	db, err := sql.Open("pgx", raw)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := db.Close(); err != nil {
			tb.Errorf("close measurement connection: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var version string
	if err := db.QueryRowContext(ctx, "SHOW server_version").Scan(&version); err != nil {
		tb.Fatal(err)
	}
	if version != "18.6" {
		tb.Fatalf("expected owned PG18.6, got %q", version)
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		tb.Fatal(err)
	}
	schema := "dashboard_pg_" + hex.EncodeToString(nonce[:])
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			tb.Errorf("drop owned schema: %v", err)
		}
	})
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	cs, err := New(cloud.Config{DSN: u.String()})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := cs.Close(); err != nil {
			tb.Errorf("close CloudStore: %v", err)
		}
	})
	f := &dashboardPGFixture{cs: cs, db: db}
	f.write(tb, dashboardPGChunk)
	switch axis {
	case "chunks":
		// Empty valid chunks change history/counter, not current entity contents.
		for i := 0; i < history; i++ {
			f.write(tb, fmt.Sprintf(`{"sessions":[],"observations":[],"prompts":[],"fixture_nonce":%d}`, i))
		}
	case "mutations":
		entries := make([]MutationEntry, history)
		for i := range entries {
			entries[i] = MutationEntry{Project: "bench", Entity: "session", EntityKey: "s", Op: "upsert", Payload: []byte(dashboardPGSession), CreatedBy: "synthetic"}
		}
		seqs, err := cs.InsertMutationBatch(context.Background(), entries)
		if err != nil || len(seqs) != history {
			tb.Fatalf("seed mutation batch accepted=%d, want %d: %v", len(seqs), history, err)
		}
	default:
		tb.Fatalf("unknown history axis %q", axis)
	}
	cs.dashboardReadModelLoad = func() (dashboardReadModel, error) {
		f.builds.Add(1)
		if f.entered != nil {
			f.entered <- struct{}{}
			select {
			case <-f.release:
			case <-time.After(10 * time.Second):
				return dashboardReadModel{}, fmt.Errorf("controlled build barrier timed out")
			}
		}
		start := time.Now()
		model, err := cs.buildDashboardReadModel()
		f.buildNS.Add(time.Since(start).Nanoseconds())
		return model, err
	}
	detail := f.detail(tb)
	if detail.Stats.Sessions != 1 || detail.Stats.Observations != 1 || len(detail.Sessions) != 1 || len(detail.Observations) != 1 || detail.Sessions[0].SessionID != "s" || detail.Observations[0].SyncID != "o" || detail.Observations[0].Title != "fixed" || detail.Observations[0].Content != "synthetic" {
		tb.Fatalf("fixture changed final live entities: %+v", detail)
	}
	var chunks, mutations, payloadBytes int64
	if err := cs.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM cloud_chunks), (SELECT count(*) FROM cloud_mutations), (SELECT COALESCE(sum(octet_length(payload::text)),0) FROM cloud_chunks) + (SELECT COALESCE(sum(octet_length(payload::text)),0) FROM cloud_mutations)`).Scan(&chunks, &mutations, &payloadBytes); err != nil {
		tb.Fatal(err)
	}
	tb.Logf("schema=%s axis=%s history=%d actual_chunks=%d actual_mutations=%d JSON_payload_bytes=%d live_sessions=1 live_observations=1 PG=%s", schema, axis, history, chunks, mutations, payloadBytes, version)
	return f
}

func (f *dashboardPGFixture) write(tb testing.TB, payload string) {
	tb.Helper()
	if err := f.cs.WriteChunk(context.Background(), "bench", chunkIDFromPayload([]byte(payload)), "synthetic", "2026-01-01T00:00:00Z", []byte(payload)); err != nil {
		tb.Fatal(err)
	}
}

func (f *dashboardPGFixture) detail(tb testing.TB) DashboardProjectDetail {
	tb.Helper()
	detail, err := f.cs.ProjectDetail("bench")
	if err != nil {
		tb.Fatal(err)
	}
	return detail
}

func (f *dashboardPGFixture) calls(tb testing.TB) int64 {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls int64
	// Exclusive workload on the owned DB. Parameters prevent the sampler query
	// from matching its own SQL. All three actual read-model SELECT shapes count.
	if err := f.db.QueryRowContext(ctx, `SELECT COALESCE(sum(calls),0)::bigint FROM public.pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database()) AND (query LIKE $1 OR query LIKE $2 OR query LIKE $3)`, "SELECT chunk_id, project_name, created_by, created_at, payload FROM cloud_chunks%", "SELECT seq, project, entity, entity_key, op, payload::text, occurred_at FROM cloud_mutations%", "%LEFT JOIN cloud_project_controls c ON c.project = p.project%").Scan(&calls); err != nil {
		tb.Fatal(err)
	}
	return calls
}

func (f *dashboardPGFixture) prepare(tb testing.TB, mode string) {
	tb.Helper()
	switch mode {
	case "cold", "concurrent":
		f.cs.invalidateDashboardReadModel()
	case "after-write":
		f.write(tb, dashboardPGChunk)
	case "after-control":
		if err := f.cs.SetProjectSyncEnabled("bench", true, "synthetic", "measurement"); err != nil {
			tb.Fatal(err)
		}
	case "warm":
	default:
		tb.Fatalf("unknown mode %q", mode)
	}
}

func (f *dashboardPGFixture) read(tb testing.TB, mode string) {
	tb.Helper()
	if mode != "concurrent" {
		rows, err := f.cs.ListProjects("")
		if err != nil || len(rows) != 1 || rows[0].Project != "bench" || rows[0].Sessions != 1 || rows[0].Observations != 1 {
			tb.Fatalf("public read: %+v %v", rows, err)
		}
		return
	}
	// Four forced overlapping cache misses. Real SQL/folding occurs after the
	// barrier; this does not estimate naturally occurring concurrency incidence.
	f.entered = make(chan struct{}, 4)
	f.release = make(chan struct{})
	results := make(chan characterizationResult, 4)
	for i := 0; i < 4; i++ {
		go characterizationRead(f.cs, results)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-f.entered:
		case <-time.After(10 * time.Second):
			close(f.release)
			tb.Fatal("cold callers did not all enter")
		}
	}
	close(f.release)
	for i := 0; i < 4; i++ {
		select {
		case result := <-results:
			if result.err != nil || len(result.rows) != 1 || result.rows[0].Project != "bench" || result.rows[0].Sessions != 1 || result.rows[0].Observations != 1 {
				tb.Fatalf("concurrent public read: %+v %v", result.rows, result.err)
			}
		case <-time.After(10 * time.Second):
			tb.Fatal("concurrent public read timed out")
		}
	}
	f.entered = nil
	f.release = nil
}

func TestDashboardReadModelPostgresCounts(t *testing.T) {
	for _, axis := range []string{"chunks", "mutations"} {
		for _, history := range []int{10, 100, 1000} {
			t.Run(fmt.Sprintf("%s/H%d", axis, history), func(t *testing.T) {
				f := dashboardPGOpen(t, axis, history)
				baseline := f.detail(t)
				for _, mode := range []string{"cold", "warm", "after-write", "after-control", "concurrent"} {
					f.prepare(t, mode)
					queries, builds := f.calls(t), f.builds.Load()
					f.read(t, mode)
					want := int64(1)
					if mode == "warm" {
						want = 0
					}
					if mode == "concurrent" {
						want = 4
					}
					if got := f.calls(t) - queries; got != want*3 {
						t.Fatalf("%s SQL SELECTs=%d want=%d", mode, got, want*3)
					}
					if got := f.builds.Load() - builds; got != want {
						t.Fatalf("%s builds=%d want=%d", mode, got, want)
					}
					if got := f.detail(t); !reflect.DeepEqual(got, baseline) {
						t.Fatalf("%s changed public state: got=%+v want=%+v", mode, got, baseline)
					}
					t.Logf("mode=%s actual_SELECTs=%d actual_builds=%d", mode, want*3, want)
				}
			})
		}
	}
}

func TestDashboardReadModelPostgresControlsAndRecreation(t *testing.T) {
	f := dashboardPGOpen(t, "mutations", 10)
	if err := f.cs.SetProjectSyncEnabled("empty", true, "synthetic", ""); err != nil {
		t.Fatal(err)
	}
	rows, err := f.cs.ListProjects("")
	if err != nil || len(rows) != 2 || rows[1] != (DashboardProjectRow{Project: "empty"}) {
		t.Fatalf("empty registration: %+v %v", rows, err)
	}
	for _, op := range []string{"delete", "upsert"} {
		seqs, err := f.cs.InsertMutationBatch(context.Background(), []MutationEntry{{Project: "bench", Entity: "observation", EntityKey: "o", Op: op, Payload: []byte(dashboardPGObservation), CreatedBy: "synthetic"}})
		if err != nil || len(seqs) != 1 {
			t.Fatalf("%s accepted=%v error=%v", op, seqs, err)
		}
		detail := f.detail(t)
		want := 0
		if op == "upsert" {
			want = 1
		}
		if detail.Stats.Observations != want || len(detail.Observations) != want || detail.Stats.Sessions != 1 {
			t.Fatalf("%s state: %+v", op, detail)
		}
		if want == 1 && (detail.Observations[0].SyncID != "o" || detail.Observations[0].Title != "fixed") {
			t.Fatalf("recreated observation: %+v", detail.Observations)
		}
	}
}

func BenchmarkDashboardReadModelPostgres(b *testing.B) {
	for _, axis := range []string{"chunks", "mutations"} {
		for _, history := range []int{10, 100, 1000} {
			for _, mode := range []string{"warm", "cold", "after-write", "after-control", "concurrent"} {
				b.Run(fmt.Sprintf("%s/H%d/%s", axis, history, mode), func(b *testing.B) {
					f := dashboardPGOpen(b, axis, history)
					queries, builds, buildNS := f.calls(b), f.builds.Load(), f.buildNS.Load()
					latencies := make([]int64, 0, b.N)
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						b.StopTimer()
						f.prepare(b, mode)
						b.StartTimer()
						start := time.Now()
						f.read(b, mode)
						latencies = append(latencies, time.Since(start).Nanoseconds())
					}
					b.StopTimer()
					qdelta, bdelta := f.calls(b)-queries, f.builds.Load()-builds
					want := int64(1)
					if mode == "warm" {
						want = 0
					}
					if mode == "concurrent" {
						want = 4
					}
					if qdelta != int64(b.N)*want*3 || bdelta != int64(b.N)*want {
						b.Fatalf("measurement counts: SELECTs=%d builds=%d operations=%d", qdelta, bdelta, b.N)
					}
					sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
					b.ReportMetric(float64(qdelta)/float64(b.N), "SELECTs/op")
					b.ReportMetric(float64(bdelta)/float64(b.N), "builds/op")
					b.ReportMetric(float64(f.buildNS.Load()-buildNS)/float64(b.N), "build-ns/op")
					b.ReportMetric(float64(latencies[int(math.Ceil(.95*float64(len(latencies))))-1]), "sample-p95-ns")
					b.Logf("p95 is nearest-rank over %d operation samples; concurrent op=4 barrier-controlled callers; mutation/write/control preparation and SQL sampler excluded", b.N)
				})
			}
		}
	}
}
