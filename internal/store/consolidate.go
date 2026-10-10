package store

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Consolidation (issue #242, design 2026-09-27): atomically merge N live
// observations into one survivor. Plan-then-mutate — the plan resolves every
// refusal before the first write; execution re-resolves inside the same
// transaction that mutates, so a fingerprint mismatch can never race a write.

var (
	// ErrConsolidateNeedTwoSources rejects a consolidation of fewer than two
	// observations: merging one row into "one" is a no-op that would still
	// burn sync journal rows.
	ErrConsolidateNeedTwoSources = errors.New("consolidate needs at least two source observations")
	// ErrConsolidateDuplicateID rejects a source list that repeats an ID: the
	// operation is destructive, so ambiguous input is never guessed.
	ErrConsolidateDuplicateID = errors.New("consolidate source list contains duplicate ids")
	// ErrConsolidateMissingObservation covers both a nonexistent ID and an
	// already-soft-deleted source; both are indistinguishable from the
	// consolidation's point of view (design: "any ID missing or already
	// soft-deleted" refuses).
	ErrConsolidateMissingObservation = errors.New("consolidate source missing or already deleted")
	// ErrConsolidateCrossProject refuses sources that do not share one
	// canonical project: one consolidation is one contiguous sync range and
	// must never mix sync targets.
	ErrConsolidateCrossProject = errors.New("consolidate sources span multiple projects")
	// ErrConsolidateProvenanceCycle refuses a batch in which one live source's
	// consolidated_from section already lists another live source of the same
	// batch. The atomic flow cannot produce that shape — it only appears via
	// manual content tampering or a resurrected peer write — so it is refused
	// rather than propagated.
	ErrConsolidateProvenanceCycle = errors.New("consolidate provenance cycle detected")
	// ErrConsolidateFingerprintMismatch refuses a confirm whose sources
	// changed since the plan. The fresh plan is returned alongside so callers
	// re-plan instead of guessing.
	ErrConsolidateFingerprintMismatch = errors.New("consolidate sources changed since plan; re-plan required")
)

// consolidatedFromMarker starts the provenance footer appended to every
// survivor's content (design option (a): the consolidated_from sync_id list
// always rides the content, independent of the column provenance).
const consolidatedFromMarker = "## consolidated_from"

// consolidateFingerprintVersion namespaces the plan fingerprint format so a
// future formula change cannot silently confirm against old fingerprints.
const consolidateFingerprintVersion = "consolidate:v1"

// consolidateSyncIDPattern matches observation sync ids inside a provenance
// footer. newSyncID's random path emits hex and its fallback emits decimal;
// both are covered by [0-9a-f]+.
var consolidateSyncIDPattern = regexp.MustCompile(`obs-[0-9a-f]+`)

// ConsolidateOverrides carries optional survivor fields. Zero values mean
// "synthesize from the sources" (design: defaults synthesized newest-first).
type ConsolidateOverrides struct {
	Title    string
	Content  string
	Type     string
	TopicKey string
}

// ConsolidateSurvivor is the planned survivor: explicit overrides already
// applied over the synthesized defaults.
type ConsolidateSurvivor struct {
	SessionID string
	Title     string
	Content   string
	Type      string
	Scope     string
	Project   string
	TopicKey  string
}

// ConsolidatePlan is the resolved, refusal-checked plan. Sources are ordered
// newest-first; Fingerprint covers every source field the atomicity guarantee
// depends on.
type ConsolidatePlan struct {
	IDs         []int64
	Sources     []Observation
	Survivor    ConsolidateSurvivor
	Project     string
	Fingerprint string
}

// ConsolidateParams selects sources, overrides, and the plan/confirm protocol:
// Confirm=false is the dry-run default; Confirm=true requires the
// Fingerprint of the plan being confirmed.
type ConsolidateParams struct {
	IDs         []int64
	Overrides   ConsolidateOverrides
	Confirm     bool
	Fingerprint string
}

// ConsolidateResult carries the plan and, after a confirmed execution, the
// persisted survivor. On fingerprint mismatch the fresh plan is returned with
// ErrConsolidateFingerprintMismatch.
type ConsolidateResult struct {
	Plan     *ConsolidatePlan
	Survivor *Observation
}

// ConsolidateObservations merges the given live observations into one survivor
// (issue #242). Dry-run (Confirm=false) resolves and returns the plan without
// writing. Confirm re-resolves inside the mutation transaction and refuses on
// drift; the whole operation — survivor insert, source soft-deletes, and one
// sync journal row per state change — is a single transaction, so a failed
// insert can never leave the deletes applied.
func (s *Store) ConsolidateObservations(p ConsolidateParams) (*ConsolidateResult, error) {
	if len(p.IDs) < 2 {
		return nil, ErrConsolidateNeedTwoSources
	}
	seen := make(map[int64]struct{}, len(p.IDs))
	for _, id := range p.IDs {
		if _, dup := seen[id]; dup {
			return nil, ErrConsolidateDuplicateID
		}
		seen[id] = struct{}{}
	}

	if !p.Confirm {
		tx, err := s.db.Begin()
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		plan, err := s.buildConsolidationPlanTx(tx, p.IDs, p.Overrides)
		if err != nil {
			return nil, err
		}
		return &ConsolidateResult{Plan: plan}, nil
	}

	var out *ConsolidateResult
	err := s.withTx(func(tx *sql.Tx) error {
		out = nil
		plan, err := s.buildConsolidationPlanTx(tx, p.IDs, p.Overrides)
		if err != nil {
			return err
		}
		if plan.Fingerprint != p.Fingerprint {
			out = &ConsolidateResult{Plan: plan}
			return ErrConsolidateFingerprintMismatch
		}
		survivor, err := s.executeConsolidationTx(tx, plan)
		if err != nil {
			return err
		}
		out = &ConsolidateResult{Plan: plan, Survivor: survivor}
		return nil
	})
	return out, err
}

// buildConsolidationPlanTx resolves every refusal and synthesizes the
// survivor before the first write (the RescueNullProjectOwnership pattern).
// Sources are fetched live-only, so a missing or already-deleted ID refuses
// identically.
func (s *Store) buildConsolidationPlanTx(tx *sql.Tx, ids []int64, overrides ConsolidateOverrides) (*ConsolidatePlan, error) {
	sources := make([]Observation, 0, len(ids))
	for _, id := range ids {
		obs, err := s.getObservationTx(tx, id)
		if err == sql.ErrNoRows {
			return nil, ErrConsolidateMissingObservation
		}
		if err != nil {
			return nil, err
		}
		sources = append(sources, *obs)
	}
	// Newest-first: created_at strings are "2006-01-02 15:04:05" and sort
	// chronologically; the monotonic id breaks same-second ties.
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].CreatedAt != sources[j].CreatedAt {
			return sources[i].CreatedAt > sources[j].CreatedAt
		}
		return sources[i].ID > sources[j].ID
	})

	// Canonical project per source (DeleteObservation resolution order).
	canonical := ""
	batchSyncIDs := make(map[string]struct{}, len(sources))
	for i := range sources {
		src := &sources[i]
		project := derefString(src.Project)
		if project == "" {
			resolved, err := s.resolveSessionProjectTx(tx, src.SessionID)
			if err != nil {
				return nil, err
			}
			project = resolved
		}
		project, _ = NormalizeProject(project)
		src.Project = nullableString(project)
		if canonical == "" {
			canonical = project
		} else if project != canonical {
			return nil, ErrConsolidateCrossProject
		}
		batchSyncIDs[src.SyncID] = struct{}{}
	}

	// Provenance cycle: a live source whose consolidated_from section already
	// lists another live source of this batch.
	for i := range sources {
		for _, listed := range consolidateSyncIDPattern.FindAllString(consolidateProvenanceSection(sources[i].Content), -1) {
			if _, hit := batchSyncIDs[listed]; hit {
				return nil, ErrConsolidateProvenanceCycle
			}
		}
	}

	newest := sources[0]
	survivor := ConsolidateSurvivor{
		SessionID: newest.SessionID,
		Type:      overrides.Type,
		Scope:     normalizeScope(newest.Scope),
		Project:   canonical,
		TopicKey:  normalizeTopicKey(overrides.TopicKey),
	}
	survivor.Title = overrides.Title
	if survivor.Title == "" {
		titles := make([]string, len(sources))
		for i := range sources {
			titles[i] = sources[i].Title
		}
		survivor.Title = strings.Join(titles, " · ")
	}
	if survivor.Type == "" {
		survivor.Type = newest.Type
	}
	baseContent := overrides.Content
	if baseContent == "" {
		contents := make([]string, len(sources))
		for i := range sources {
			contents[i] = sources[i].Content
		}
		baseContent = strings.Join(contents, "\n\n")
	}
	survivor.Content = consolidateContentWithProvenance(baseContent, sources)

	return &ConsolidatePlan{
		IDs:         ids,
		Sources:     sources,
		Survivor:    survivor,
		Project:     canonical,
		Fingerprint: consolidateFingerprint(sources),
	}, nil
}

