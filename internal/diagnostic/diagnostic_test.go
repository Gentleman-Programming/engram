package diagnostic

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/internal/store"
	_ "modernc.org/sqlite"
)

func newDiagnosticTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, _ := newDiagnosticTestStoreWithConfig(t)
	return s
}

func newDiagnosticTestStoreWithConfig(t *testing.T) (*store.Store, store.Config) {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, cfg
}

func seedDiagnosticPendingMutation(t *testing.T, dataDir, project, entity, entityKey, op, payload string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "engram.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		store.DefaultSyncTargetKey, entity, entityKey, op, payload, store.SyncSourceLocal, project,
	); err != nil {
		t.Fatalf("insert sync mutation %q: %v", entityKey, err)
	}
}

func TestSQLiteLockContentionBranches(t *testing.T) {
	s := newDiagnosticTestStore(t)
	tests := []struct {
		name       string
		snapshot   store.SQLiteLockSnapshot
		probeErr   error
		wantStatus string
		wantReason string
	}{
		{
			name:       "healthy snapshot is ok",
			snapshot:   store.SQLiteLockSnapshot{JournalMode: "wal", BusyTimeoutMS: 5000, CheckpointBusy: 0, CheckpointLog: 2, CheckpointedFrames: 2},
			wantStatus: StatusOK,
			wantReason: CheckSQLiteLockContention + "_ok",
		},
		{
			name:       "checkpoint busy is warning",
			snapshot:   store.SQLiteLockSnapshot{JournalMode: "wal", BusyTimeoutMS: 5000, CheckpointBusy: 3, CheckpointLog: 7, CheckpointedFrames: 4},
			wantStatus: StatusWarning,
			wantReason: "sqlite_lock_contention_detected",
		},
		{
			name:       "probe failure is error",
			probeErr:   errors.New("probe unavailable"),
			wantStatus: StatusError,
			wantReason: "sqlite_lock_probe_failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report, err := NewRunner().RunOne(context.Background(), Scope{
				Store:   s,
				Project: "engram",
				ReadSQLiteLockSnapshot: func(context.Context) (store.SQLiteLockSnapshot, error) {
					return tc.snapshot, tc.probeErr
				},
			}, CheckSQLiteLockContention)
			if err != nil {
				t.Fatalf("RunOne: %v", err)
			}
			if report.Status != tc.wantStatus || report.Checks[0].ReasonCode != tc.wantReason {
				t.Fatalf("status=%s reason=%s report=%+v", report.Status, report.Checks[0].ReasonCode, report)
			}
		})
	}
}

func TestRegistryLookupAndOrdering(t *testing.T) {
	codes := RegisteredCodes()
	want := []string{CheckInvalidSessionIdentity, CheckManualSessionNameProjectMismatch, CheckSessionProjectDirectoryMismatch, CheckSQLiteLockContention, CheckSyncMutationRequiredFields, CheckUnownedSessionProject}
	if strings.Join(codes, ",") != strings.Join(want, ",") {
		t.Fatalf("RegisteredCodes = %v, want %v", codes, want)
	}
	if _, err := DefaultRegistry().Lookup("not_real"); err == nil {
		t.Fatal("expected invalid check error")
	}
}

func TestRunnerRollsUpBlockedFindings(t *testing.T) {
	s := newDiagnosticTestStore(t)
	runner := NewRunnerWithRegistry(NewRegistry(fakeBlockedCheck{}))
	report, err := runner.RunOne(context.Background(), Scope{Store: s, Project: "engram", Now: time.Now()}, "fake_blocked")
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusBlocked || report.Summary.Blocked != 1 {
		t.Fatalf("status=%s summary=%+v", report.Status, report.Summary)
	}
	if got := report.Checks[0].Findings[0].ReasonCode; got != "fake_blocked_reason" {
		t.Fatalf("reason_code=%q", got)
	}
}

type fakeBlockedCheck struct{}

func (fakeBlockedCheck) Code() string { return "fake_blocked" }
func (fakeBlockedCheck) Run(context.Context, Scope) (CheckResult, error) {
	return resultFromFindings("fake_blocked", map[string]any{"evaluated": true}, []Finding{{CheckID: "fake_blocked", Severity: SeverityBlocking, ReasonCode: "fake_blocked_reason", Message: "blocked", Why: "test", Evidence: mustJSON(map[string]any{"ok": false}), SafeNextStep: "none"}}), nil
}

