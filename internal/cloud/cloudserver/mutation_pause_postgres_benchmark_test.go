package cloudserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
)

// Opt-in characterization of baseline 7404e0b7, not an optimization target.
// Only the dedicated, owned PostgreSQL 18.6 fixture is allowed. No inherited
// CLOUDSTORE_TEST_DSN, global statistics reset, or server lifecycle operations.
func pausePGFixture(tb testing.TB, distinct int) (*sql.DB, *cloudstore.CloudStore, http.Handler, []byte, string) {
	tb.Helper()
	raw := os.Getenv("ENGRAM_PAUSE_BENCH_DSN")
	if raw == "" {
		tb.Skip("set ENGRAM_PAUSE_BENCH_DSN to the owned measurement fixture")
	}
	u, err := url.Parse(raw)
	if err != nil {
		tb.Fatal("invalid measurement DSN")
	}
	// Exact comparison also rejects passwords, alternate hosts, and pgx URL
	// options that could redirect connections before any database effect.
	const allowed = "postgres://engram1831@127.0.0.1:15431/engram1831_measurement?sslmode=disable"
	if raw != allowed || u.Hostname() != "127.0.0.1" || u.Port() != "15431" || u.User == nil || u.User.Username() != "engram1831" || u.Path != "/engram1831_measurement" {
		tb.Fatal("measurement DSN is not the exact owned fixture")
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
		tb.Fatalf("expected owned PG 18.6, got %q", version)
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		tb.Fatal(err)
	}
	schema := "pause_pg_" + hex.EncodeToString(nonce[:])
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			tb.Errorf("drop owned schema %s: %v", schema, err)
		}
	})
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	cs, err := cloudstore.New(cloud.Config{DSN: u.String()})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := cs.Close(); err != nil {
			tb.Errorf("close CloudStore: %v", err)
		}
	})
	projects := make([]string, distinct)
	for i := range projects {
		projects[i] = fmt.Sprintf("pause-pg-%03d", i)
	}
	entries := make([]MutationEntry, 100)
	for i := range entries {
		id := fmt.Sprintf("session-%03d", i)
		payload, err := json.Marshal(map[string]string{"id": id, "project": projects[i%distinct], "directory": "/synthetic", "started_at": "2026-01-01T00:00:00Z"})
		if err != nil {
			tb.Fatal(err)
		}
		entries[i] = MutationEntry{Project: projects[i%distinct], Entity: "session", EntityKey: id, Op: "upsert", Payload: payload}
	}
	body, err := json.Marshal(mutationPushEnvelope{Entries: entries})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Logf("owned schema=%s PG=%s B=100 D=%d; synthetic loopback, repeated payload", schema, version, distinct)
	return db, cs, New(cs, multiProjectAuth{token: "pause-pg", projects: projects}, 0).Handler(), body, schema
}

func pausePGCalls(tb testing.TB, db *sql.DB) int64 {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls int64
	// Aggregate the exact normalized concrete-store SELECT shape, scoped to this
	// database. Fresh schema relation OIDs avoid inherited query identities. Do
	// not run other pause readers concurrently: their calls would contaminate it.
	const query = `SELECT COALESCE(sum(calls), 0)::bigint FROM public.pg_stat_statements
		WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
		AND btrim(regexp_replace(query, '\s+', ' ', 'g')) = $1`
	if err := db.QueryRowContext(ctx, query, "SELECT sync_enabled FROM cloud_project_controls WHERE project = $1").Scan(&calls); err != nil {
		tb.Fatal(err)
	}
	return calls
}

