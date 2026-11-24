package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestManagedRuntimeRecoveryPreservesAdmissionOrderAndPreparedContent(t *testing.T) {
	cfg := FallbackConfig(t.TempDir())
	cfg.MaxObservationLength = 4
	st, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	scope := ManagedRuntimeScope{
		Root: "root", EffectiveID: "root", Project: "project",
		Directory: project.RuntimeWorktreeDirectory(t.TempDir()), Mode: SessionOwnershipProjectOwned,
	}
	_, generation, err := st.AttachManagedRuntime(scope, managedTestHostA, false)
	if err != nil {
		t.Fatal(err)
	}
	first := AddObservationParams{Title: "First", Content: "abc☃<private>one-secret</private>", TopicKey: "topic"}
	second := AddObservationParams{Title: "Second", Content: "def☃<private>two-secret</private>", TopicKey: "topic"}
	if err := st.RecordManagedRuntimeObservation(scope, generation, "first", first); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordManagedRuntimeObservation(scope, generation, "second", second); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordManagedRuntimeWrite(scope, generation, "opaque"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordManagedRuntimeObservation(scope, generation, "blocked", second); !errors.Is(err, ErrManagedRuntimeWriteUnresolved) {
		t.Fatalf("accepted a new write behind an opaque pending intent: %v", err)
	}
	other := scope
	other.Root, other.EffectiveID = "other", "other"
	_, otherGeneration, err := st.AttachManagedRuntime(other, managedTestHostB, false)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := AddObservationParams{OperationID: "unrelated", Title: "Unrelated", Content: "other data"}
	if err := st.RecordManagedRuntimeObservation(other, otherGeneration, unrelated.OperationID, unrelated); err != nil {
		t.Fatalf("opaque intent affected a different cohort: %v", err)
	}
	if _, err := st.SaveManagedRuntimeObservation(other, otherGeneration, unrelated); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := st.db.QueryRow("SELECT observation_payload FROM managed_runtime_writes WHERE operation_id='second'").Scan(&payload); err != nil ||
		bytes.Contains(payload, []byte("two-secret")) {
		t.Fatalf("pending row contains unredacted content or could not be read: %v", err)
	}
	second.OperationID = "second"
	if _, err := st.SaveManagedRuntimeObservation(scope, generation, second); !errors.Is(err, ErrManagedRuntimeWriteUnresolved) {
		t.Fatalf("later write overtook an earlier pending save: %v", err)
	}
	if err := st.RetireManagedRuntimeHost(scope, generation, managedTestHostA); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.MaxObservationLength = 2 // Recovery uses the already prepared bytes.
	st, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER fixture_fail_confirm BEFORE UPDATE OF confirmed ON managed_runtime_writes
		BEGIN SELECT RAISE(ABORT,'forced recovery confirmation failure'); END`); err != nil {
		t.Fatal(err)
	}
	if applied, _, err := st.RecoverManagedRuntimeObservations(scope, generation); applied != 0 ||
		err == nil || !strings.Contains(err.Error(), "forced recovery confirmation failure") {
		t.Fatalf("failed recovery: applied=%d err=%v", applied, err)
	}
	if id, err := st.GetObservationSaveResult("first"); err != nil || id != 0 {
		t.Fatalf("failed recovery left a committed receipt: id=%d err=%v", id, err)
	}
	if _, err := st.db.Exec("DROP TRIGGER fixture_fail_confirm"); err != nil {
		t.Fatal(err)
	}
	if applied, more, err := st.RecoverManagedRuntimeObservations(scope, generation); err != nil || applied != 2 || more {
		t.Fatalf("recovery: applied=%d more=%v err=%v", applied, more, err)
	}
	id, err := st.GetObservationSaveResult("second")
	if err != nil || id <= 0 {
		t.Fatalf("missing committed result: %d %v", id, err)
	}
	row, err := st.GetObservation(id)
	if err != nil || row.Title != second.Title || row.Content != "def"+observationTruncationMarker {
		t.Fatalf("recovery changed order or re-truncated the prepared payload: %+v %v", row, err)
	}
	var retained int
	if err := st.db.QueryRow("SELECT count(*) FROM managed_runtime_writes WHERE observation_payload IS NOT NULL").Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("committed receipts retained %d staged payloads: %v", retained, err)
	}
	if _, err := st.CloseManagedRuntime(scope, generation); !errors.Is(err, ErrManagedRuntimeWriteUnresolved) {
		t.Fatalf("automatic recovery cleared an opaque pending intent: %v", err)
	}
	if _, err := st.SaveManagedRuntimeObservation(scope, generation, AddObservationParams{
		OperationID: "opaque", Title: "Explicit recovery", Content: "text",
	}); err != nil {
		t.Fatal(err)
	}
	if applied, err := st.CloseManagedRuntime(scope, generation); err != nil || !applied {
		t.Fatalf("explicit recovery did not release the close fence: applied=%v err=%v", applied, err)
	}
}

func TestManagedRuntimeRecoveryBatchesAndCoalescesConcurrentDrains(t *testing.T) {
	st := newTestStore(t)
	scope := ManagedRuntimeScope{
		Root: "root", EffectiveID: "root", Project: "project",
		Directory: project.RuntimeWorktreeDirectory(t.TempDir()), Mode: SessionOwnershipProjectOwned,
	}
	_, generation, err := st.AttachManagedRuntime(scope, managedTestHostA, false)
	if err != nil {
		t.Fatal(err)
	}
	count := managedRuntimeRecoveryBatch + 1
	for i := 1; i <= count; i++ {
		id := fmt.Sprintf("save-%d", i)
		params := AddObservationParams{Title: id, Content: id}
		if err := st.RecordManagedRuntimeObservation(scope, generation, id, params); err != nil {
			t.Fatal(err)
		}
	}
	if applied, more, err := st.RecoverManagedRuntimeObservations(scope, generation); err != nil || applied != count-1 || !more {
		t.Fatalf("first batch = %d more=%v err=%v", applied, more, err)
	}
	last := fmt.Sprintf("save-%d", count)
	if id, err := st.GetObservationSaveResult(last); err != nil || id != 0 {
		t.Fatalf("last operation left the batch early: %d, %v", id, err)
	}
	type result struct {
		applied int
		more    bool
		err     error
	}
	begin := make(chan struct{})
	results := make(chan result, 2)
	for range cap(results) {
		go func() {
			<-begin
			applied, more, err := st.RecoverManagedRuntimeObservations(scope, generation)
			results <- result{applied, more, err}
		}()
	}
	close(begin)
	var total int
	for range cap(results) {
		got := <-results
		if got.err != nil || got.more {
			t.Fatalf("last batch: %+v", got)
		}
		total += got.applied
	}
	if total != 1 {
		t.Fatalf("concurrent drains recovered %d writes, want 1", total)
	}
	if id, err := st.GetObservationSaveResult(last); err != nil || id <= 0 {
		t.Fatalf("last operation has no committed receipt: %d, %v", id, err)
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

func TestManagedRuntimeContinuationWaitsForEndedHostRetirement(t *testing.T) {
	for _, tryEarly := range []bool{true, false} {
		t.Run(fmt.Sprintf("early-attach-%t", tryEarly), func(t *testing.T) {
			st := newTestStore(t)
			scope := ManagedRuntimeScope{Root: "root", EffectiveID: "root", Project: "project",
				Directory: project.RuntimeWorktreeDirectory(t.TempDir()), Mode: SessionOwnershipProjectOwned}
			_, generation, err := st.AttachManagedRuntime(scope, managedTestHostA, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.RecordManagedRuntimeObservation(scope, generation, "old-operation",
				AddObservationParams{Title: "Pending", Content: "keep in the old scope"}); err != nil {
				t.Fatal(err)
			}
			if err := st.EndSession(scope.EffectiveID, "explicit close"); err != nil {
				t.Fatal(err)
			}
			next := scope
			next.EffectiveID = ""
			if tryEarly {
				id, gen, err := st.AttachManagedRuntime(next, managedTestHostB, true)
				if !errors.Is(err, ErrManagedRuntimeHostsRemain) || id != "" || gen != 0 {
					t.Fatalf("early attachment bypassed old host: %q generation=%d err=%v", id, gen, err)
				}
				if _, err := st.GetSession("root:resume:2"); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("rejected attachment left a continuation: %v", err)
				}
			}
			current, hosts, err := st.ListManagedRuntimeHosts(scope)
			if err != nil || current != generation || len(hosts) != 1 || hosts[0] != managedTestHostA {
				t.Fatalf("old roster changed: generation=%d hosts=%v err=%v", current, hosts, err)
			}
			if applied, err := st.CloseManagedRuntime(scope, generation); applied || !errors.Is(err, ErrManagedRuntimeHostsRemain) {
				t.Fatalf("external end bypassed host retirement: applied=%v err=%v", applied, err)
			}
			if err := st.RetireManagedRuntimeHost(scope, generation, managedTestHostA); err != nil {
				t.Fatalf("retire exact old host: %v", err)
			}
			if applied, err := st.CloseManagedRuntime(scope, generation); applied || !errors.Is(err, ErrManagedRuntimeScopeEnded) {
				t.Fatalf("external end acquired a managed close receipt: applied=%v err=%v", applied, err)
			}
			id, gen, err := st.AttachManagedRuntime(next, managedTestHostB, true)
			if err != nil || id != "root:resume:2" || gen <= generation {
				t.Fatalf("attachment after retirement: %q generation=%d err=%v", id, gen, err)
			}
			next.EffectiveID = id
			if err := st.RecordManagedRuntimeObservation(next, gen, "next-operation",
				AddObservationParams{Title: "New scope", Content: "can still save"}); err != nil {
				t.Fatal(err)
			}
			if applied, more, err := st.RecoverManagedRuntimeObservations(next, gen); applied != 1 || more || err != nil {
				t.Fatalf("new continuation recovery: applied=%d more=%v err=%v", applied, more, err)
			}
			var pending int
			if err := st.db.QueryRow(`SELECT count(*) FROM managed_runtime_writes
				WHERE operation_id='old-operation' AND effective_id='root' AND confirmed=0 AND observation_payload IS NOT NULL`).Scan(&pending); err != nil || pending != 1 {
				t.Fatalf("recovery changed the old pending write: count=%d err=%v", pending, err)
			}
		})
	}
}

func TestManagedRuntimeKeepsPriorContinuationAdmissions(t *testing.T) {
	st := newTestStore(t)
	checkInventory := func(want ...ManagedRuntimeScope) {
		t.Helper()
		got, err := st.ListManagedRuntimeScopes(t.Context())
		if err != nil || got == nil || !slices.Equal(got, want) {
			t.Fatalf("managed scope inventory: got %+v, want %+v (%v)", got, want, err)
		}
	}
	checkInventory()
	var tables int
	if err := st.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name LIKE 'managed_runtime_%'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("inventory initialized the optional catalog: %d %v", tables, err)
	}
	scope := ManagedRuntimeScope{Root: "root", EffectiveID: "root", Project: "project",
		Directory: project.RuntimeWorktreeDirectory(t.TempDir()), Mode: SessionOwnershipProjectOwned}
	_, oldGeneration, err := st.AttachManagedRuntime(scope, managedTestHostA, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordManagedRuntimeObservation(scope, oldGeneration, "old-pending",
		AddObservationParams{Title: "Pending", Content: "keep in the ended binding"}); err != nil {
		t.Fatal(err)
	}
	if err := st.EndSession(scope.EffectiveID, "explicit close"); err != nil {
		t.Fatal(err)
	}
	// The journal still needs to reconcile hosts after an external end.
	checkInventory(scope)
	if err := st.RetireManagedRuntimeHost(scope, oldGeneration, managedTestHostA); err != nil {
		t.Fatal(err)
	}
	next := scope
	next.EffectiveID = ""
	effective, generation, err := st.AttachManagedRuntime(next, managedTestHostB, true)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate a roster persisted before the earlier-host admission fence.
	if _, err := st.db.Exec("UPDATE managed_runtime_hosts SET retired=0 WHERE root=? AND effective_id=?", scope.Root, scope.EffectiveID); err != nil {
		t.Fatal(err)
	}
	if id, gen, err := st.AttachManagedRuntime(next, managedTestHostB, true); err != nil || id != effective || gen != generation {
		t.Fatalf("retry of an already admitted host changed: %q generation=%d err=%v", id, gen, err)
	}
	newPeer := ManagedRuntimeHost{PID: 22, Start: "birth-c", Domain: "local-domain"}
	id, peerGeneration, err := st.AttachManagedRuntime(next, newPeer, true)
	if err != nil || id != effective || peerGeneration <= generation {
		t.Fatalf("joining the current continuation changed: %q generation=%d err=%v", id, peerGeneration, err)
	}
	next.EffectiveID = effective
	other := ManagedRuntimeScope{Root: "other", EffectiveID: "other", Project: "project",
		Directory: scope.Directory, Mode: scope.Mode}
	if _, _, err := st.AttachManagedRuntime(other, managedTestHostA, false); err != nil {
		t.Fatal(err)
	}
	checkInventory(other, next)
	if _, _, err := st.ListManagedRuntimeHosts(scope); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("current host lookup accepted a historical binding: %v", err)
	}
	gen, hosts, err := st.ListManagedRuntimeRetirementHosts(scope)
	if err != nil || gen != oldGeneration || len(hosts) != 1 || hosts[0] != managedTestHostA {
		t.Fatalf("historical retirement lookup: generation=%d hosts=%+v err=%v", gen, hosts, err)
	}
	wrong := scope
	wrong.Project = "unrelated"
	if _, _, err := st.ListManagedRuntimeRetirementHosts(wrong); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("retirement lookup accepted a mismatched project: %v", err)
	}
	if err := st.RetireManagedRuntimeHost(scope, oldGeneration, managedTestHostB); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("retirement accepted a host from the other binding: %v", err)
	}
	if err := st.RetireManagedRuntimeHost(scope, oldGeneration, managedTestHostA); err != nil {
		t.Fatalf("historical host could not retire: %v", err)
	}
	if applied, err := st.CloseManagedRuntime(scope, oldGeneration); applied || !errors.Is(err, ErrManagedRuntimeGeneration) {
		t.Fatalf("retirement authorized stale managed close: %v %v", applied, err)
	}
	if applied, _, err := st.RecoverManagedRuntimeObservations(scope, oldGeneration); applied != 0 || !errors.Is(err, ErrManagedRuntimeGeneration) {
		t.Fatalf("retirement authorized stale recovery: %d %v", applied, err)
	}
	var pending bool
	if err := st.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM managed_runtime_writes
		WHERE operation_id='old-pending' AND effective_id='root' AND confirmed=0 AND observation_payload IS NOT NULL)`).Scan(&pending); err != nil || !pending {
		t.Fatalf("historical retirement changed the old pending write: %v %v", pending, err)
	}
	for _, host := range []ManagedRuntimeHost{managedTestHostB, newPeer} {
		if err := st.RetireManagedRuntimeHost(next, peerGeneration, host); err != nil {
			t.Fatal(err)
		}
	}
	if closed, err := st.CloseManagedRuntime(next, peerGeneration); !closed || err != nil {
		t.Fatalf("close current continuation: %v %v", closed, err)
	}
	checkInventory(other)
}

