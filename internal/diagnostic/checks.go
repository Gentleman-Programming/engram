package diagnostic

import (
	"context"
	"os"
	"strings"

	projectpkg "github.com/Gentleman-Programming/engram/internal/project"
	"github.com/Gentleman-Programming/engram/internal/store"
)

const (
	CheckSessionProjectDirectoryMismatch  = "session_project_directory_mismatch"
	CheckManualSessionNameProjectMismatch = "manual_session_name_project_mismatch"
	CheckSyncMutationRequiredFields       = "sync_mutation_required_fields"
	CheckSQLiteLockContention             = "sqlite_lock_contention"
)

type SessionProjectDirectoryMismatchCheck struct{}
type ManualSessionNameProjectMismatchCheck struct{}
type SyncMutationRequiredFieldsCheck struct{}
type SQLiteLockContentionCheck struct{}

func (SessionProjectDirectoryMismatchCheck) Code() string {
	return CheckSessionProjectDirectoryMismatch
}
func (ManualSessionNameProjectMismatchCheck) Code() string {
	return CheckManualSessionNameProjectMismatch
}
func (SyncMutationRequiredFieldsCheck) Code() string { return CheckSyncMutationRequiredFields }
func (SQLiteLockContentionCheck) Code() string       { return CheckSQLiteLockContention }

func (c SessionProjectDirectoryMismatchCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	sessions, err := scope.Store.ListDiagnosticSessions(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}
	findings := make([]Finding, 0)
	detected := make(map[string]DetectedProject)
	for _, session := range sessions {
		directory := strings.TrimSpace(session.Directory)
		directoryProject, ok := detectSessionDirectoryProject(scope, detected, directory)
		sessionProject := normalizeProjectName(session.Project)
		if !ok || directoryProject.Project == "" || sessionProject == "" || directoryProject.Project == sessionProject {
			continue
		}
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityWarning,
			ReasonCode:           "session_project_directory_mismatch",
			Message:              "Session project does not match the project inferred from its directory.",
			Why:                  "Project/directory drift can cause agents to retrieve or save memories under the wrong project scope.",
			Evidence:             mustJSON(map[string]any{"session_id": session.ID, "session_project": session.Project, "directory": session.Directory, "directory_project": directoryProject.Project, "directory_project_source": directoryProject.Source, "directory_project_path": directoryProject.Path}),
			SafeNextStep:         "Review the session evidence and use explicit `--project`/MCP project overrides until the project naming is consolidated.",
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), map[string]any{"sessions_evaluated": len(sessions)}, findings), nil
}

func detectSessionDirectoryProject(scope Scope, cache map[string]DetectedProject, directory string) (DetectedProject, bool) {
	if strings.TrimSpace(directory) == "" {
		return DetectedProject{}, false
	}
	if cached, ok := cache[directory]; ok {
		return cached, cached.Project != ""
	}
	if scope.DetectProject != nil {
		detected, ok := scope.DetectProject(directory)
		cache[directory] = detected
		return detected, ok && detected.Project != ""
	}
	if _, err := os.Stat(directory); err != nil {
		return DetectedProject{}, false
	}
	res := projectpkg.DetectProjectFull(directory)
	if res.Error != nil || (res.Source != projectpkg.SourceGitRemote && res.Source != projectpkg.SourceGitRoot) {
		return DetectedProject{}, false
	}
	detected := DetectedProject{Project: normalizeProjectName(res.Project), Source: res.Source, Path: res.Path}
	cache[directory] = detected
	return detected, detected.Project != ""
}

func (c ManualSessionNameProjectMismatchCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	sessions, err := scope.Store.ListDiagnosticSessions(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}
	knownProjects, err := knownSessionProjects(scope)
	if err != nil {
		return CheckResult{}, err
	}
	findings := make([]Finding, 0)
	for _, session := range sessions {
		if !strings.HasPrefix(session.Name, "manual-save-") {
			continue
		}
		nameProject := normalizeProjectName(strings.TrimPrefix(session.Name, "manual-save-"))
		sessionProject := normalizeProjectName(session.Project)
		if nameProject == "" || sessionProject == "" || nameProject == sessionProject || !knownProjects[nameProject] {
			continue
		}
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityWarning,
			ReasonCode:           "manual_session_name_project_mismatch",
			Message:              "Manual session name suffix does not match sessions.project.",
			Why:                  "Manual session naming drift can hide memories from project-scoped context retrieval.",
			Evidence:             mustJSON(map[string]any{"session_id": session.ID, "session_name": session.Name, "session_project": session.Project, "name_project": nameProject}),
			SafeNextStep:         "Use `engram context --project <project>` or MCP `project` overrides explicitly before deciding whether to consolidate projects.",
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), map[string]any{"sessions_evaluated": len(sessions)}, findings), nil
}

func knownSessionProjects(scope Scope) (map[string]bool, error) {
	sessions, err := scope.Store.ListDiagnosticSessions("")
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool)
	for _, session := range sessions {
		project := normalizeProjectName(session.Project)
		if project != "" {
			known[project] = true
		}
	}
	return known, nil
}

func (c SyncMutationRequiredFieldsCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	mutations, err := scope.Store.ListPendingProjectMutations(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}
	findings := make([]Finding, 0)
	for _, mutation := range mutations {
		validation := store.ValidateSyncMutationPayload(mutation.Entity, mutation.Op, mutation.Payload, mutation.EntityKey)
		if validation.ReasonCode == "" {
			continue
		}
		nextStep := "Run `engram cloud upgrade doctor` and inspect the mutation payload before any manual repair."
		if strings.TrimSpace(scope.Project) != "" {
			nextStep = "Run `engram cloud upgrade doctor --project " + scope.Project + "` and inspect the mutation payload before any manual repair."
		}
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityBlocking,
			ReasonCode:           validation.ReasonCode,
			Message:              validation.Message,
			Why:                  "A pending sync mutation with missing required fields can block safe cloud replication and must fail loudly instead of being silently dropped.",
			Evidence:             mustJSON(map[string]any{"seq": mutation.Seq, "target_key": mutation.TargetKey, "project": mutation.Project, "entity": mutation.Entity, "op": mutation.Op, "entity_key": mutation.EntityKey, "missing_fields": validation.MissingFields}),
			SafeNextStep:         nextStep,
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), map[string]any{"pending_mutations_evaluated": len(mutations)}, findings), nil
}

func (c SQLiteLockContentionCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	readSnapshot := scope.Store.ReadSQLiteLockSnapshot
	if scope.ReadSQLiteLockSnapshot != nil {
		readSnapshot = scope.ReadSQLiteLockSnapshot
	}
	snapshot, err := readSnapshot(ctx)
	if err != nil {
		finding := Finding{CheckID: c.Code(), Severity: SeverityError, ReasonCode: "sqlite_lock_probe_failed", Message: err.Error(), Why: "Doctor could not read SQLite lock state, so contention cannot be ruled out.", Evidence: mustJSON(map[string]any{"error": err.Error()}), SafeNextStep: "Close other Engram processes and rerun `engram doctor --check sqlite_lock_contention`.", RequiresConfirmation: false}
		return resultFromFindings(c.Code(), map[string]any{"probe": "failed"}, []Finding{finding}), nil
	}
	findings := make([]Finding, 0)
	if snapshot.CheckpointBusy > 0 || snapshot.BusyTimeoutMS <= 0 {
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityWarning,
			ReasonCode:           "sqlite_lock_contention_detected",
			Message:              "SQLite lock probe reported contention indicators.",
			Why:                  "Lock contention can cause writes or sync enrollment to fail; doctor only reports the condition and does not repair it.",
			Evidence:             mustJSON(snapshot),
			SafeNextStep:         "Stop other Engram processes, wait for active operations to finish, then rerun `engram doctor --check sqlite_lock_contention`.",
			RequiresConfirmation: false,
		})
	}
	return resultFromFindings(c.Code(), snapshot, findings), nil
}

func normalizeProjectName(value string) string {
	normalized, _ := store.NormalizeProject(strings.TrimSpace(value))
	return strings.TrimSpace(normalized)
}