func TestSessionProjectDirectoryMismatchFinding(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.CreateSession("s1", "api", "/work/web"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	report, err := NewRunner().RunOne(context.Background(), Scope{
		Store:   s,
		Project: "api",
		DetectProject: func(dir string) (DetectedProject, bool) {
			if dir == "/work/web" {
				return DetectedProject{Project: "web", Source: "test", Path: dir}, true
			}
			return DetectedProject{}, false
		},
	}, CheckSessionProjectDirectoryMismatch)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusWarning || len(report.Checks[0].Findings) != 1 {
		t.Fatalf("report=%+v", report)
	}
}

// TestSyncMutationRequiredFieldsSurfacesNonEnrolledCountFailure proves the
// check fails loudly instead of reporting a clean bill of health when the
// enrollment evidence cannot be read. The enrollment table is dropped after
// migrations so payload validation still succeeds and only the cloud sync
// enrollment lookup fails.
func TestSyncMutationRequiredFieldsSurfacesNonEnrolledCountFailure(t *testing.T) {
	s, cfg := newDiagnosticTestStoreWithConfig(t)

	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE sync_enrolled_projects`); err != nil {
		db.Close()
		t.Fatalf("drop sync_enrolled_projects: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close probe db: %v", err)
	}

	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s, Project: "engram"}, CheckSyncMutationRequiredFields)
	if err == nil {
		t.Fatalf("expected non-enrolled count failure, got report=%+v", report)
	}
	if !strings.Contains(err.Error(), "sync_enrolled_projects") {
		t.Fatalf("expected error naming the enrollment table, got %v", err)
	}

	errReport := ErrorReport("engram", err)
	if errReport.Status != StatusError || errReport.Summary.Errors != 1 {
		t.Fatalf("expected error report, got %+v", errReport)
	}
	if errReport.Checks[0].ReasonCode != "diagnostic_error" || !strings.Contains(errReport.Checks[0].Message, "sync_enrolled_projects") {
		t.Fatalf("expected surfaced query failure, got %+v", errReport.Checks[0])
	}
}

// TestSyncMutationRequiredFieldsIgnoresBacklogWithoutCloudEnrollment proves a
// local-only install is never reported as blocked for a non-enrolled backlog.
// The store journals sync mutations unconditionally, so on a device that never
// opted into cloud sync every pending mutation belongs to a non-enrolled
// project: that is the normal steady state, not a fault.
func TestSyncMutationRequiredFieldsIgnoresBacklogWithoutCloudEnrollment(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.CreateSession("manual-save-engram", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	pending, err := s.CountPendingNonEnrolledSyncMutations(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("CountPendingNonEnrolledSyncMutations: %v", err)
	}
	if len(pending) == 0 {
		t.Fatal("fixture must journal a non-enrolled pending mutation")
	}

	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s, Project: "engram"}, CheckSyncMutationRequiredFields)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusOK || len(report.Checks[0].Findings) != 0 {
		t.Fatalf("local-only install must not be blocked, got %+v", report)
	}
}

// TestSyncMutationRequiredFieldsBlocksNonEnrolledBacklogWhenCloudSyncInUse
// proves the issue #688 signal survives: once the device uses cloud sync, a
// project whose pending mutations cannot be delivered is reported as blocked
// with the enrollment guidance, while the enrolled project stays silent.
func TestSyncMutationRequiredFieldsBlocksNonEnrolledBacklogWhenCloudSyncInUse(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.CreateSession("manual-save-enrolled", "enrolled", "/work/enrolled"); err != nil {
		t.Fatalf("CreateSession enrolled: %v", err)
	}
	if err := s.EnrollProject("enrolled"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	if err := s.CreateSession("manual-save-local", "local", "/work/local"); err != nil {
		t.Fatalf("CreateSession local: %v", err)
	}

	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s}, CheckSyncMutationRequiredFields)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusBlocked || len(report.Checks[0].Findings) != 1 {
		t.Fatalf("expected one blocking finding, got %+v", report)
	}
	finding := report.Checks[0].Findings[0]
	if finding.Severity != SeverityBlocking || finding.ReasonCode != constants.ReasonNonEnrolledPendingMutations {
		t.Fatalf("unexpected finding: %+v", finding)
	}
	if !strings.Contains(string(finding.Evidence), `"project":"local"`) {
		t.Fatalf("expected the non-enrolled project in evidence, got %s", finding.Evidence)
	}
	if !strings.Contains(finding.SafeNextStep, "engram cloud enroll <project>") {
		t.Fatalf("expected enrollment guidance, got %q", finding.SafeNextStep)
	}
}

func TestRunnerRunAllHealthyEvaluatesEveryMVPCheck(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.CreateSession("manual-save-engram", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	report, err := NewRunner().RunAll(context.Background(), Scope{
		Store:   s,
		Project: "engram",
		ReadSQLiteLockSnapshot: func(context.Context) (store.SQLiteLockSnapshot, error) {
			return store.SQLiteLockSnapshot{JournalMode: "wal", BusyTimeoutMS: 5000, CheckpointBusy: 0}, nil
		},
	})
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	registered := len(RegisteredCodes())
	if report.Status != StatusOK || report.Summary.OK != registered || len(report.Checks) != registered {
		t.Fatalf("report=%+v, want %d ok checks", report, registered)
	}
	for _, check := range report.Checks {
		if check.Result != StatusOK || len(check.Evidence) == 0 {
			t.Fatalf("expected ok check with evidence, got %+v", check)
		}
	}
}

func TestInvalidSessionIdentityCheckReportsSourceReferencesAndJournal(t *testing.T) {
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO sessions (id, project, directory) VALUES ('', 'engram', '/tmp/engram');
		INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
		VALUES ('obs-empty-session', '', 'bugfix', 'title', 'content', 'engram', 'project', 'hash', 1, 1, datetime('now'), datetime('now'));
		INSERT INTO user_prompts (sync_id, session_id, content, project, created_at) VALUES ('prompt-empty-session', '', 'prompt', 'engram', datetime('now'));
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES ('cloud', 'session', '', 'upsert', '{"id":"","project":"engram","directory":"/tmp/engram"}', 'local', 'engram');`); err != nil {
		t.Fatalf("seed corrupt identity: %v", err)
	}

	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s, Project: "engram"}, CheckInvalidSessionIdentity)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusBlocked || len(report.Checks[0].Findings) != 1 {
		t.Fatalf("report=%+v", report)
	}
	var evidence store.InvalidSessionIdentityEvidence
	if err := json.Unmarshal(report.Checks[0].Findings[0].Evidence, &evidence); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if evidence.ObservationCount != 1 || evidence.PromptCount != 1 || evidence.InvalidJournalCount != 1 {
		t.Fatalf("evidence=%+v", evidence)
	}

	plan, err := BuildRepairPlan(context.Background(), Scope{Store: s, Project: "engram"}, report, CheckInvalidSessionIdentity, RepairModeApply)
	if err != nil {
		t.Fatalf("BuildRepairPlan: %v", err)
	}
	if plan.Status != "noop" || len(plan.Actions) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].ReasonCode != "cannot_repair_without_explicit_canonical_session_id" {
		t.Fatalf("repair plan=%+v", plan)
	}
}