func TestManagedRuntimeScopeInventoryRejectsIncompleteCatalog(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.db.Exec("CREATE TABLE managed_runtime_generations (generation INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ListManagedRuntimeScopes(t.Context()); got != nil || !errors.Is(err, errManagedRuntimeSchemaIncomplete) {
		t.Fatalf("partial catalog looked like an empty inventory: %+v %v", got, err)
	}
}

func TestManagedRuntimeEndedGitSubdirectoryCanRetire(t *testing.T) {
	st := newTestStore(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required to verify the worktree scope")
	}
	checkout := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", checkout).CombinedOutput(); err != nil {
		t.Fatalf("initialize checkout: %v %s", err, out)
	}
	child := filepath.Join(checkout, "subdirectory")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	scope := ManagedRuntimeScope{Root: "root", EffectiveID: "root", Project: "project",
		Directory: project.RuntimeWorktreeDirectory(checkout), Mode: SessionOwnershipProjectOwned}
	if err := st.StartSessionWithOwnershipMode(scope.Root, scope.Project, child, scope.Mode); err != nil {
		t.Fatal(err)
	}
	_, generation, err := st.AttachManagedRuntime(scope, managedTestHostA, false)
	if err != nil {
		t.Fatalf("live admission from same worktree: %v", err)
	}
	row, err := st.GetSession(scope.Root)
	if err != nil || row.Directory == scope.Directory {
		t.Fatalf("fixture must preserve a stored subdirectory: %+v %v", row, err)
	}
	if err := st.EndSession(scope.Root, "explicit end"); err != nil {
		t.Fatal(err)
	}
	if err := st.RetireManagedRuntimeHost(scope, generation, managedTestHostA); err != nil {
		t.Fatalf("externally ended host in the same worktree could not retire: %v", err)
	}
}
