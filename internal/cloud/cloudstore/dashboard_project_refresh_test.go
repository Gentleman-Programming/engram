package cloudstore

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeProjectRefreshChunk(t *testing.T, cs *CloudStore, project, id, creator string) {
	t.Helper()
	stamp := "2026-01-01T00:00:00Z"
	if strings.HasSuffix(id, "2") {
		stamp = "2026-01-02T00:00:00Z"
	}
	payload := []byte(fmt.Sprintf(`{"sessions":[{"id":"s-%s","project":"%s","started_at":"2026-01-01T00:00:00Z"}],"observations":[{"sync_id":"o-%s","session_id":"s-%s","project":"%s","type":"note","title":"%s","content":"original","created_at":"%s"}],"prompts":[{"sync_id":"p-%s","session_id":"s-%s","project":"%s","content":"prompt","created_at":"%s"}]}`, project, project, id, project, project, id, stamp, id, project, project, stamp))
	if err := cs.WriteChunk(context.Background(), project, chunkIDFromPayload(payload), creator, "", payload); err != nil {
		t.Fatal(err)
	}
}

// Count actual decoded historical payloads through query-visible PostgreSQL
// functions. The views retain values and filter semantics; all writes precede
// their installation. Count only canonical history streams: PostgreSQL can also
// evaluate volatile view expressions for metadata-only inventory queries.
func countBetaHistoryReads(t *testing.T, cs *CloudStore) func(string) int64 {
	t.Helper()
	if _, err := cs.db.Exec(`CREATE SEQUENCE beta_chunk_reads; CREATE SEQUENCE beta_mutation_reads;
 CREATE FUNCTION count_history_payload(body jsonb,owner text,counter regclass) RETURNS jsonb LANGUAGE plpgsql VOLATILE AS $$ BEGIN IF owner='beta' AND (strpos(current_query(),'SELECT chunk_id, project_name, created_by, created_at, payload FROM cloud_chunks')>0 OR strpos(current_query(),'SELECT seq, project, entity, entity_key, op, payload, occurred_at FROM cloud_mutations')>0) THEN PERFORM nextval(counter); END IF; RETURN body; END $$;`); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ table, owner, counter string }{{"cloud_chunks", "project_name", "beta_chunk_reads"}, {"cloud_mutations", "project", "beta_mutation_reads"}} {
		wrapDashboardHistoryPayload(t, cs, item.table, fmt.Sprintf("count_history_payload(payload,%s,'%s'::regclass)", item.owner, item.counter))
	}

	return func(counter string) int64 {
		t.Helper()
		var value int64
		var called bool
		if err := cs.db.QueryRow("SELECT last_value,is_called FROM "+counter).Scan(&value, &called); err != nil {
			t.Fatal(err)
		}
		if !called {
			return 0
		}
		return value
	}
}