func TestInvalidSessionIdentityEvidenceAttributesOnlyMatchingJournalMutations(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if _, err := s.DB().Exec(`
		INSERT INTO sessions (id, project, directory) VALUES ('', 'engram', '/tmp/empty');
		INSERT INTO sessions (id, project, directory) VALUES (' ', 'engram', '/tmp/space');
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES
			('cloud', 'session', '', 'upsert', 'not json', 'local', 'engram'),
			('cloud', 'session', 'valid-key', 'upsert', '{"id":"","directory":"/tmp"}', 'local', 'engram'),
			('cloud', 'session', ' ', 'upsert', '{"id":"other","directory":"/tmp"}', 'local', 'engram'),
			('cloud', 'session', 'other', 'upsert', '{"id":" ","directory":"/tmp"}', 'local', 'engram'),
			('cloud', 'session', 'unrelated', 'upsert', '{"id":"different","directory":"/tmp"}', 'local', 'engram');
	`); err != nil {
		t.Fatalf("seed invalid session journal: %v", err)
	}

	evidence, err := s.ListInvalidSessionIdentityEvidence("engram")
	if err != nil {
		t.Fatalf("ListInvalidSessionIdentityEvidence: %v", err)
	}
	counts := make(map[string]int64, len(evidence))
	for _, item := range evidence {
		counts[item.SessionID] = item.InvalidJournalCount
	}
	if counts[""] != 2 || counts[" "] != 2 {
		t.Fatalf("invalid journal counts=%v, want empty=2 whitespace=2", counts)
	}
}

