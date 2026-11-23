package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

var managedTestHostA = ManagedRuntimeHost{PID: 20, Start: "birth-a", Domain: "local-domain"}
var managedTestHostB = ManagedRuntimeHost{PID: 21, Start: "birth-b", Domain: "local-domain"}

func TestManagedRuntimeWriteAndClosureRollback(t *testing.T) {
	st := newTestStore(t)
	dir := t.TempDir()
	scope := ManagedRuntimeScope{Root: "root", EffectiveID: "root", Project: "project", Directory: dir, Mode: SessionOwnershipProjectOwned}
	if err := st.EnrollProject(scope.Project); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSessionWithOwnershipMode(scope.Root, scope.Project, scope.Directory, scope.Mode); err != nil {
		t.Fatal(err)
	}
	if err := st.EndSession(scope.Root, "old root"); err != nil {
		t.Fatal(err)
	}
	effective, err := st.ResumeSessionWithOwnershipMode(scope.Root, scope.Project, scope.Directory, scope.Mode)
	if err != nil {
		t.Fatal(err)
	}
	scope.EffectiveID = effective
	first, err := st.AdmitManagedRuntime(scope, managedTestHostA)
	if err != nil {
		t.Fatal(err)
	}
	alias := scope
	alias.Root = scope.EffectiveID
	if _, err := st.AdmitManagedRuntime(alias, managedTestHostB); !errors.Is(err, ErrRuntimeSessionScopeConflict) {
		t.Fatalf("effective session admitted as a competing root: %v", err)
	}
	if err := st.RecordManagedRuntimeWrite(scope, first, "save"); err != nil {
		t.Fatal(err)
	}
	second, err := st.AdmitManagedRuntime(scope, managedTestHostB)
	if err != nil || second != first+1 {
		t.Fatalf("generation was not advanced: %d %v", second, err)
	}
	if _, err := st.CloseManagedRuntime(scope, first); !errors.Is(err, ErrManagedRuntimeGeneration) {
		t.Fatalf("stale close: %v", err)
	}
	if _, err := st.CloseManagedRuntime(scope, second); !errors.Is(err, ErrManagedRuntimeHostsRemain) {
		t.Fatalf("live cohort was not preserved: %v", err)
	}
	for _, host := range []ManagedRuntimeHost{managedTestHostA, managedTestHostB} {
		if err := st.RetireManagedRuntimeHost(scope, second, host); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CloseManagedRuntime(scope, second); !errors.Is(err, ErrManagedRuntimeWriteUnresolved) {
		t.Fatalf("receipt absent: %v", err)
	}
	params := AddObservationParams{
		OperationID: "save", Title: "Managed write", Content: "fixture contents", Type: "manual", Scope: "project", TopicKey: "fixture-topic",
	}
	if _, err := st.db.Exec(`CREATE TRIGGER fixture_fail_save BEFORE INSERT ON observation_save_operations
	BEGIN SELECT RAISE(ABORT,'forced save failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveManagedRuntimeObservation(scope, second, params); err == nil {
		t.Fatal("expected failed write")
	}
	if _, err := st.CloseManagedRuntime(scope, second); !errors.Is(err, ErrManagedRuntimeWriteUnresolved) {
		t.Fatalf("rolled-back save released terminal gate: %v", err)
	}
	if _, err := st.db.Exec("DROP TRIGGER fixture_fail_save"); err != nil {
		t.Fatal(err)
	}
	saved, err := st.SaveManagedRuntimeObservation(scope, second, params)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSessionWithOwnershipMode("topic-move", scope.Project, scope.Directory, scope.Mode); err != nil {
		t.Fatal(err)
	}
	params.OperationID, params.SessionID, params.Project = "", "topic-move", scope.Project
	params.Content = "updated content"
	moved, err := st.AddObservationWithResult(params)
	if err != nil || moved.ID != saved.ID {
		t.Fatalf("expected the existing topic row to move: %+v %v", moved, err)
	}
	movedRow, err := st.GetObservation(saved.ID)
	if err != nil || movedRow.SessionID != "topic-move" {
		t.Fatalf("topic row did not move: %+v %v", movedRow, err)
	}
	if _, err := st.db.Exec("UPDATE sessions SET summary='retained summary' WHERE id=?", effective); err != nil {
		t.Fatal(err)
	}
	sequence := func() int64 {
		t.Helper()
		state, err := st.getSyncState(DefaultSyncTargetKey)
		if err != nil {
			t.Fatal(err)
		}
		return state.LastEnqueuedSeq
	}
	before := sequence()
	if _, err := st.db.Exec(`CREATE TRIGGER fixture_interrupt BEFORE UPDATE ON managed_runtime_generations
	BEGIN SELECT RAISE(ABORT,'forced terminal receipt failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CloseManagedRuntime(scope, second); err == nil || !strings.Contains(err.Error(), "forced terminal receipt failure") {
		t.Fatalf("unexpected write result: %v", err)
	}
	var promoted bool
	if err := st.db.QueryRow("SELECT confirmed FROM managed_runtime_writes WHERE operation_id='save'").Scan(&promoted); err != nil || !promoted {
		t.Fatalf("previous write confirmation changed: %v %v", promoted, err)
	}
	row, err := st.GetSession(effective)
	if err != nil || row.EndedAt != nil || sequence() != before {
		t.Fatalf("session or journal escaped rollback: %+v %v", row, err)
	}
	if _, err := st.db.Exec("DROP TRIGGER fixture_interrupt"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CloseManagedRuntime(scope, second); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CloseManagedRuntime(scope, second); err != nil || sequence() != before+1 {
		t.Fatalf("repeated close changed the journal: %v", err)
	}
	row, err = st.GetSession(effective)
	if err != nil || row.EndedAt == nil || row.Summary == nil || *row.Summary != "retained summary" {
		t.Fatalf("managed closure did not preserve the summary: %+v %v", row, err)
	}
}

func TestMigrateCopyManagedRuntimeReceipts(t *testing.T) {
	parent := copyTempDir(t)
	source := filepath.Join(parent, "source")
	st, err := New(FallbackConfig(source))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	scope := ManagedRuntimeScope{Root: "root", EffectiveID: "root", Project: "project", Directory: parent, Mode: SessionOwnershipShared}
	if err := st.CreateSessionWithOwnershipMode(scope.Root, scope.Project, scope.Directory, scope.Mode); err != nil {
		t.Fatal(err)
	}
	generation, err := st.AdmitManagedRuntime(scope, managedTestHostA)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RetireManagedRuntimeHost(scope, generation, managedTestHostA); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CloseManagedRuntime(scope, generation); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "complete")
	if _, err := MigrateCopy(source, output, true); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(FallbackConfig(output))
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := reopened.CloseManagedRuntime(scope, generation); err != nil || applied {
		_ = reopened.Close()
		t.Fatalf("closed receipt was not preserved: %v", err)
	}
	if _, err := reopened.db.Exec("DROP TABLE managed_runtime_writes"); err != nil {
		_ = reopened.Close()
		t.Fatal(err)
	}
	scope.Root, scope.EffectiveID = "new-live", "new-live"
	if err := reopened.CreateSessionWithOwnershipMode(scope.Root, scope.Project, scope.Directory, scope.Mode); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.AdmitManagedRuntime(scope, managedTestHostA); !errors.Is(err, errManagedRuntimeSchemaIncomplete) {
		t.Fatalf("partial managed schema was repaired silently: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateCopy(output, filepath.Join(parent, "incomplete"), true); err == nil || !strings.Contains(err.Error(), "unsupported database catalog") {
		t.Fatalf("partial managed schema was accepted: %v", err)
	}
}

func TestManagedRuntimeInvalidatesDeletedGeneration(t *testing.T) {
	st := newTestStore(t)
	scope := ManagedRuntimeScope{Root: "root", EffectiveID: "root", Project: "project", Directory: t.TempDir(), Mode: SessionOwnershipProjectOwned}
	create := func() {
		t.Helper()
		if err := st.CreateSessionWithOwnershipMode(scope.Root, scope.Project, scope.Directory, scope.Mode); err != nil {
			t.Fatal(err)
		}
	}
	create()
	old, err := st.AdmitManagedRuntime(scope, managedTestHostA)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSession(scope.Root); err != nil {
		t.Fatal(err)
	}
	create()
	next, err := st.AdmitManagedRuntime(scope, managedTestHostA)
	if err != nil || next <= old {
		t.Fatalf("new admission reused a generation: %d %v", next, err)
	}
	if _, err := st.CloseManagedRuntime(scope, old); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted receipt can target replacement: %v", err)
	}
	row, err := st.GetSession(scope.Root)
	if err != nil || row.EndedAt != nil {
		t.Fatalf("replacement was closed: %+v %v", row, err)
	}
}

func TestManagedRuntimeRejectsGenericOperationReplay(t *testing.T) {
	st := newTestStore(t)
	scope := ManagedRuntimeScope{Root: "root", EffectiveID: "root", Project: "project", Directory: t.TempDir(), Mode: SessionOwnershipProjectOwned}
	if err := st.CreateSessionWithOwnershipMode(scope.Root, scope.Project, scope.Directory, scope.Mode); err != nil {
		t.Fatal(err)
	}
	generation, err := st.AdmitManagedRuntime(scope, managedTestHostA)
	if err != nil {
		t.Fatal(err)
	}
	const operation = "ordinary-save-with-same-id"
	if err := st.RecordManagedRuntimeWrite(scope, generation, operation); err != nil {
		t.Fatal(err)
	}
	params := AddObservationParams{OperationID: operation, SessionID: scope.EffectiveID, Project: scope.Project, Title: "Same request", Content: "Fixture"}
	if _, err := st.AddObservationWithResult(params); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveManagedRuntimeObservation(scope, generation, params); !errors.Is(err, ErrObservationOperationConflict) {
		t.Fatalf("ordinary save was accepted as a managed replay: %v", err)
	}
	if err := st.RetireManagedRuntimeHost(scope, generation, managedTestHostA); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CloseManagedRuntime(scope, generation); !errors.Is(err, ErrManagedRuntimeWriteUnresolved) {
		t.Fatalf("ordinary save cleared the managed write gate: %v", err)
	}
}

func TestManagedRuntimeRecoversHostsAfterReopen(t *testing.T) {
	cfg := FallbackConfig(t.TempDir())
	st, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	scope := ManagedRuntimeScope{Root: "cohort", EffectiveID: "cohort", Project: "project", Directory: t.TempDir(), Mode: SessionOwnershipProjectOwned}
	if err := st.CreateSessionWithOwnershipMode(scope.Root, scope.Project, scope.Directory, scope.Mode); err != nil {
		t.Fatal(err)
	}
	first, err := st.AdmitManagedRuntime(scope, managedTestHostA)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.AdmitManagedRuntime(scope, managedTestHostB)
	if err != nil || second <= first {
		t.Fatalf("second generation=%d: %v", second, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	generation, hosts, err := st.ListManagedRuntimeHosts(scope)
	if err != nil || generation != second || len(hosts) != 2 || hosts[0] != managedTestHostA || hosts[1] != managedTestHostB {
		t.Fatalf("after reopen, generation=%d, hosts=%+v: %v", generation, hosts, err)
	}
	retry, err := st.AdmitManagedRuntime(scope, managedTestHostA)
	if err != nil || retry != second {
		t.Fatalf("admission replay changed generation: %d %v", retry, err)
	}
	if err := st.RetireManagedRuntimeHost(scope, second, managedTestHostB); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CloseManagedRuntime(scope, second); !errors.Is(err, ErrManagedRuntimeHostsRemain) {
		t.Fatalf("outstanding oldest host did not fence close: %v", err)
	}
	if err := st.RetireManagedRuntimeHost(scope, second, managedTestHostA); err != nil {
		t.Fatal(err)
	}
	if applied, err := st.CloseManagedRuntime(scope, second); err != nil || !applied {
		t.Fatalf("terminal close: %v %v", applied, err)
	}
}
