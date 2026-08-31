package diagnostic

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/cloud/constants"
	projectpkg "github.com/Gentleman-Programming/engram/internal/project"
	"github.com/Gentleman-Programming/engram/internal/store"
)

const (
	CheckSessionProjectDirectoryMismatch  = "session_project_directory_mismatch"
	CheckManualSessionNameProjectMismatch = "manual_session_name_project_mismatch"
	CheckSyncMutationRequiredFields       = "sync_mutation_required_fields"
	CheckInvalidSessionIdentity           = "invalid_session_identity"
	CheckUnownedSessionProject            = "unowned_session_project"
	CheckSQLiteLockContention             = "sqlite_lock_contention"
)

// ReasonQuarantinedPulledSessionIdentity marks a finding of
// CheckInvalidSessionIdentity that describes a pulled session mutation the
// apply path skipped rather than a corrupt local source row.
const ReasonQuarantinedPulledSessionIdentity = "quarantined_pulled_session_identity"

type SessionProjectDirectoryMismatchCheck struct{}
type ManualSessionNameProjectMismatchCheck struct{}
type SyncMutationRequiredFieldsCheck struct{}
type InvalidSessionIdentityCheck struct{}
type UnownedSessionProjectCheck struct{}
type SQLiteLockContentionCheck struct{}

func (SessionProjectDirectoryMismatchCheck) Code() string {
	return CheckSessionProjectDirectoryMismatch
}
func (ManualSessionNameProjectMismatchCheck) Code() string {
	return CheckManualSessionNameProjectMismatch
}
func (SyncMutationRequiredFieldsCheck) Code() string { return CheckSyncMutationRequiredFields }
func (InvalidSessionIdentityCheck) Code() string     { return CheckInvalidSessionIdentity }
func (UnownedSessionProjectCheck) Code() string      { return CheckUnownedSessionProject }
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

// cloudSyncInUse reports whether this device opted into cloud sync. Enrollment
// is the store level signal the cloud paths already use to decide whether a
// project may be delivered, so at least one enrolled project is the evidence
// that the operator asked for cloud sync at all.
func cloudSyncInUse(scope Scope) (bool, error) {
	enrolled, err := scope.Store.ListEnrolledProjects()
	if err != nil {
		return false, err
	}
	return len(enrolled) > 0, nil
}

func (c SyncMutationRequiredFieldsCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	mutations, err := scope.Store.ListPendingProjectMutations(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}
	blocking := make([]Finding, 0)
	quarantined := make([]Finding, 0)
	for _, mutation := range mutations {
		// A quarantined row is an explicit, already-taken disposition: it no
		// longer reaches transport, so it must not keep doctor blocked. It stays
		// reported as non-blocking evidence of what was dropped from sync.
		if strings.TrimSpace(mutation.Disposition) == store.SyncMutationDispositionQuarantined {
			quarantined = append(quarantined, c.quarantinedFinding(mutation))
			continue
		}
		validation := store.ValidateSyncMutationPayload(mutation.Entity, mutation.Op, mutation.Payload, mutation.EntityKey)
		if validation.ReasonCode == "" {
			continue
		}
		nextStep := "Run `engram cloud upgrade doctor` and inspect the mutation payload before any manual repair."
		if strings.TrimSpace(scope.Project) != "" {
			nextStep = "Run `engram cloud upgrade doctor --project " + scope.Project + "` and inspect the mutation payload before any manual repair."
		}
		blocking = append(blocking, Finding{
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
	// Quarantined rows are already-taken dispositions, so they never count as
	// work still pending delivery.
	evidence := map[string]any{"pending_mutations_evaluated": len(mutations) - len(quarantined)}
	if len(quarantined) > 0 {
		evidence["quarantined_mutations"] = len(quarantined)
	}
	// Blocking findings lead the roll-up so the check summary always describes the
	// work that still needs a decision rather than already-dispositioned evidence.
	rollUp := func() []Finding { return append(append([]Finding{}, blocking...), quarantined...) }

	// A non-enrolled backlog is only a fault on a device that actually uses
	// cloud sync. The store journals sync mutations unconditionally, so on a
	// local-only install every pending mutation belongs to a non-enrolled
	// project by definition — the normal steady state, not something doctor
	// should block on and answer with `engram cloud enroll`. This mirrors the
	// autosync manager, which owns the same reason code and only evaluates it
	// while cloud sync is configured and running. The gate is deliberately
	// placed after the payload/quarantine pass so a local-only install still
	// gets its quarantined evidence reported instead of silently dropped.
	usesCloudSync, err := cloudSyncInUse(scope)
	if err != nil {
		return CheckResult{}, err
	}
	if !usesCloudSync {
		return resultFromFindings(c.Code(), evidence, rollUp()), nil
	}
	// CountPendingNonEnrolledSyncMutations only counts rows whose disposition is
	// still `pending`, so a quarantined row can never resurrect this blocking
	// finding: the backlog it reports is genuinely undeliverable work.
	nonEnrolledCounts, err := scope.Store.CountPendingNonEnrolledSyncMutations(store.DefaultSyncTargetKey)
	if err != nil {
		return CheckResult{}, err
	}
	scopedProject := normalizeProjectName(scope.Project)
	for _, projectCount := range nonEnrolledCounts {
		project := normalizeProjectName(projectCount.Project)
		if scopedProject != "" && project != scopedProject {
			continue
		}
		blocking = append(blocking, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityBlocking,
			ReasonCode:           constants.ReasonNonEnrolledPendingMutations,
			Message:              fmt.Sprintf("Pending cloud sync mutations for project %q are blocked because it is not enrolled.", project),
			Why:                  "Cloud delivery cannot continue while pending mutations belong to a project that is not enrolled.",
			Evidence:             mustJSON(map[string]any{"project": project, "pending_mutations": projectCount.Count}),
			SafeNextStep:         "Run `engram cloud enroll <project>` for each intended project or review enrollment, then rerun `engram doctor`.",
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), evidence, rollUp()), nil
}

func (c SyncMutationRequiredFieldsCheck) quarantinedFinding(mutation store.SyncMutation) Finding {
	return Finding{
		CheckID:    c.Code(),
		Severity:   SeverityInfo,
		ReasonCode: "sync_mutation_quarantined",
		Message:    "Sync mutation is quarantined and no longer blocks cloud replication.",
		Why:        "Quarantine keeps the irreparable journal row as durable local evidence while removing it from transport, so doctor reports it instead of staying blocked forever.",
		Evidence: mustJSON(map[string]any{
			"seq":                  mutation.Seq,
			"target_key":           mutation.TargetKey,
			"project":              mutation.Project,
			"entity":               mutation.Entity,
			"op":                   mutation.Op,
			"entity_key":           mutation.EntityKey,
			"disposition":          mutation.Disposition,
			"disposition_reason":   mutation.DispositionReason,
			"disposition_evidence": mutation.DispositionEvidence,
			"disposition_at":       mutation.DispositionAt,
		}),
		SafeNextStep:         "No action required. Inspect the recorded disposition evidence if you need to know what was dropped from cloud sync.",
		RequiresConfirmation: false,
	}
}

func (c InvalidSessionIdentityCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	evidence, err := scope.Store.ListInvalidSessionIdentityEvidence(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}
	quarantined, err := scope.Store.ListQuarantinedPulledSessionEvidence(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}
	findings := make([]Finding, 0, len(evidence)+len(quarantined))
	// Blocking source-row findings stay first: resultFromFindings derives the
	// check-level reason code from findings[0].
	for _, item := range evidence {
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityBlocking,
			ReasonCode:           CheckInvalidSessionIdentity,
			Message:              "Session source ID is blank; affected references and journal entries cannot be repaired without an explicit canonical session ID.",
			Why:                  "A blank session ID is not accepted by cloud replication and re-emitting it would preserve corrupt identity data.",
			Evidence:             mustJSON(item),
			SafeNextStep:         "Provide an explicit canonical session ID through a supported repair workflow; automatic ID generation is intentionally unavailable.",
			RequiresConfirmation: true,
		})
	}
	// Quarantined pulled mutations are reported but not blocking: the pull
	// already skipped them and advanced its cursor, so replication itself is
	// healthy and only the dropped remote rows need an operator decision.
	for _, item := range quarantined {
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityWarning,
			ReasonCode:           ReasonQuarantinedPulledSessionIdentity,
			Message:              "A pulled session mutation was skipped because its identity is blank or does not match its payload; the pull cursor advanced past it.",
			Why:                  "Halting the pull on a historical blank identity would pin the cursor forever, so the mutation is quarantined as evidence instead.",
			Evidence:             mustJSON(item),
			SafeNextStep:         "Inspect the quarantined mutation with `engram conflicts deferred`; it can only be applied once the remote side publishes a canonical session ID.",
			RequiresConfirmation: true,
		})
	}
	details := map[string]any{
		"invalid_source_sessions":     len(evidence),
		"quarantined_pulled_sessions": len(quarantined),
	}
	return resultFromFindings(c.Code(), details, findings), nil
}

// Run reports the sessions that identify no project. A database upgraded from
// the schema where sessions.project was nullable keeps those rows intact, and
// they are the population the ownership errors send to doctor, so leaving them
// unreported would answer that referral with an empty report.
//
// The listing is deliberately unscoped even when scope.Project is set: an
// unowned session belongs to no project, so a project-scoped query can never
// return it, and a user who runs `engram doctor --project <name>` after an
// ownership failure must still be shown the rows that caused it.
func (c UnownedSessionProjectCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	sessions, err := scope.Store.ListDiagnosticSessions("")
	if err != nil {
		return CheckResult{}, err
	}
	findings := make([]Finding, 0)
	for _, session := range sessions {
		if normalizeProjectName(session.Project) != "" {
			continue
		}
		findings = append(findings, Finding{
			CheckID:    c.Code(),
			Severity:   SeverityWarning,
			ReasonCode: CheckUnownedSessionProject,
			Message:    fmt.Sprintf("Session %q identifies no project.", session.ID),
			Why:        "A session left over from the schema where sessions.project was nullable owns no project, so its memories stay outside every project-scoped retrieval and writes to it must resolve ownership before they can land.",
			Evidence: mustJSON(map[string]any{
				"session_id":      session.ID,
				"session_project": session.Project,
				"directory":       session.Directory,
			}),
			SafeNextStep:         fmt.Sprintf("Assign ownership with `%s --project <name> --session %s` after confirming which project the session belongs to.", store.RescueOwnershipCommand, session.ID),
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), map[string]any{"sessions_evaluated": len(sessions)}, findings), nil
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