func TestInvalidSessionIdentityEvidenceDoesNotAttributeUnmatchedWhitespaceIdentities(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if _, err := s.DB().Exec(`
		INSERT INTO sessions (id, project, directory) VALUES ('', 'engram', '/tmp/empty');
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		VALUES ('cloud', 'session', char(9), 'upsert', '{"id":"\n","directory":"/tmp"}', 'local', 'engram');
	`); err != nil {
		t.Fatalf("seed unmatched whitespace journal: %v", err)
	}

	evidence, err := s.ListInvalidSessionIdentityEvidence("engram")
	if err != nil {
		t.Fatalf("ListInvalidSessionIdentityEvidence: %v", err)
	}
	if len(evidence) != 1 || evidence[0].InvalidJournalCount != 0 {
		t.Fatalf("evidence=%+v, want one unassigned empty-session mutation", evidence)
	}
}

func TestInvalidSessionIdentityEvidenceDoesNotAttributeMalformedPayloadWithoutExactKey(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if _, err := s.DB().Exec(`
		INSERT INTO sessions (id, project, directory) VALUES ('', 'engram', '/tmp/empty');
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		VALUES ('cloud', 'session', 'nonmatching-key', 'upsert', 'not json', 'local', 'engram');
	`); err != nil {
		t.Fatalf("seed malformed session journal: %v", err)
	}

	evidence, err := s.ListInvalidSessionIdentityEvidence("engram")
	if err != nil {
		t.Fatalf("ListInvalidSessionIdentityEvidence: %v", err)
	}
	if len(evidence) != 1 || evidence[0].InvalidJournalCount != 0 {
		t.Fatalf("evidence=%+v, want one unassigned malformed mutation", evidence)
	}
}

// TestInvalidSessionIdentityCheckReportsQuarantinedPulledSessions proves the
// pull-side skip is not silent: a historical chunk carrying a blank session
// identity is skipped so the cursor can advance, and doctor must still report
// the quarantined mutation as evidence.
func TestInvalidSessionIdentityCheckReportsQuarantinedPulledSessions(t *testing.T) {
	s := newDiagnosticTestStore(t)
	mutation := store.SyncMutation{
		Seq:       7,
		Entity:    store.SyncEntitySession,
		EntityKey: "\t",
		Op:        store.SyncOpUpsert,
		Payload:   `{"id":"","project":"engram","directory":"/remote"}`,
	}
	if err := s.ApplyPulledMutation(store.DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("ApplyPulledMutation: %v", err)
	}

	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s, Project: "engram"}, CheckInvalidSessionIdentity)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if len(report.Checks) != 1 || len(report.Checks[0].Findings) != 1 {
		t.Fatalf("report=%+v, want one quarantined finding", report)
	}
	finding := report.Checks[0].Findings[0]
	if finding.ReasonCode != "quarantined_pulled_session_identity" {
		t.Fatalf("finding reason code=%q", finding.ReasonCode)
	}
	var evidence store.QuarantinedPulledSessionEvidence
	if err := json.Unmarshal(finding.Evidence, &evidence); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if evidence.RemoteSeq != 7 || evidence.EntityKey != "\t" || evidence.TargetKey != store.DefaultSyncTargetKey || evidence.Project != "engram" {
		t.Fatalf("evidence=%+v", evidence)
	}

	var details map[string]any
	if err := json.Unmarshal(report.Checks[0].Evidence, &details); err != nil {
		t.Fatalf("decode check evidence: %v", err)
	}
	if details["finding_count"] != float64(1) {
		t.Fatalf("check evidence=%v", details)
	}
}