func pausePGPush(handler http.Handler, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer pause-pg")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func pausePGAccepted(tb testing.TB, rec *httptest.ResponseRecorder) {
	tb.Helper()
	var response struct {
		AcceptedSeqs []int64 `json:"accepted_seqs"`
	}
	if rec.Code != http.StatusOK {
		tb.Fatalf("push HTTP %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		tb.Fatal(err)
	}
	if len(response.AcceptedSeqs) != 100 {
		tb.Fatalf("accepted %d entries, want 100", len(response.AcceptedSeqs))
	}
}

type pausePGStream struct {
	Mutations []StoredMutation `json:"mutations"`
	LatestSeq int64            `json:"latest_seq"`
}

func pausePGReadAll(tb testing.TB, handler http.Handler) pausePGStream {
	tb.Helper()
	var stream pausePGStream
	var cursor int64
	for {
		pull := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/sync/mutations/pull?since_seq=%d&limit=25", cursor), nil)
		pull.Header.Set("Authorization", "Bearer pause-pg")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, pull)
		var page struct {
			pausePGStream
			HasMore bool `json:"has_more"`
		}
		if rec.Code != http.StatusOK {
			tb.Fatalf("pull HTTP %d: %s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			tb.Fatal(err)
		}
		for _, mutation := range page.Mutations {
			if mutation.Seq <= cursor {
				tb.Fatalf("non-advancing mutation cursor: %d after %d", mutation.Seq, cursor)
			}
			cursor = mutation.Seq
		}
		stream.Mutations = append(stream.Mutations, page.Mutations...)
		stream.LatestSeq = page.LatestSeq
		if !page.HasMore {
			return stream
		}
		if len(page.Mutations) == 0 {
			tb.Fatal("has_more without mutations")
		}
	}
}

// An accepted append must be visible beyond the old limit=100 first page.
func TestPausePGPullSnapshotPagination(t *testing.T) {
	_, _, handler, body, _ := pausePGFixture(t, 1)
	pausePGAccepted(t, pausePGPush(handler, body))
	before := pausePGReadAll(t, handler)
	pausePGAccepted(t, pausePGPush(handler, body))
	after := pausePGReadAll(t, handler)
	if len(before.Mutations) != 100 || len(after.Mutations) != 200 || reflect.DeepEqual(before, after) {
		t.Fatalf("snapshot missed append: before=%d after=%d", len(before.Mutations), len(after.Mutations))
	}
}

func TestMutationPausePostgresQueryCount(t *testing.T) {
	for _, distinct := range []int{1, 10, 100} {
		t.Run(fmt.Sprintf("B100/D%d", distinct), func(t *testing.T) {
			db, cs, handler, body, _ := pausePGFixture(t, distinct)
			before := pausePGCalls(t, db)
			pausePGAccepted(t, pausePGPush(handler, body))
			if delta := pausePGCalls(t, db) - before; delta != 100 {
				t.Fatalf("control SELECT calls=%d, want 100", delta)
			}
			t.Log("control-selects/request=100")
			persistedBefore := pausePGReadAll(t, handler)
			if len(persistedBefore.Mutations) != 100 {
				t.Fatalf("accepted mutation snapshot=%d, want 100", len(persistedBefore.Mutations))
			}
			if err := cs.SetProjectSyncEnabled("pause-pg-000", false, "synthetic", "characterization"); err != nil {
				t.Fatal(err)
			}
			before = pausePGCalls(t, db)
			if rec := pausePGPush(handler, body); rec.Code != http.StatusConflict {
				t.Fatalf("paused HTTP %d: %s", rec.Code, rec.Body.String())
			}
			if delta := pausePGCalls(t, db) - before; delta != 1 {
				t.Fatalf("paused control SELECT calls=%d, want 1", delta)
			}
			// Compare all pages, including sequences, identities and raw payloads.
			if persistedAfter := pausePGReadAll(t, handler); !reflect.DeepEqual(persistedBefore, persistedAfter) {
				t.Fatalf("rejection changed complete public mutation snapshot: before=%+v after=%+v", persistedBefore, persistedAfter)
			}
		})
	}
}

// Warm store/connections: one untimed successful request precedes measurement.
// Repeated identical payloads replay the same entities/chunks, but baseline
// InsertMutationBatch appends 100 journal rows per request (no journal dedup).
// Thus the journal grows with b.N; this is not fresh-entity insertion latency.
// Timer includes HTTP handling, pgx and insertion; excludes setup, migrations,
// statistics reads and response assertions. No production latency or speedup claim.
func BenchmarkMutationPausePostgres(b *testing.B) {
	for _, distinct := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("B100/D%d", distinct), func(b *testing.B) {
			b.StopTimer()
			db, _, handler, body, _ := pausePGFixture(b, distinct)
			pausePGAccepted(b, pausePGPush(handler, body))
			before := pausePGCalls(b, db)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StartTimer()
				rec := pausePGPush(handler, body)
				b.StopTimer()
				pausePGAccepted(b, rec)
			}
			delta := pausePGCalls(b, db) - before
			if delta != int64(b.N)*100 {
				b.Fatalf("control SELECT calls=%d, want %d", delta, int64(b.N)*100)
			}
			b.ReportMetric(float64(delta)/float64(b.N), "control-selects/op")
		})
	}
}