func wrapDashboardHistoryPayload(t *testing.T, cs *CloudStore, table, expression string) {
	t.Helper()
	var columns string
	if err := cs.db.QueryRow(`SELECT string_agg(quote_ident(column_name),', ' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 AND column_name<>'payload'`, table).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	query := fmt.Sprintf(`ALTER TABLE %s RENAME TO %s_history; CREATE VIEW %s AS SELECT %s,%s AS payload FROM %s_history`, table, table, table, columns, expression, table)
	if _, err := cs.db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardProjectRefreshSkipsUnchangedHistoryPostgres(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	writeProjectRefreshChunk(t, cs, "alpha", "alpha-1", "alice")
	writeProjectRefreshChunk(t, cs, "beta", "beta-1", "alice")
	if err := cs.SetProjectSyncEnabled("registered-empty", true, "tester", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.ListProjects(""); err != nil {
		t.Fatal(err)
	}
	writeProjectRefreshChunk(t, cs, "alpha", "alpha-2", "charlie")
	expected, err := cs.buildDashboardReadModel()
	if err != nil {
		t.Fatal(err)
	}
	reads := countBetaHistoryReads(t, cs)
	got, err := cs.loadDashboardNeutralReadModel()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("scoped refresh differs from independent full fold: got=%+v want=%+v", got, expected)
	}
	for _, counter := range []string{"beta_chunk_reads", "beta_mutation_reads"} {
		if count := reads(counter); count != 0 {
			t.Errorf("unchanged beta history replayed: %s=%d, want0", counter, count)
		}
	}
}

func TestDashboardProjectPartitionCertificate(t *testing.T) {
	for _, tc := range []struct {
		name, owner, chunk string
		safe               bool
	}{
		{"local", "alpha", `{"sessions":[{"id":"s","project":"alpha"}],"observations":[{"sync_id":"o","project":"alpha"}],"prompts":[{"sync_id":"p","project":"alpha"}]}`, true},
		{"fallback", "alpha", `{"sessions":[{"id":"s"}],"observations":[{"sync_id":"o"}],"prompts":[{"sync_id":"p"}]}`, true},
		{"session", "alpha", `{"sessions":[{"id":"s","project":"beta"}]}`, false},
		{"observation", "alpha", `{"observations":[{"sync_id":"o","project":"beta"}]}`, false},
		{"prompt", "alpha", `{"prompts":[{"sync_id":"p","project":"beta"}]}`, false},
		{"mutation", "alpha", `{"mutations":[{"entity":"session","entity_key":"s","op":"upsert","payload":"{\"id\":\"s\",\"project\":\"beta\"}"}]}`, false},
		{"raw envelope", " alpha ", `{}`, false},
		{"empty envelope", "", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var chunk engramsync.ChunkData
			if err := json.Unmarshal([]byte(tc.chunk), &chunk); err != nil {
				t.Fatal(err)
			}
			if got := dashboardChunkIsProjectLocal(dashboardChunkRow{project: tc.owner, parsed: chunk}); got != tc.safe {
				t.Fatalf("partition safe=%v want%v", got, tc.safe)
			}
		})
	}
	for _, tc := range []struct {
		owner, payload string
		safe           bool
	}{
		{"alpha", `{"project":"alpha"}`, true}, {"alpha", `{}`, true}, {"alpha", `{"project":"beta"}`, false}, {" alpha ", `{}`, false}, {"alpha", `{"project":42}`, false},
	} {
		if got := dashboardMutationIsProjectLocal(tc.owner, tc.payload); got != tc.safe {
			t.Errorf("mutation %q %s safe=%v", tc.owner, tc.payload, got)
		}
	}
}