// TestInvalidSessionIdentityCheckReportsEveryQuarantinedPulledSession proves the
// doctor surface scales with the number of dropped mutations. A chunk carrying
// several blank identities must produce one finding per dropped mutation: the
// quarantine rows are the only record that remote data was discarded, so a
// report that collapses them would hide part of the loss it exists to expose.
func TestInvalidSessionIdentityCheckReportsEveryQuarantinedPulledSession(t *testing.T) {
	s := newDiagnosticTestStore(t)
	mutations := []store.SyncMutation{
		{Entity: store.SyncEntitySession, EntityKey: "\t", Op: store.SyncOpUpsert, Payload: `{"id":"","project":"engram","directory":"/first"}`},
		{Entity: store.SyncEntitySession, EntityKey: "\n", Op: store.SyncOpUpsert, Payload: `{"id":"","project":"engram","directory":"/second"}`},
	}
	if err := s.ApplyPulledChunk(store.DefaultSyncTargetKey, "blank-identities", mutations); err != nil {
		t.Fatalf("ApplyPulledChunk: %v", err)
	}

	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s, Project: "engram"}, CheckInvalidSessionIdentity)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if len(report.Checks) != 1 || len(report.Checks[0].Findings) != 2 {
		t.Fatalf("report=%+v, want one finding per dropped mutation", report)
	}
	seen := map[string]string{}
	for _, finding := range report.Checks[0].Findings {
		var evidence store.QuarantinedPulledSessionEvidence
		if err := json.Unmarshal(finding.Evidence, &evidence); err != nil {
			t.Fatalf("decode evidence: %v", err)
		}
		if previous, duplicated := seen[evidence.SyncID]; duplicated {
			t.Fatalf("sync_id %q reported twice (%q and %q)", evidence.SyncID, previous, evidence.EntityKey)
		}
		seen[evidence.SyncID] = evidence.EntityKey
	}
	if len(seen) != 2 {
		t.Fatalf("distinct quarantined sync ids=%v, want 2", seen)
	}
}

