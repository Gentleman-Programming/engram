package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/project"
)

var managedTestHostA = ManagedRuntimeHost{PID: 20, Start: "birth-a", Domain: "local-domain"}
var managedTestHostB = ManagedRuntimeHost{PID: 21, Start: "birth-b", Domain: "local-domain"}

func TestManagedRuntimeObservationIntentPinsCanonicalPayloadAndCatalog(t *testing.T) {
	st := newTestStore(t)
	scope := ManagedRuntimeScope{
		Root: "root", EffectiveID: "root", Project: "project",
		Directory: project.RuntimeWorktreeDirectory(t.TempDir()), Mode: SessionOwnershipProjectOwned,
	}
	_, generation, err := st.AttachManagedRuntime(scope, managedTestHostA, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := copyValidateDB(st.db, false); err != nil {
		t.Fatalf("previous optional managed catalog: %v", err)
	}
	if _, err := st.db.Exec(managedRuntimeFingerprintMigration); err != nil {
		t.Fatal(err)
	}
	if err := copyValidateDB(st.db, false); err != nil {
		t.Fatalf("fingerprint-only managed catalog: %v", err)
	}
	original := AddObservationParams{Title: "Title", Content: "Visible <private>first secret</private>", Org: "test"}
	if err := st.RecordManagedRuntimeObservation(scope, generation, "save", original); err != nil {
		t.Fatal(err)
	}
	var staged int
	if err := st.db.QueryRow("SELECT count(*) FROM managed_runtime_writes WHERE operation_id='save' AND observation_payload IS NOT NULL").Scan(&staged); err != nil || staged != 1 {
		t.Fatalf("recorded save did not stage its payload: %d, %v", staged, err)
	}
	if err := copyValidateDB(st.db, false); err != nil {
		t.Fatalf("new optional managed catalog: %v", err)
	}
	changed := original
	changed.Content = "different"
	if err := st.RecordManagedRuntimeObservation(scope, generation, "save", changed); !errors.Is(err, ErrObservationOperationConflict) {
		t.Fatalf("changed input was accepted for an existing intent: %v", err)
	}
	retry := original
	retry.Content = "Visible <private>different secret</private>"
	if err := st.RecordManagedRuntimeObservation(scope, generation, "save", retry); err != nil {
		t.Fatalf("redacted data affected intent identity: %v", err)
	}
	retry.OperationID = "save"
	saved, err := st.SaveManagedRuntimeObservation(scope, generation, retry)
	if err != nil || saved.ID <= 0 {
		t.Fatalf("save: %+v, %v", saved, err)
	}
	row, err := st.GetObservation(saved.ID)
	if err != nil || strings.Contains(row.Content, "secret") {
		t.Fatalf("stored observation contains redacted content: %+v, %v", row, err)
	}
	if err := st.RecordManagedRuntimeWrite(scope, generation, "opaque"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordManagedRuntimeObservation(scope, generation, "blocked", original); !errors.Is(err, ErrManagedRuntimeWriteUnresolved) {
		t.Fatalf("existing opaque intent did not fence new admission: %v", err)
	}
	var retained int
	if err := st.db.QueryRow("SELECT count(*) FROM managed_runtime_writes WHERE observation_payload IS NOT NULL").Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("confirmed writes retained staged payloads: %d, %v", retained, err)
	}
}

func TestManagedRuntimeAttachRegistersAndAdmitsAtomically(t *testing.T) {
	st := newTestStore(t)
	scope := ManagedRuntimeScope{
		Root: "root", EffectiveID: "root", Project: "project",
		Directory: t.TempDir(), Mode: SessionOwnershipProjectOwned,
	}
	if err := st.EnrollProject(scope.Project); err != nil {
		t.Fatal(err)
	}
	effective, first, err := st.AttachManagedRuntime(scope, managedTestHostA, false)
	if err != nil || first <= 0 || effective != scope.Root {
		t.Fatalf("first attachment: %q %d, %v", effective, first, err)
	}
	if id, retry, err := st.AttachManagedRuntime(scope, managedTestHostA, false); err != nil || retry != first || id != effective {
		t.Fatalf("reattachment after lost acknowledgement: %q %d, %v", id, retry, err)
	}
	if err := st.RetireManagedRuntimeHost(scope, first, managedTestHostA); err != nil {
		t.Fatal(err)
	}
	if applied, err := st.CloseManagedRuntime(scope, first); err != nil || !applied {
		t.Fatalf("close root: %v, %v", applied, err)
	}
	successor := scope
	successor.EffectiveID = "root:resume:2"
	replacement := successor
	replacement.EffectiveID = "root:resume:3"
	unresolved := successor
	unresolved.EffectiveID = ""
	before, err := st.getSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AttachManagedRuntime(replacement, managedTestHostB, true); !errors.Is(err, ErrRuntimeSessionEffectiveIDMismatch) {
		t.Fatalf("unexpected continuation selection: %v", err)
	}
	if _, _, err := st.AttachManagedRuntime(unresolved, managedTestHostA, true); !errors.Is(err, ErrManagedRuntimeGeneration) {
		t.Fatalf("old host was able to attach to a later continuation: %v", err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER fixture_reject_attachment BEFORE INSERT ON managed_runtime_hosts
		BEGIN SELECT RAISE(ABORT, 'forced attachment failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AttachManagedRuntime(unresolved, managedTestHostB, true); err == nil || !strings.Contains(err.Error(), "forced attachment failure") {
		t.Fatalf("did not reach the host write after session registration: %v", err)
	}
	var rows, generations int
	if err := st.db.QueryRow("SELECT count(*) FROM sessions WHERE id<>?", scope.Root).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("refused attachment left a continuation: %d %v", rows, err)
	}
	if err := st.db.QueryRow("SELECT count(*) FROM managed_runtime_generations").Scan(&generations); err != nil || generations != 1 {
		t.Fatalf("refused attachment changed generations: %d %v", generations, err)
	}
	after, err := st.getSyncState(DefaultSyncTargetKey)
	if err != nil || before.LastEnqueuedSeq != after.LastEnqueuedSeq {
		t.Fatalf("refused attachment changed sync state: %+v %v", after, err)
	}
	if _, err := st.db.Exec("DROP TRIGGER fixture_reject_attachment"); err != nil {
		t.Fatal(err)
	}
	effective, next, err := st.AttachManagedRuntime(unresolved, managedTestHostB, true)
	if err != nil || next <= first || effective != successor.EffectiveID {
		t.Fatalf("continuation attachment: %q %d, %v", effective, next, err)
	}
	if id, retry, err := st.AttachManagedRuntime(unresolved, managedTestHostB, true); err != nil || retry != next || id != effective {
		t.Fatalf("lost unasserted attachment result: %q %d, %v", id, retry, err)
	}
	if _, _, err := st.AttachManagedRuntime(unresolved, managedTestHostA, true); !errors.Is(err, ErrManagedRuntimeGeneration) {
		t.Fatalf("old host was able to reattach to the active continuation: %v", err)
	}
	generation, hosts, err := st.ListManagedRuntimeHosts(successor)
	if err != nil || generation != next || len(hosts) != 1 || hosts[0] != managedTestHostB {
		t.Fatalf("expected exact persisted membership: %d %+v %v", generation, hosts, err)
	}
	if _, err := st.CloseManagedRuntime(successor, next); !errors.Is(err, ErrManagedRuntimeHostsRemain) {
		t.Fatalf("new host failed to fence close: %v", err)
	}
}

func TestManagedRuntimeScopedCloseRequiresManagedGeneration(t *testing.T) {
	st := newTestStore(t)
	scope := ManagedRuntimeScope{
		Root: "root", Project: "project", Directory: t.TempDir(),
		Mode: SessionOwnershipProjectOwned,
	}
	effective, generation, err := st.AttachManagedRuntime(scope, managedTestHostA, false)
	if err != nil {
		t.Fatal(err)
	}
	scope.EffectiveID = effective
	if _, err := st.EndRuntimeSessionIfCurrent(scope.Root, scope.Project, scope.Directory, scope.Mode, "", effective); !errors.Is(err, ErrManagedRuntimeCloseRequiresGeneration) {
		t.Fatalf("ordinary scoped close while host is attached: %v", err)
	}
	if err := st.RetireManagedRuntimeHost(scope, generation, managedTestHostA); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EndRuntimeSession(scope.Root, scope.Project, scope.Directory, scope.Mode, ""); !errors.Is(err, ErrManagedRuntimeCloseRequiresGeneration) {
		t.Fatalf("ordinary scoped close while awaiting managed closure: %v", err)
	}
	if session, err := st.GetSession(effective); err != nil || session.EndedAt != nil {
		t.Fatalf("managed session = %+v, %v", session, err)
	}
	if err := st.CreateSessionWithOwnershipMode("ordinary", scope.Project, scope.Directory, scope.Mode); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EndRuntimeSession("ordinary", scope.Project, scope.Directory, scope.Mode, ""); err != nil {
		t.Fatalf("ordinary scoped close on a separate session: %v", err)
	}
	if err := st.EndSession(effective, "manual exact close"); err != nil {
		t.Fatalf("manual exact close: %v", err)
	}
}

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
	sequence := func() int64 {
		t.Helper()
		state, err := st.getSyncState(DefaultSyncTargetKey)
		if err != nil {
			t.Fatal(err)
		}
		return state.LastEnqueuedSeq
	}
	beforeSave := sequence()
	if _, err := st.db.Exec(`CREATE TRIGGER fixture_fail_save BEFORE INSERT ON observation_save_operations
	BEGIN SELECT RAISE(ABORT,'forced save failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveManagedRuntimeObservation(scope, second, params); err == nil || !strings.Contains(err.Error(), "forced save failure") {
		t.Fatalf("unexpected write result: %v", err)
	}
	var observations int
	if err := st.db.QueryRow(`SELECT count(*) FROM observations WHERE session_id = ? AND topic_key = ?`, effective, params.TopicKey).Scan(&observations); err != nil || observations != 0 {
		t.Fatalf("failed save left observations: %d, %v", observations, err)
	}
	if after := sequence(); after != beforeSave {
		t.Fatalf("failed save advanced sync sequence: %d -> %d", beforeSave, after)
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