func TestDashboardProjectRefreshMixedHistoryFallsBackPostgres(t *testing.T) {
	for _, tc := range []string{"historical mixed", "new mixed", "raw chunk owner", "raw mutation owner"} {
		t.Run(tc, func(t *testing.T) {
			cs := openIsolatedCloudStore(t)
			writeProjectRefreshChunk(t, cs, "alpha", "alpha-1", "alice")
			writeProjectRefreshChunk(t, cs, "beta", "beta-1", "bob")
			mixed := []byte(`{"observations":[{"sync_id":"cross","session_id":"s-beta","project":"beta","type":"note","title":"cross-project"}]}`)
			if tc == "historical mixed" {
				if err := cs.WriteChunk(context.Background(), "alpha", chunkIDFromPayload(mixed), "mixed", "", mixed); err != nil {
					t.Fatal(err)
				}
			}
			if tc == "raw chunk owner" {
				if _, err := cs.db.Exec(`UPDATE cloud_chunks SET project_name=' alpha ' WHERE project_name='alpha'`); err != nil {
					t.Fatal(err)
				}
			}
			if tc == "raw mutation owner" {
				if _, err := cs.db.Exec(`UPDATE cloud_mutations SET project=' alpha ' WHERE project='alpha'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := cs.ListProjects(""); err != nil {
				t.Fatal(err)
			}
			if tc == "new mixed" {
				if err := cs.WriteChunk(context.Background(), "alpha", chunkIDFromPayload(mixed), "mixed", "", mixed); err != nil {
					t.Fatal(err)
				}
			} else {
				writeProjectRefreshChunk(t, cs, "alpha", "alpha-2", "alice")
			}
			expected, err := cs.buildDashboardReadModel()
			if err != nil {
				t.Fatal(err)
			}
			reads := countBetaHistoryReads(t, cs)
			got, err := cs.loadDashboardNeutralReadModel()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("fallback differs from full fold: got%+v want%+v", got, expected)
			}
			if got.projectPartitionSafe {
				t.Fatal("unsafe historical ownership certified partitionable")
			}
			if reads("beta_chunk_reads") == 0 || reads("beta_mutation_reads") == 0 {
				t.Fatal("unsafe history did not use global replay")
			}
		})
	}
}

func TestDashboardProjectRefreshDeletesAndReplacementPostgres(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	writeProjectRefreshChunk(t, cs, "alpha", "alpha-1", "alice")
	writeProjectRefreshChunk(t, cs, "beta", "beta-1", "bob")
	before, err := cs.loadDashboardNeutralReadModel()
	if err != nil {
		t.Fatal(err)
	}
	batch := []MutationEntry{
		{Project: "alpha", Entity: store.SyncEntitySession, EntityKey: "s-alpha", Op: store.SyncOpUpsert, Payload: json.RawMessage(`{"id":"s-alpha","project":"alpha","directory":"/alpha","started_at":"2026-01-01T00:00:00Z","ended_at":"2026-01-02T00:00:00Z","summary":"closed"}`)},
		{Project: "alpha", Entity: store.SyncEntityObservation, EntityKey: "o-alpha-1", Op: store.SyncOpDelete, Payload: json.RawMessage(`{"sync_id":"o-alpha-1","project":"alpha"}`)},
		{Project: "alpha", Entity: store.SyncEntityPrompt, EntityKey: "p-alpha-1", Op: store.SyncOpDelete, Payload: json.RawMessage(`{"sync_id":"p-alpha-1","project":"alpha"}`)},
	}
	if _, err := cs.InsertMutationBatch(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	expected, err := cs.buildDashboardReadModel()
	if err != nil {
		t.Fatal(err)
	}
	got, err := cs.loadDashboardNeutralReadModel()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("mutation refresh differs: got%+v want%+v", got, expected)
	}
	if len(got.projectDetails["alpha"].Observations) != 0 || len(got.projectDetails["alpha"].Prompts) != 0 {
		t.Fatal("deleted content remains visible")
	}
	if !reflect.DeepEqual(before.projectDetails["beta"], got.projectDetails["beta"]) {
		t.Fatal("unaffected project changed")
	}
	if len(before.projectDetails["alpha"].Observations) != 1 || len(before.projectDetails["alpha"].Prompts) != 1 {
		t.Fatal("published baseline was mutated")
	}
}

func TestDashboardProjectInvalidationFencesDirtyUnion(t *testing.T) {
	base := dashboardPrincipalReadModel()
	base.projectPartitionSafe = true
	cs := &CloudStore{dashboardReadModel: base, dashboardReadModelOK: true}
	cs.invalidateDashboardProjects("project-a")
	stale := &dashboardReadModelBuild{gen: cs.dashboardReadModelGen, done: make(chan struct{}), model: base}
	cs.dashboardReadModelBuild = stale
	cs.invalidateDashboardProjects("project-b", "project-a")
	cs.finishDashboardReadModelBuild(stale)
	if cs.dashboardReadModelOK || len(cs.dashboardReadModelDirty) != 2 {
		t.Fatalf("obsolete generation published/cleared dirty union: %+v", cs.dashboardReadModelDirty)
	}
	cs.invalidateDashboardReadModel()
	if cs.dashboardReadModel.projectPartitionSafe || cs.dashboardReadModelDirty != nil {
		t.Fatal("hard invalidation retained partition baseline")
	}
	fresh := &dashboardReadModelBuild{gen: cs.dashboardReadModelGen, done: make(chan struct{}), model: base}
	cs.finishDashboardReadModelBuild(fresh)
	if !cs.dashboardReadModelOK || cs.dashboardReadModelDirty != nil {
		t.Fatal("current build not published cleanly")
	}
}

func TestDashboardProjectRefreshFailureDoesNotCachePostgres(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	writeProjectRefreshChunk(t, cs, "alpha", "alpha-1", "alice")
	before, err := cs.loadDashboardNeutralReadModel()
	if err != nil {
		t.Fatal(err)
	}
	writeProjectRefreshChunk(t, cs, "alpha", "alpha-2", "alice")
	if _, err := cs.db.Exec(`CREATE FUNCTION refresh_payload(body jsonb) RETURNS jsonb LANGUAGE plpgsql VOLATILE AS $$ BEGIN RAISE EXCEPTION 'controlled refresh failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	wrapDashboardHistoryPayload(t, cs, "cloud_chunks", "refresh_payload(payload)")
	if _, err := cs.loadDashboardNeutralReadModel(); err == nil {
		t.Fatal("failed refresh returned success")
	}
	if cs.dashboardReadModelOK || len(cs.dashboardReadModelDirty) != 1 {
		t.Fatal("failed refresh cached or lost invalidation")
	}
	if !reflect.DeepEqual(before, cs.dashboardReadModel) {
		t.Fatal("failed refresh mutated immutable baseline")
	}
	if _, err := cs.db.Exec(`DROP VIEW cloud_chunks; ALTER TABLE cloud_chunks_history RENAME TO cloud_chunks`); err != nil {
		t.Fatal(err)
	}
	expected, err := cs.buildDashboardReadModel()
	if err != nil {
		t.Fatal(err)
	}
	got, err := cs.loadDashboardNeutralReadModel()
	if err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("refresh retry got%+v err%v", got, err)
	}
}

func TestDashboardProjectRefreshConcurrentInvalidationPostgres(t *testing.T) {
	for _, hard := range []bool{false, true} {
		t.Run(fmt.Sprintf("hard=%v", hard), func(t *testing.T) {
			cs := openIsolatedCloudStore(t)
			writeProjectRefreshChunk(t, cs, "alpha", "alpha-1", "alice")
			writeProjectRefreshChunk(t, cs, "beta", "beta-1", "bob")
			if _, err := cs.ListProjects(""); err != nil {
				t.Fatal(err)
			}
			writeProjectRefreshChunk(t, cs, "alpha", "alpha-2", "alice")
			if _, err := cs.db.Exec(`CREATE SEQUENCE refresh_entered;
  CREATE FUNCTION refresh_payload(body jsonb,owner text) RETURNS jsonb LANGUAGE plpgsql VOLATILE AS $$ BEGIN
  IF owner='alpha' AND strpos(current_query(),'SELECT chunk_id, project_name, created_by, created_at, payload FROM cloud_chunks')>0 THEN
   PERFORM nextval('refresh_entered'); PERFORM pg_advisory_xact_lock(hashtext(current_schema()));
  END IF; RETURN body; END $$`); err != nil {
				t.Fatal(err)
			}
			wrapDashboardHistoryPayload(t, cs, "cloud_chunks", "refresh_payload(payload,project_name)")
			lock, err := cs.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_, _ = lock.ExecContext(context.Background(), "SELECT pg_advisory_unlock_all()")
				_ = lock.Close()
			}()
			if _, err := lock.ExecContext(context.Background(), "SELECT pg_advisory_lock(hashtext(current_schema()))"); err != nil {
				t.Fatal(err)
			}
			results := make(chan characterizationResult, 1)
			go characterizationRead(cs, results)
			entered := false
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if err := cs.db.QueryRow("SELECT is_called FROM refresh_entered").Scan(&entered); err != nil {
					t.Fatal(err)
				}
				if entered {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if !entered {
				t.Fatal("scoped history read did not reach barrier")
			}
			select {
			case <-results:
				t.Fatal("scoped read served stale data instead of waiting")
			default:
			}
			if hard {
				cs.invalidateDashboardReadModel()
			} else {
				cs.invalidateDashboardProjects("beta")
			}
			if _, err := lock.ExecContext(context.Background(), "SELECT pg_advisory_unlock_all()"); err != nil {
				t.Fatal(err)
			}
			result := characterizationWait(t, results)
			if result.err != nil {
				t.Fatal(result.err)
			}
			if cs.dashboardReadModelOK {
				t.Fatal("obsolete scoped result published after invalidation")
			}
			if hard {
				if cs.dashboardReadModel.projectPartitionSafe || cs.dashboardReadModelDirty != nil {
					t.Fatal("hard invalidation did not discard baseline")
				}
			} else {
				if len(cs.dashboardReadModelDirty) != 2 {
					t.Fatalf("dirty union lost:%+v", cs.dashboardReadModelDirty)
				}
			}
			expected, err := cs.buildDashboardReadModel()
			if err != nil {
				t.Fatal(err)
			}
			got, err := cs.loadDashboardNeutralReadModel()
			if err != nil || !reflect.DeepEqual(got, expected) {
				t.Fatalf("follow-up got%+v err%v", got, err)
			}
		})
	}
}
