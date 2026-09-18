package diagnostic

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/constants"
	projectpkg "github.com/Gentleman-Programming/engram/v2/internal/project"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

const (
	CheckSessionProjectDirectoryMismatch  = "session_project_directory_mismatch"
	CheckManualSessionNameProjectMismatch = "manual_session_name_project_mismatch"
	CheckSyncMutationRequiredFields       = "sync_mutation_required_fields"
	CheckSyncTargetClosedSpace            = "sync_target_closed_space"
	CheckInvalidSessionIdentity           = "invalid_session_identity"
	CheckOrphanedObservationSession       = "orphaned_observation_session"
	CheckUnownedSessionProject            = "unowned_session_project"
	CheckSQLiteLockContention             = "sqlite_lock_contention"
	CheckAmbiguousActiveRuntimeSessions   = "ambiguous_active_runtime_sessions"
)

// ReasonQuarantinedPulledSessionIdentity marks a finding of
// CheckInvalidSessionIdentity that describes a pulled session mutation the
// apply path skipped rather than a corrupt local source row.
const ReasonQuarantinedPulledSessionIdentity = "quarantined_pulled_session_identity"

// ReasonForeignSyncTarget marks a finding of CheckSyncTargetClosedSpace whose
// sync_state row carries a target key outside the closed set of legitimate sync
// targets.
const ReasonForeignSyncTarget = "foreign_sync_target"

type SessionProjectDirectoryMismatchCheck struct{}
type ManualSessionNameProjectMismatchCheck struct{}
type SyncMutationRequiredFieldsCheck struct{}
type SyncTargetClosedSpaceCheck struct{}
type InvalidSessionIdentityCheck struct{}
type OrphanedObservationSessionCheck struct{}
type UnownedSessionProjectCheck struct{}
type SQLiteLockContentionCheck struct{}
type AmbiguousActiveRuntimeSessionsCheck struct{}

func (SessionProjectDirectoryMismatchCheck) Code() string {
	return CheckSessionProjectDirectoryMismatch
}
func (ManualSessionNameProjectMismatchCheck) Code() string {
	return CheckManualSessionNameProjectMismatch
}
func (SyncMutationRequiredFieldsCheck) Code() string { return CheckSyncMutationRequiredFields }
func (SyncTargetClosedSpaceCheck) Code() string      { return CheckSyncTargetClosedSpace }
func (InvalidSessionIdentityCheck) Code() string     { return CheckInvalidSessionIdentity }
func (OrphanedObservationSessionCheck) Code() string { return CheckOrphanedObservationSession }
func (UnownedSessionProjectCheck) Code() string      { return CheckUnownedSessionProject }
func (SQLiteLockContentionCheck) Code() string       { return CheckSQLiteLockContention }
func (AmbiguousActiveRuntimeSessionsCheck) Code() string {
	return CheckAmbiguousActiveRuntimeSessions
}

func (c AmbiguousActiveRuntimeSessionsCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	sessions, err := scope.Store.ListDiagnosticSessions(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}

	directoriesByProject := make(map[string]map[string]struct{})
	directoryBySessionID := make(map[string]string)
	for _, session := range sessions {
		project := normalizeProjectName(session.Project)
		if project == "" || session.Directory == "" {
			continue
		}
		if directoriesByProject[project] == nil {
			directoriesByProject[project] = make(map[string]struct{})
		}
		directoriesByProject[project][session.Directory] = struct{}{}
		directoryBySessionID[session.ID] = session.Directory
	}

	projects := make([]string, 0, len(directoriesByProject))
	for project := range directoriesByProject {
		projects = append(projects, project)
	}
	sort.Strings(projects)

	findings := make([]Finding, 0)
	for _, project := range projects {
		directories := make([]string, 0, len(directoriesByProject[project]))
		for directory := range directoriesByProject[project] {
			directories = append(directories, directory)
		}
		sort.Strings(directories)

		candidateIDs, err := scope.Store.ActiveRuntimeSessions(project, directories...)
		if err != nil {
			return CheckResult{}, err
		}
		candidatesByDirectory := make(map[string][]string)
		for _, id := range candidateIDs {
			candidatesByDirectory[directoryBySessionID[id]] = append(candidatesByDirectory[directoryBySessionID[id]], id)
		}

		ambiguousDirectories := make([]string, 0)
		ambiguousIDs := make([]string, 0)
		for _, directory := range directories {
			ids := candidatesByDirectory[directory]
			if len(ids) < 2 {
				continue
			}
			ambiguousDirectories = append(ambiguousDirectories, directory)
			ambiguousIDs = append(ambiguousIDs, ids...)
		}
		if len(ambiguousIDs) == 0 {
			continue
		}
		sort.Strings(ambiguousIDs)
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityWarning,
			ReasonCode:           c.Code(),
			Message:              fmt.Sprintf("Project %q has %d active runtime session candidates across %d directory or directories.", project, len(ambiguousIDs), len(ambiguousDirectories)),
			Why:                  "Omitted-session writes fail closed when multiple active runtime sessions match the same project and directory, so doctor reports the ambiguity without selecting or changing a session.",
			Evidence:             mustJSON(map[string]any{"project": project, "active_candidate_count": len(ambiguousIDs), "directories": ambiguousDirectories, "session_ids": ambiguousIDs}),
			SafeNextStep:         "Use an explicit session ID for writes in the affected directory; doctor does not select, end, or modify sessions.",
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), map[string]any{"projects_evaluated": len(projects)}, findings), nil
}

func (c SessionProjectDirectoryMismatchCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
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
	detected := make(map[string]DetectedProject)
	for _, session := range sessions {
		if _, knownManualTarget := knownManualSessionTarget(session.Name, knownProjects); knownManualTarget {
			continue
		}
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
		nameProject, knownManualTarget := knownManualSessionTarget(session.Name, knownProjects)
		sessionProject := normalizeProjectName(session.Project)
		if nameProject == "" || sessionProject == "" || nameProject == sessionProject || !knownManualTarget {
			continue
		}
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityWarning,
			ReasonCode:           "manual_session_name_project_mismatch",
			Message:              "Manual session name suffix does not match sessions.project.",
			Why:                  "Manual session naming drift cannot prove project ownership and must not be auto-rescued.",
			Evidence:             mustJSON(map[string]any{"session_id": session.ID, "session_name": session.Name, "session_project": session.Project, "ownership_mode": session.OwnershipMode, "name_project": nameProject}),
			SafeNextStep:         "Review the persisted project, then use the ownership rescue command deliberately; its SQLite backup is the rollback point.",
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), map[string]any{"sessions_evaluated": len(sessions)}, findings), nil
}

// knownManualSessionTarget recognizes the exact manual session name convention
// only when its normalized target is evidenced by a local session project. A
// manual-looking name without that local evidence remains untrusted.
func knownManualSessionTarget(name string, knownProjects map[string]bool) (string, bool) {
	if !strings.HasPrefix(name, "manual-save-") {
		return "", false
	}
	target := normalizeProjectName(strings.TrimPrefix(name, "manual-save-"))
	return target, target != "" && knownProjects[target]
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
	sourceObservations, err := scope.Store.ListDiagnosticObservationRequiredFields(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}
	mutations, err := scope.Store.ListPendingProjectMutations(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}
	blocking := make([]Finding, 0)
	terminal := make([]Finding, 0)
	for _, observation := range sourceObservations {
		blocking = append(blocking, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityBlocking,
			ReasonCode:           "observation_source_missing_required_fields",
			Message:              fmt.Sprintf("Observation source row %d is missing required fields: %s", observation.ID, strings.Join(observation.MissingFields, ", ")),
			Why:                  "A corrupt local observation source can produce rejected cloud payloads even when no pending mutation remains to diagnose.",
			Evidence:             mustJSON(observation),
			SafeNextStep:         syncMutationRequiredFieldsRepairHint(scope.Project),
			RequiresConfirmation: true,
		})
	}
	for _, mutation := range mutations {
		// A quarantined row is an explicit, already-taken disposition: it no
		// longer reaches transport, so it must not keep doctor blocked. It stays
		// reported as non-blocking evidence of what was dropped from sync.
		switch strings.TrimSpace(mutation.Disposition) {
		case store.SyncMutationDispositionQuarantined:
			terminal = append(terminal, c.quarantinedFinding(mutation))
			continue
		case store.SyncMutationDispositionSuperseded:
			if missing := supersededEvidenceMissingFields(mutation); len(missing) > 0 {
				blocking = append(blocking, c.incompleteSupersededFinding(mutation, missing))
			} else {
				terminal = append(terminal, c.supersededFinding(mutation))
			}
			continue
		}
		validation := store.ValidateSyncMutationPayload(mutation.Entity, mutation.Op, mutation.Payload, mutation.EntityKey)
		if validation.ReasonCode == "" {
			continue
		}
		blocking = append(blocking, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityBlocking,
			ReasonCode:           validation.ReasonCode,
			Message:              validation.Message,
			Why:                  "A pending sync mutation with missing required fields can block safe cloud replication and must fail loudly instead of being silently dropped.",
			Evidence:             mustJSON(map[string]any{"seq": mutation.Seq, "target_key": mutation.TargetKey, "project": mutation.Project, "entity": mutation.Entity, "op": mutation.Op, "entity_key": mutation.EntityKey, "missing_fields": validation.MissingFields}),
			SafeNextStep:         syncMutationRequiredFieldsRepairHint(scope.Project),
			RequiresConfirmation: true,
		})
	}
	// Quarantined rows are already-taken dispositions, so they never count as
	// work still pending delivery.
	evidence := map[string]any{"pending_mutations_evaluated": len(mutations) - len(terminal), "corrupt_source_observations": len(sourceObservations)}
	if len(terminal) > 0 {
		evidence["terminal_mutations"] = len(terminal)
	}
	// Blocking findings lead the roll-up so the check summary always describes the
	// work that still needs a decision rather than already-dispositioned evidence.
	rollUp := func() []Finding { return append(append([]Finding{}, blocking...), terminal...) }

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