// TestRepairPlanReportsQuarantinedPulledSessionsAsSkipped keeps repair honest:
// doctor reports the quarantined mutation, so the repair plan must name it as
// unrepairable instead of returning a bare noop.
func TestRepairPlanReportsQuarantinedPulledSessionsAsSkipped(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.ApplyPulledMutation(store.DefaultSyncTargetKey, store.SyncMutation{
		Seq:       3,
		Entity:    store.SyncEntitySession,
		EntityKey: "\n",
		Op:        store.SyncOpUpsert,
		Payload:   `{"id":"","project":"engram","directory":"/remote"}`,
	}); err != nil {
		t.Fatalf("ApplyPulledMutation: %v", err)
	}
	scope := Scope{Store: s, Project: "engram"}
	report, err := NewRunner().RunOne(context.Background(), scope, CheckInvalidSessionIdentity)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	plan, err := BuildRepairPlan(context.Background(), scope, report, CheckInvalidSessionIdentity, RepairModeApply)
	if err != nil {
		t.Fatalf("BuildRepairPlan: %v", err)
	}
	if plan.Status != "noop" || len(plan.Actions) != 0 || len(plan.Skipped) != 1 {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.Skipped[0].ReasonCode != ReasonQuarantinedPulledSessionIdentity {
		t.Fatalf("skip reason=%q", plan.Skipped[0].ReasonCode)
	}
}

// TestSyncMutationRequiredFieldsSeparatesQuarantinedEvidenceFromBlockingWork
// exercises the cloud-enrolled case on purpose: `engram` is enrolled so the
// check runs past the cloud-sync gate, proving quarantined rows are reported as
// non-blocking evidence on the very path that still evaluates delivery faults.
func TestSyncMutationRequiredFieldsSeparatesQuarantinedEvidenceFromBlockingWork(t *testing.T) {
	s, cfg := newDiagnosticTestStoreWithConfig(t)
	dataDir := cfg.DataDir
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	seedDiagnosticPendingMutation(t, dataDir, "engram", store.SyncEntitySession, "poison", store.SyncOpUpsert, `{"id":"poison"}`)

	runCheck := func(stage string) Report {
		t.Helper()
		report, err := NewRunner().RunOne(context.Background(), Scope{Store: s, Project: "engram"}, CheckSyncMutationRequiredFields)
		if err != nil {
			t.Fatalf("RunOne %s: %v", stage, err)
		}
		return report
	}

	if report := runCheck("before quarantine"); report.Status != StatusBlocked {
		t.Fatalf("expected blocked report before quarantine, got %+v", report)
	}

	quarantine, err := s.QuarantineIrreparableSyncMutations("engram", true)
	if err != nil || len(quarantine.Actions) != 1 {
		t.Fatalf("quarantine report=%+v err=%v", quarantine, err)
	}

	report := runCheck("after quarantine")
	if report.Status == StatusBlocked || report.Summary.Blocked != 0 {
		t.Fatalf("quarantined mutation still blocks doctor: %+v", report)
	}
	check := report.Checks[0]
	if check.Result == StatusBlocked || check.Severity == SeverityBlocking {
		t.Fatalf("quarantined mutation still blocks the check: %+v", check)
	}
	if len(check.Findings) != 1 {
		t.Fatalf("expected the quarantined row to stay visible as evidence, got %+v", check.Findings)
	}
	finding := check.Findings[0]
	if finding.Severity != SeverityInfo || finding.ReasonCode != "sync_mutation_quarantined" || finding.RequiresConfirmation {
		t.Fatalf("unexpected quarantined finding: %+v", finding)
	}
	var evidence map[string]any
	if err := json.Unmarshal(finding.Evidence, &evidence); err != nil {
		t.Fatalf("finding evidence invalid: %v", err)
	}
	if evidence["entity_key"] != "poison" || evidence["disposition"] != store.SyncMutationDispositionQuarantined {
		t.Fatalf("quarantined evidence lost mutation identity: %v", evidence)
	}
	if reason, _ := evidence["disposition_reason"].(string); strings.TrimSpace(reason) == "" {
		t.Fatalf("quarantined evidence lost the disposition reason: %v", evidence)
	}

	seedDiagnosticPendingMutation(t, dataDir, "engram", store.SyncEntityObservation, "obs-missing", store.SyncOpUpsert, `{"sync_id":"obs-missing"}`)
	report = runCheck("with new blocking work")
	if report.Status != StatusBlocked {
		t.Fatalf("quarantined evidence masked genuinely blocking work: %+v", report)
	}
	check = report.Checks[0]
	if len(check.Findings) != 2 {
		t.Fatalf("expected blocking and quarantined findings, got %+v", check.Findings)
	}
	if check.Findings[0].Severity != SeverityBlocking || check.Findings[0].ReasonCode != "sync_mutation_payload_missing_required_fields" {
		t.Fatalf("blocking finding must lead the roll-up: %+v", check.Findings[0])
	}
	if check.ReasonCode != "sync_mutation_payload_missing_required_fields" {
		t.Fatalf("check reason code should describe the blocking finding, got %q", check.ReasonCode)
	}
	if check.Findings[1].ReasonCode != "sync_mutation_quarantined" {
		t.Fatalf("quarantined evidence dropped: %+v", check.Findings[1])
	}
}

// TestSyncMutationRequiredFieldsReportsQuarantinedEvidenceWithoutCloudEnrollment
// pins the seam between the quarantine reporting and the cloud-sync gate: the
// early return taken by a local-only install must still carry the quarantined
// evidence, because quarantine is a local disposition that has nothing to do
// with whether the operator opted into cloud sync.
func TestSyncMutationRequiredFieldsReportsQuarantinedEvidenceWithoutCloudEnrollment(t *testing.T) {
	s, cfg := newDiagnosticTestStoreWithConfig(t)
	seedDiagnosticPendingMutation(t, cfg.DataDir, "engram", store.SyncEntitySession, "poison", store.SyncOpUpsert, `{"id":"poison"}`)

	quarantine, err := s.QuarantineIrreparableSyncMutations("engram", true)
	if err != nil || len(quarantine.Actions) != 1 {
		t.Fatalf("quarantine report=%+v err=%v", quarantine, err)
	}

	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s, Project: "engram"}, CheckSyncMutationRequiredFields)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status == StatusBlocked {
		t.Fatalf("local-only install must not be blocked by a quarantined row: %+v", report)
	}
	check := report.Checks[0]
	if len(check.Findings) != 1 || check.Findings[0].ReasonCode != "sync_mutation_quarantined" {
		t.Fatalf("cloud sync gate swallowed the quarantined evidence: %+v", check.Findings)
	}
	if check.Findings[0].Severity != SeverityInfo || check.Findings[0].RequiresConfirmation {
		t.Fatalf("quarantined finding must stay non-blocking: %+v", check.Findings[0])
	}
	var evidence map[string]any
	if err := json.Unmarshal(check.Findings[0].Evidence, &evidence); err != nil {
		t.Fatalf("finding evidence invalid: %v", err)
	}
	if evidence["entity_key"] != "poison" || evidence["disposition"] != store.SyncMutationDispositionQuarantined {
		t.Fatalf("quarantined evidence lost mutation identity: %v", evidence)
	}
	// The local-only gate must not answer a quarantined row with cloud
	// enrollment guidance: there is nothing to enroll for.
	if strings.Contains(check.Findings[0].SafeNextStep, "engram cloud enroll") {
		t.Fatalf("local-only quarantine must not suggest cloud enrollment: %+v", check.Findings[0])
	}
}