// consolidateProvenanceSection returns the consolidated_from tail of a
// content string, or "" when the content carries no provenance footer.
func consolidateProvenanceSection(content string) string {
	idx := strings.Index(content, consolidatedFromMarker)
	if idx < 0 {
		return ""
	}
	return content[idx+len(consolidatedFromMarker):]
}

// consolidateContentWithProvenance appends the consolidated_from footer to
// the survivor content. Provenance always rides the content (option (a))
// independent of the column provenance (option (c)).
func consolidateContentWithProvenance(base string, sources []Observation) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(base, "\n"))
	b.WriteString("\n\n")
	b.WriteString(consolidatedFromMarker)
	b.WriteString("\n")
	for i := range sources {
		fmt.Fprintf(&b, "- %s\n", sources[i].SyncID)
	}
	return b.String()
}

// consolidateFingerprint hashes the source set: every field a write can
// change (updated_at and revision_count move on every mutation path) plus the
// identity fields. Sorted by id so list order never changes the fingerprint.
func consolidateFingerprint(sources []Observation) string {
	ordered := make([]Observation, len(sources))
	copy(ordered, sources)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	h := sha256.New()
	fmt.Fprintf(h, "%s|", consolidateFingerprintVersion)
	for i := range ordered {
		fmt.Fprintf(h, "%d|%s|%s|%d|", ordered[i].ID, ordered[i].SyncID, ordered[i].UpdatedAt, ordered[i].RevisionCount)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// executeConsolidationTx mutates inside the caller's transaction: survivor
// INSERT first, its upsert journal row next (a peer mid-range never sees
// deletes without a survivor), then one soft-delete plus journal row per
// source, oldest-first for deterministic ranges. Unenrolled projects journal
// no cloud rows; their sources take the supersede-pending path, mirroring
// DeleteObservation.
func (s *Store) executeConsolidationTx(tx *sql.Tx, plan *ConsolidatePlan) (*Observation, error) {
	enrolled, err := isProjectEnrolledTx(tx, plan.Project)
	if err != nil {
		return nil, err
	}

	syncID := newSyncID("obs")
	res, err := s.execHook(tx,
		`INSERT INTO observations (sync_id, session_id, type, title, content, tool_name, project, scope, topic_key, normalized_hash, revision_count, duplicate_count, last_seen_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, CAST(? AS TEXT), ?, ?, ?, 1, 1, datetime('now'), datetime('now'))`,
		syncID, plan.Survivor.SessionID, plan.Survivor.Type, plan.Survivor.Title, plan.Survivor.Content,
		"mem_consolidate", plan.Survivor.Project, plan.Survivor.Scope, nullableString(plan.Survivor.TopicKey),
		hashNormalized(plan.Survivor.Content),
	)
	if err != nil {
		return nil, err
	}
	survivorID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	if enrolled {
		survivor, err := s.getObservationTx(tx, survivorID)
		if err != nil {
			return nil, err
		}
		if err := s.enqueueSyncMutationTx(tx, SyncEntityObservation, syncID, SyncOpUpsert, observationPayloadFromObservation(survivor)); err != nil {
			return nil, err
		}
	}

	// Oldest-first deletes keep journal ranges deterministic.
	ordered := make([]Observation, len(plan.Sources))
	copy(ordered, plan.Sources)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for i := range ordered {
		src := ordered[i]
		if _, err := s.execHook(tx,
			`UPDATE observations
			 SET deleted_at = datetime('now'),
			     updated_at = datetime('now')
			 WHERE id = ? AND deleted_at IS NULL`,
			src.ID,
		); err != nil {
			return nil, err
		}
		var deletedAt string
		if err := tx.QueryRow(`SELECT deleted_at FROM observations WHERE id = ?`, src.ID).Scan(&deletedAt); err != nil {
			return nil, err
		}
		if !enrolled {
			changed, err := s.supersedeDeletedEntityMutationTx(tx, SyncEntityObservation, src.SyncID, plan.Project)
			if err != nil {
				return nil, fmt.Errorf("supersede observation mutation: %w", err)
			}
			if err := s.refreshSupersededProjectLifecycleTx(tx, plan.Project, changed); err != nil {
				return nil, err
			}
			continue
		}
		if err := s.enqueueSyncMutationTx(tx, SyncEntityObservation, src.SyncID, SyncOpDelete, syncObservationPayload{
			SyncID:    src.SyncID,
			SessionID: src.SessionID,
			Project:   src.Project,
			Deleted:   true,
			DeletedAt: &deletedAt,
		}); err != nil {
			return nil, err
		}
	}

	return s.getObservationTx(tx, survivorID)
}
