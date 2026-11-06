package store

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const semanticSnippetSQL = "SELECT title, type, ifnull(content,'') FROM observations WHERE sync_id = ?"

// Delegate every operation to SQLite except the exact snippet lookup for one
// synthetic observation. Returning Rows errors exercises QueryRow.Scan itself.
type snippetFaultConnector struct {
	driver     driver.Driver
	dsn, badID string
	failure    error
	hits       int
}

func (c *snippetFaultConnector) Driver() driver.Driver { return c.driver }
func (c *snippetFaultConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &snippetFaultConn{Conn: conn, fault: c}, nil
}

type snippetFaultConn struct {
	driver.Conn
	fault *snippetFaultConnector
}

func (c *snippetFaultConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query == semanticSnippetSQL && len(args) == 1 && args[0].Value == c.fault.badID {
		c.fault.hits++
		return &snippetFaultRows{failure: c.fault.failure}, nil
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}
func (c *snippetFaultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

type snippetFaultRows struct{ failure error }

func (*snippetFaultRows) Columns() []string           { return []string{"title", "type", "content"} }
func (*snippetFaultRows) Close() error                { return nil }
func (r *snippetFaultRows) Next([]driver.Value) error { return r.failure }

type snippetRecordingRunner struct{ prompts []string }

func (r *snippetRecordingRunner) Compare(_ context.Context, prompt string) (SemanticVerdict, error) {
	r.prompts = append(r.prompts, prompt)
	return SemanticVerdict{Relation: "compatible", Confidence: 0.9, Reasoning: "synthetic healthy pair", Model: "test"}, nil
}

func TestScanProject_SemanticSnippetLookupFailure(t *testing.T) {
	for _, badIndex := range []int{0, 2} {
		for _, failure := range []error{errors.New("synthetic row read failure"), io.EOF} {
			for _, apply := range []bool{false, true} {
				t.Run(fmt.Sprintf("bad-index=%d/error=%v/apply=%v", badIndex, failure, apply), func(t *testing.T) {
					s := newTestStore(t)
					const project = "snippet-lookup-test"
					if err := s.CreateSession("snippet-session", project, "/synthetic"); err != nil {
						t.Fatal(err)
					}
					var snippets []ObservationSnippet
					for i := 0; i < 3; i++ {
						title := fmt.Sprintf("JWT auth token session security approach private-title-%d", i)
						content := fmt.Sprintf("private-content-%d full synthetic JWT token session details", i)
						id, err := s.AddObservation(AddObservationParams{SessionID: "snippet-session", Type: "decision", Title: title, Content: content, Project: project, Scope: "project"})
						if err != nil {
							t.Fatal(err)
						}
						obs, err := s.GetObservation(id)
						if err != nil {
							t.Fatal(err)
						}
						snippets = append(snippets, ObservationSnippet{ID: id, SyncID: obs.SyncID, Title: title, Type: "decision", Content: content})
					}
					if err := s.EnrollProject(project); err != nil {
						t.Fatal(err)
					}
					before, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 1000)
					if err != nil {
						t.Fatal(err)
					}
					stateBefore, err := s.GetSyncState(DefaultSyncTargetKey)
					if err != nil {
						t.Fatal(err)
					}

					originalDB := s.db
					fault := &snippetFaultConnector{driver: originalDB.Driver(), dsn: filepath.Join(s.DataDir(), "engram.db"), badID: snippets[badIndex].SyncID, failure: failure}
					wrapped := sql.OpenDB(fault)
					wrapped.SetMaxOpenConns(1)
					s.db = wrapped
					t.Cleanup(func() { s.db = originalDB; _ = wrapped.Close() })
					var diagnostic bytes.Buffer
					previousLog := log.Writer()
					log.SetOutput(&diagnostic)
					t.Cleanup(func() { log.SetOutput(previousLog) })

					var built [][2]ObservationSnippet
					runner := &snippetRecordingRunner{}
					result, err := s.ScanProject(ScanOptions{Project: project, Apply: apply, Semantic: true, Concurrency: 1, MaxSemantic: 10, Runner: runner,
						BuildPrompt: func(a, b ObservationSnippet) string {
							built = append(built, [2]ObservationSnippet{a, b})
							return a.Content + " versus " + b.Content
						},
					})
					if err != nil {
						t.Fatalf("ScanProject: %v", err)
					}
					want := ScanResult{Project: project, Inspected: 3, RankedQueries: 3, CandidatesFound: 6, DryRun: !apply, SemanticJudged: 1}
					if !reflect.DeepEqual(result, want) {
						t.Errorf("result = %+v, want %+v", result, want)
					}
					healthy := make([]ObservationSnippet, 0, 2)
					for i, snippet := range snippets {
						if i != badIndex {
							healthy = append(healthy, snippet)
						}
					}
					if len(built) != 1 || built[0] != [2]ObservationSnippet{healthy[0], healthy[1]} {
						t.Errorf("builder received %+v, want only full healthy snippets %+v", built, healthy)
					}
					wantPrompts := []string{healthy[0].Content + " versus " + healthy[1].Content}
					if !reflect.DeepEqual(runner.prompts, wantPrompts) {
						t.Errorf("evaluator prompts = %q, want %q", runner.prompts, wantPrompts)
					}
					// The same bad ID fails in both orientations: no reverse pair may slip through.
					if fault.hits != 3 {
						t.Errorf("lookup faults = %d, want source and both reverse candidates (3)", fault.hits)
					}
					logs := diagnostic.String()
					for _, fragment := range []string{"[store] ScanProject: source snippet obs=" + fault.badID, "[store] ScanProject: candidate snippet obs=", "cand=" + fault.badID} {
						if !strings.Contains(logs, fragment) {
							t.Errorf("missing diagnostic %q in %q", fragment, logs)
						}
					}
					errorText := failure.Error()
					if failure == io.EOF {
						errorText = sql.ErrNoRows.Error()
					}
					if !strings.Contains(logs, errorText) {
						t.Errorf("missing Scan error %q in %q", errorText, logs)
					}
					for _, snippet := range snippets {
						for _, private := range []string{snippet.Title, snippet.Content} {
							if strings.Contains(logs, private) {
								t.Errorf("diagnostic leaked snippet: %q", logs)
							}
						}
					}
					relations, err := s.ListRelations(ListRelationsOptions{Project: project})
					if err != nil {
						t.Fatal(err)
					}
					wantRelations := 0
					if apply {
						wantRelations = 1
					}
					if len(relations) != wantRelations {
						t.Errorf("relations = %+v, want %d healthy only", relations, wantRelations)
					}
					for _, relation := range relations {
						if relation.SourceID != healthy[0].SyncID || relation.TargetID != healthy[1].SyncID {
							t.Errorf("unexpected pair persisted: %+v", relation)
						}
						full, err := s.GetRelation(relation.SyncID)
						if err != nil {
							t.Fatal(err)
						}
						if full.Relation != "compatible" || full.JudgmentStatus != "judged" ||
							full.Confidence == nil || *full.Confidence != 0.9 ||
							full.Reason == nil || *full.Reason != "synthetic healthy pair" ||
							full.MarkedByActor == nil || *full.MarkedByActor != "engram" ||
							full.MarkedByModel == nil || *full.MarkedByModel != "test" {
							t.Errorf("healthy judgment = %+v", full)
						}
					}
					after, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 1000)
					if err != nil {
						t.Fatal(err)
					}
					stateAfter, err := s.GetSyncState(DefaultSyncTargetKey)
					if err != nil {
						t.Fatal(err)
					}
					delta := 0
					if apply {
						delta = 1
					}
					if len(after) != len(before)+delta {
						t.Errorf("mutation count = %d, want %d", len(after), len(before)+delta)
					}
					if stateAfter.LastEnqueuedSeq != stateBefore.LastEnqueuedSeq+int64(delta) {
						t.Errorf("sync sequence = %d, want %d", stateAfter.LastEnqueuedSeq, stateBefore.LastEnqueuedSeq+int64(delta))
					}
					for _, mutation := range after {
						if mutation.Seq <= stateBefore.LastEnqueuedSeq {
							continue
						}
						if len(relations) != 1 || mutation.EntityKey != relations[0].SyncID || strings.Contains(mutation.Payload, fault.badID) {
							t.Errorf("unexpected scan mutation: %+v", mutation)
						}
					}
					if !apply && !reflect.DeepEqual(stateBefore, stateAfter) {
						t.Errorf("dry-run changed sync state: before=%+v after=%+v", stateBefore, stateAfter)
					}
				})
			}
		}
	}
}