func syncMutationRequiredFieldsRepairHint(project string) string {
	command := "engram doctor repair --check sync_mutation_required_fields --dry-run"
	if project = strings.TrimSpace(project); project != "" {
		command = "engram doctor repair --project " + project + " --check sync_mutation_required_fields --dry-run"
	}
	return "Run `" + command + "` to inspect local repairs; cloud-upgrade tooling requires configured cloud sync."
}

func supersededEvidenceMissingFields(mutation store.SyncMutation) []string {
	missing := make([]string, 0, 3)
	if strings.TrimSpace(mutation.DispositionReason) == "" {
		missing = append(missing, "disposition_reason")
	}
	if strings.TrimSpace(mutation.DispositionEvidence) == "" {
		missing = append(missing, "disposition_evidence")
	}
	if mutation.DispositionAt == nil || strings.TrimSpace(*mutation.DispositionAt) == "" {
		missing = append(missing, "disposition_at")
	}
	return missing
}

func (c SyncMutationRequiredFieldsCheck) incompleteSupersededFinding(mutation store.SyncMutation, missing []string) Finding {
	return Finding{
		CheckID:              c.Code(),
		Severity:             SeverityBlocking,
		ReasonCode:           "sync_mutation_superseded_evidence_incomplete",
		Message:              "Superseded sync mutation is missing required audit evidence: " + strings.Join(missing, ", "),
		Why:                  "A terminal supersession without its reason, evidence, and timestamp cannot prove why transport was suppressed.",
		Evidence:             mustJSON(map[string]any{"seq": mutation.Seq, "missing_fields": missing}),
		SafeNextStep:         "Inspect the local journal evidence and repair it deliberately; automatic supersession metadata repair is unavailable.",
		RequiresConfirmation: true,
	}
}