// newDiagnosticTestStoreWithLegacyNullableSessions builds the shape an upgraded
// database has: sessions.project is still nullable, because no migration ever
// rewrote the column, and it carries rows that identify no project.
func newDiagnosticTestStoreWithLegacyNullableSessions(t *testing.T, sessions ...struct{ id, project string }) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	raw, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		project TEXT,
		directory TEXT NOT NULL,
		started_at TEXT NOT NULL DEFAULT (datetime('now')),
		ended_at TEXT,
		summary TEXT
	)`); err != nil {
		_ = raw.Close()
		t.Fatalf("create legacy sessions: %v", err)
	}
	for _, session := range sessions {
		var project any = session.project
		if session.project == "<NULL>" {
			project = nil
		}
		if _, err := raw.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, session.id, project, "/tmp"); err != nil {
			_ = raw.Close()
			t.Fatalf("seed legacy session %q: %v", session.id, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open migrated legacy database: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Doctor must survive the database it exists to diagnose. A legacy NULL project
// used to abort every check that reads sessions, so the whole report was lost.
func TestDoctorRunsEveryCheckOnLegacyNullProjectDatabase(t *testing.T) {
	type legacySession struct{ id, project string }
	s := newDiagnosticTestStoreWithLegacyNullableSessions(t,
		legacySession{"null-session", "<NULL>"},
		legacySession{"owned-session", "engram"},
	)

	report, err := NewRunner().RunAll(context.Background(), Scope{Store: s})
	if err != nil {
		t.Fatalf("RunAll on legacy NULL project database = %v, want a report", err)
	}
	if report.Summary.Total != len(RegisteredCodes()) {
		t.Fatalf("report evaluated %d checks, want all %d", report.Summary.Total, len(RegisteredCodes()))
	}
}

// Surfacing legacy ownership state is what doctor is for, so an unowned session
// must be reported as a finding that names it and carries the repair.
func TestUnownedSessionProjectCheckReportsLegacyOwnershipGaps(t *testing.T) {
	type legacySession struct{ id, project string }
	s := newDiagnosticTestStoreWithLegacyNullableSessions(t,
		legacySession{"null-session", "<NULL>"},
		legacySession{"blank-session", "  "},
		legacySession{"owned-session", "engram"},
	)

	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s}, CheckUnownedSessionProject)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusWarning {
		t.Fatalf("report status = %s, want %s", report.Status, StatusWarning)
	}
	check := report.Checks[0]
	if len(check.Findings) != 2 {
		t.Fatalf("findings = %+v, want one per unowned session", check.Findings)
	}
	seen := map[string]Finding{}
	for _, finding := range check.Findings {
		var evidence struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(finding.Evidence, &evidence); err != nil {
			t.Fatalf("finding evidence invalid: %v", err)
		}
		seen[evidence.SessionID] = finding
		if finding.ReasonCode != CheckUnownedSessionProject || finding.Severity != SeverityWarning {
			t.Fatalf("finding = %+v, want a warning that names the check", finding)
		}
		if !strings.Contains(finding.SafeNextStep, store.RescueOwnershipCommand) {
			t.Fatalf("finding must carry the repair, got %q", finding.SafeNextStep)
		}
		if !strings.Contains(finding.SafeNextStep, evidence.SessionID) {
			t.Fatalf("repair must name the session, got %q", finding.SafeNextStep)
		}
	}
	if _, ok := seen["null-session"]; !ok {
		t.Fatalf("NULL ownership was not reported: %+v", check.Findings)
	}
	if _, ok := seen["blank-session"]; !ok {
		t.Fatalf("blank ownership was not reported: %+v", check.Findings)
	}
	if _, ok := seen["owned-session"]; ok {
		t.Fatalf("an owned session must not be reported: %+v", check.Findings)
	}
}

// A healthy database reports nothing, so the check cannot become permanent noise.
func TestUnownedSessionProjectCheckIsOKWhenEverySessionIsOwned(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.CreateSession("owned-session", "engram", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s}, CheckUnownedSessionProject)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusOK || len(report.Checks[0].Findings) != 0 {
		t.Fatalf("report = %+v, want ok with no findings", report)
	}
}