func (c SyncMutationRequiredFieldsCheck) supersededFinding(mutation store.SyncMutation) Finding {
	return Finding{
		CheckID:    c.Code(),
		Severity:   SeverityInfo,
		ReasonCode: "sync_mutation_superseded",
		Message:    "Sync mutation is superseded by current local lifecycle evidence and no longer blocks cloud replication.",
		Why:        "Supersession preserves the obsolete local journal row and its reason without acknowledging or transporting it, so doctor keeps audit evidence without treating it as active work.",
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
		SafeNextStep:         "No action required. Inspect the recorded disposition evidence if you need to audit the local reconciliation.",
		RequiresConfirmation: false,
	}
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

func (c SyncTargetClosedSpaceCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	states, err := scope.Store.ListSyncStates()
	if err != nil {
		return CheckResult{}, err
	}
	enrolled, err := scope.Store.ListEnrolledProjects()
	if err != nil {
		return CheckResult{}, err
	}
	// The closed set of legitimate sync targets: the legacy global cloud target,
	// the reserved cloud inbox target, the local chunk target, and one
	// cloud:<project> target per enrolled project. The listing is deliberately
	// unscoped even when scope.Project is set because sync_state rows are global:
	// a foreign target belongs to no project, so a project-scoped query could
	// never return it and doctor must still report the row that drifted in.
	closed := map[string]bool{
		store.DefaultSyncTargetKey: true,
		store.SyncInboxTargetKey:   true,
		store.LocalChunkTargetKey:  true,
	}
	for _, project := range enrolled {
		closed[syncTargetKeyForClosedSpace(project.Project)] = true
	}
	findings := make([]Finding, 0)
	for _, state := range states {
		if closed[state.TargetKey] {
			continue
		}
		findings = append(findings, Finding{
			CheckID:    c.Code(),
			Severity:   SeverityError,
			ReasonCode: ReasonForeignSyncTarget,
			Message:    fmt.Sprintf("Sync target %q is outside the closed set of legitimate sync targets.", state.TargetKey),
			Why:        "A sync_state row for an unknown target records sync progress no configured delivery pipeline can ever advance, so its state can silently rot while appearing live.",
			Evidence: mustJSON(map[string]any{
				"target_key":        state.TargetKey,
				"lifecycle":         state.Lifecycle,
				"unacked_mutations": state.UnackedMutations,
			}),
			SafeNextStep:         safeNextStepForForeignSyncTarget(state),
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), map[string]any{"sync_targets_evaluated": len(states), "enrolled_projects": len(enrolled)}, findings), nil
}

// safeNextStepForForeignSyncTarget selects the doctor guidance for a foreign
// sync_state row. Only rows with no pending mutations are inert drift the later
// cloud-inbox cleanup can remove safely; rows with unacknowledged mutations
// record writes no configured pipeline will ever deliver, so they demand an
// explicit review decision before the row is discarded.
func safeNextStepForForeignSyncTarget(state store.SyncTargetState) string {
	if state.UnackedMutations == 0 {
		return "If the target belongs to a project you want synced, run `engram cloud enroll <project>`. Otherwise no action is required: the row is inert drift left by the removed derivation fallback, it cannot advance and no data is at risk, and a later cloud-inbox slice removes these legacy rows automatically."
	}
	return fmt.Sprintf("Review the %d unacknowledged mutation(s) recorded for this target before removing the row: they record writes that no configured pipeline will deliver. If the target belongs to a project you want synced, run `engram cloud enroll <project>` so delivery can resume; otherwise clear or re-ack the pending mutations through a supported repair workflow first.", state.UnackedMutations)
}

func syncTargetKeyForClosedSpace(project string) string {
	project = normalizeProjectName(project)
	if project == "" {
		return ""
	}
	return store.DefaultSyncTargetKey + ":" + project
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
		mode := strings.TrimSpace(session.OwnershipMode)
		if normalizeProjectName(session.Project) != "" && (mode == store.SessionOwnershipShared || mode == store.SessionOwnershipProjectOwned) {
			continue
		}
		findings = append(findings, Finding{
			CheckID:    c.Code(),
			Severity:   SeverityWarning,
			ReasonCode: CheckUnownedSessionProject,
			Message:    fmt.Sprintf("Session %q has unclassified or invalid ownership metadata.", session.ID),
			Why:        "Legacy, blank, and invalid ownership metadata cannot safely establish a project-owned session, so an operator must classify it explicitly before relying on ownership enforcement.",
			Evidence: mustJSON(map[string]any{
				"session_id":      session.ID,
				"session_project": session.Project,
				"ownership_mode":  session.OwnershipMode,
				"directory":       session.Directory,
			}),
			SafeNextStep:         fmt.Sprintf("Assign ownership with `%s --project <name> --session %s` after confirming which project the session belongs to.", store.RescueOwnershipCommand, session.ID),
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), map[string]any{"sessions_evaluated": len(sessions)}, findings), nil
}

func (c OrphanedObservationSessionCheck) Run(ctx context.Context, scope Scope) (CheckResult, error) {
	_ = ctx
	evidence, err := scope.Store.ListOrphanedObservationSessionEvidence(scope.Project)
	if err != nil {
		return CheckResult{}, err
	}
	findings := make([]Finding, 0, len(evidence))
	for _, item := range evidence {
		findings = append(findings, Finding{
			CheckID:              c.Code(),
			Severity:             SeverityWarning,
			ReasonCode:           CheckOrphanedObservationSession,
			Message:              fmt.Sprintf("%d observation(s) reference missing session %q.", item.ObservationCount, item.SessionID),
			Why:                  "Observations reference a missing session, so their canonical session cannot be reconstructed automatically.",
			Evidence:             mustJSON(item),
			SafeNextStep:         "Inspect and recover the affected data deliberately. The canonical session cannot be reconstructed automatically, and no supported repair exists.",
			RequiresConfirmation: true,
		})
	}
	return resultFromFindings(c.Code(), map[string]any{"orphaned_session_references_evaluated": len(evidence)}, findings), nil
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
