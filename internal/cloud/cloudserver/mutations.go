package cloudserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/internal/project"
)

// ─── Types ────────────────────────────────────────────────────────────────────

// MutationEntry is an alias for cloudstore.MutationEntry (canonical wire type).
// Using a type alias ensures cloudstore.CloudStore satisfies MutationStore without
// adapter shims.
type MutationEntry = cloudstore.MutationEntry

// mutationPushEnvelope is the parsed request body for POST /sync/mutations/push.
// CreatedBy is optional and non-breaking — absent fields default to "unknown".
type mutationPushEnvelope struct {
	Entries   []MutationEntry `json:"entries"`
	CreatedBy string          `json:"created_by,omitempty"`
}

// StoredMutation is an alias for cloudstore.StoredMutation (canonical read type).
type StoredMutation = cloudstore.StoredMutation

// MutationStore is the subset of store methods needed by mutation handlers.
// It is satisfied by cloudstore.CloudStore and by test fakes.
// BC1: Using cloudstore types directly (via alias) ensures the type assertion
// s.store.(MutationStore) succeeds at runtime with a real *cloudstore.CloudStore.
type MutationStore interface {
	InsertMutationBatch(ctx context.Context, batch []cloudstore.MutationEntry) ([]int64, error)
	ListMutationsSince(ctx context.Context, sinceSeq int64, limit int, allowedProjects []string) ([]cloudstore.StoredMutation, bool, int64, error)
	IsProjectSyncEnabled(project string) (bool, error)
}

// Compile-time assertion: *cloudstore.CloudStore must satisfy MutationStore.
// This prevents future regressions where cloudstore changes break the interface contract.
var _ MutationStore = (*cloudstore.CloudStore)(nil)

// EnrolledProjectsProvider is an optional extension of ProjectAuthorizer
// that returns the list of enrolled projects for the authenticated caller.
type EnrolledProjectsProvider interface {
	EnrolledProjects() []string
}

const maxMutationBatchSize = 100
const defaultPullLimit = 100

// ─── Handlers ────────────────────────────────────────────────────────────────

// handleMutationPush handles POST /sync/mutations/push.
// REQ-200: bearer auth, body limit 8 MiB, batch size cap 100, pause gate (409 on sync_enabled=false).
// BC2: project authorization is enforced for every distinct project in the batch.
// BW9: 409 pause response uses writeActionableError for structured error envelope.
func (s *CloudServer) handleMutationPush(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPushBodyBytes)

	var req mutationPushEnvelope
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if len(req.Entries) > maxMutationBatchSize {
		http.Error(w, fmt.Sprintf("batch too large: max %d entries per request", maxMutationBatchSize), http.StatusBadRequest)
		return
	}

	// JC1: Empty batch is rejected early — empty batches carry no project info and
	// cannot be pause-gated or audited. Clients must send at least one entry.
	if len(req.Entries) == 0 {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, "empty_batch",
			"mutation batch must contain at least one entry")
		return
	}

	// BR2-1: Reject any entry with an empty project before auth/pause checks.
	// An empty project is always invalid: it bypasses per-project auth and would
	// be inserted into cloud_mutations with a blank project column.
	for _, entry := range req.Entries {
		if strings.TrimSpace(entry.Project) == "" {
			writeActionableError(w, http.StatusBadRequest, "invalid_request", "empty_project",
				"mutation entries must specify a project")
			return
		}
	}

	// N4: Assert MutationStore once here; use ms throughout (pause gate + InsertMutationBatch).
	// This avoids the double assertion that existed before (once inside an if-ok block at the
	// pause gate and once again before InsertMutationBatch).
	ms, ok := s.store.(MutationStore)
	if !ok {
		http.Error(w, "mutation store not available", http.StatusInternalServerError)
		return
	}

	// BC2: Authorize every distinct project in the batch before accepting any entry.
	// If ANY project is unauthorized, the entire batch is rejected (all-or-nothing).
	// N2: The empty-project `continue` is removed — BR2-1 (lines above) already
	// guarantees every entry has a non-empty project before this loop is reached.
	seen := make(map[string]struct{})
	for _, entry := range req.Entries {
		project := strings.TrimSpace(entry.Project)
		if _, ok := seen[project]; ok {
			continue
		}
		seen[project] = struct{}{}
		if !s.authorizeProjectScope(w, project) {
			// authorizeProjectScope already wrote the 403 response.
			return
		}
	}

	// REQ-414: Resolve primary project from request body (first entry).
	// Server-side has no filesystem cwd semantics; source is always "request_body".
	// N3: The `if len(req.Entries) > 0` guard is removed — JC1 (above) guarantees
	// at least one entry exists at this point.
	primaryProject := strings.TrimSpace(req.Entries[0].Project)

	// Check sync pause per project (REQ-203 + BW9: use writeActionableError for 409).
	for _, entry := range req.Entries {
		proj := strings.TrimSpace(entry.Project)
		enabled, err := ms.IsProjectSyncEnabled(proj)
		if err != nil {
			http.Error(w, fmt.Sprintf("check project sync: %v", err), http.StatusInternalServerError)
			return
		}
		if !enabled {
			// REQ-404: emit audit entry for pause-rejection before writing 409 response.
			// Uses structural type assertion — MutationStore is NOT extended.
			contributor := strings.TrimSpace(req.CreatedBy)
			if contributor == "" {
				contributor = "unknown"
			}
			if auditor, ok := s.store.(interface {
				InsertAuditEntry(ctx context.Context, entry cloudstore.AuditEntry) error
			}); ok {
				if aerr := auditor.InsertAuditEntry(r.Context(), cloudstore.AuditEntry{
					Contributor: contributor,
					Project:     proj,
					Action:      cloudstore.AuditActionMutationPush,
					Outcome:     cloudstore.AuditOutcomeRejectedProjectPaused,
					EntryCount:  len(req.Entries),
					ReasonCode:  "sync-paused",
				}); aerr != nil {
					log.Printf("cloudserver: audit insert failed (mutation push): %v", aerr)
				}
			} else {
				log.Printf("cloudserver: store (%T) does not implement InsertAuditEntry; audit skipped", s.store)
			}
			// REQ-414: include project envelope in 409 response alongside error fields.
			jsonResponse(w, http.StatusConflict, map[string]any{
				"error_class":    strings.TrimSpace(constants.UpgradeErrorClassPolicy),
				"error_code":     "sync-paused",
				"error":          fmt.Sprintf("sync is paused for project %q", proj),
				"project":        primaryProject,
				"project_source": project.SourceRequestBody,
				"project_path":   "",
			})
			return
		}
	}

	// REQ-006 / REQ-008: Validate each entry's payload before storage.
	// Relation entries are strictly validated (all 6 required fields).
	// Legacy entities (session, observation, prompt) use the lenient floor only.
	// Any failure rejects the ENTIRE batch (atomic — no partial inserts).
	var invalid []map[string]any
	for i, entry := range req.Entries {
		if field, ok := validateMutationEntry(entry); !ok {
			invalid = append(invalid, map[string]any{
				"index":  i,
				"field":  field,
				"entity": entry.Entity,
			})
		}
	}
	if len(invalid) > 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error":       "invalid relation payload",
			"reason_code": "validation_error",
			"invalid":     invalid,
		})
		return
	}

	acceptedSeqs, err := ms.InsertMutationBatch(r.Context(), req.Entries)
	if err != nil {
		http.Error(w, fmt.Sprintf("insert mutations: %v", err), http.StatusInternalServerError)
		return
	}

	// REQ-414: include project envelope in 200 response.
	jsonResponse(w, http.StatusOK, map[string]any{
		"accepted_seqs":  acceptedSeqs,
		"project":        primaryProject,
		"project_source": project.SourceRequestBody,
		"project_path":   "",
	})
}

// handleMutationPull handles GET /sync/mutations/pull.
// REQ-201: bearer auth, since_seq/limit params, server-side enrollment filter.
func (s *CloudServer) handleMutationPull(w http.ResponseWriter, r *http.Request) {
	sinceSeqStr := strings.TrimSpace(r.URL.Query().Get("since_seq"))
	limitStr := strings.TrimSpace(r.URL.Query().Get("limit"))

	sinceSeq := int64(0)
	if sinceSeqStr != "" {
		if v, err := strconv.ParseInt(sinceSeqStr, 10, 64); err == nil {
			sinceSeq = v
		}
	}

	limit := defaultPullLimit
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			if v > defaultPullLimit {
				v = defaultPullLimit
			}
			limit = v
		}
	}

	// Resolve allowed projects from the caller's enrollment (REQ-202).
	// BW2: Fail closed — when projectAuth is set but does not implement
	// EnrolledProjectsProvider, default to an empty allowedProjects slice
	// (returns nothing) rather than nil (which returns everything).
	var allowedProjects []string
	if s.projectAuth != nil {
		if ep, ok := s.projectAuth.(EnrolledProjectsProvider); ok {
			allowedProjects = ep.EnrolledProjects()
		} else {
			// EnrolledProjectsProvider not implemented: fail closed with empty list.
			// Log a warning so operators know the contract is violated.
			log.Printf("[cloudserver] WARNING: projectAuth (%T) does not implement EnrolledProjectsProvider; mutation pull returns empty to prevent cross-tenant leak", s.projectAuth)
			allowedProjects = []string{}
		}
	}

	// REQ-414: For pull, primary project = first enrolled project (or empty if none).
	// Server-side has no filesystem cwd; source is always "request_body".
	pullPrimaryProject := ""
	if len(allowedProjects) > 0 {
		pullPrimaryProject = allowedProjects[0]
	}

	ms, ok := s.store.(MutationStore)
	if !ok {
		jsonResponse(w, http.StatusOK, map[string]any{
			"mutations":      []StoredMutation{},
			"has_more":       false,
			"latest_seq":     int64(0),
			"project":        pullPrimaryProject,
			"project_source": project.SourceRequestBody,
			"project_path":   "",
		})
		return
	}

	mutations, hasMore, latestSeq, err := ms.ListMutationsSince(r.Context(), sinceSeq, limit, allowedProjects)
	if err != nil {
		http.Error(w, fmt.Sprintf("list mutations: %v", err), http.StatusInternalServerError)
		return
	}

	if mutations == nil {
		mutations = []StoredMutation{}
	}

	// REQ-414: include project envelope in 200 pull response.
	jsonResponse(w, http.StatusOK, map[string]any{
		"mutations":      mutations,
		"has_more":       hasMore,
		"latest_seq":     latestSeq,
		"project":        pullPrimaryProject,
		"project_source": project.SourceRequestBody,
		"project_path":   "",
	})
}

// ─── REQ-006 / REQ-008: Per-entity payload validation ────────────────────────

// relationRequiredFields lists the 6 fields that MUST be present and non-empty
// in every relation mutation payload (REQ-006). This list is the stable
// validation contract — Phase 3 MUST NOT remove or rename these fields without
// a wire-format version bump.
var relationRequiredFields = []string{
	"sync_id",
	"source_id",
	"target_id",
	"judgment_status",
	"marked_by_actor",
	"marked_by_kind",
}

// validateRelationPayload checks that all 6 required relation fields are present
// and non-empty in the decoded payload map.
// Returns (missingField, false) when any required field is absent or empty,
// or ("", true) when all required fields are present.
func validateRelationPayload(payload json.RawMessage) (string, bool) {
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		// Malformed JSON: treat sync_id as missing (first required field).
		return "sync_id", false
	}
	for _, field := range relationRequiredFields {
		v, ok := fields[field]
		if !ok {
			return field, false
		}
		s, isStr := v.(string)
		if !isStr || strings.TrimSpace(s) == "" {
			return field, false
		}
	}
	return "", true
}

// validateLegacyPayload is a no-op for legacy entities (session, observation,
// prompt). REQ-008: these entities have no new required payload fields — their
// push/pull behavior is UNCHANGED from before Phase 2. Any tightening of legacy
// payload validation is a breaking change and must not be done here.
func validateLegacyPayload(_ string, _ json.RawMessage) (string, bool) {
	return "", true
}

// validateMutationEntry dispatches to the correct validator for the entry's
// entity type. Returns (missingField, false) on validation failure.
func validateMutationEntry(entry MutationEntry) (string, bool) {
	switch entry.Entity {
	case "relation":
		return validateRelationPayload(entry.Payload)
	default:
		return validateLegacyPayload(entry.Entity, entry.Payload)
	}
}

// ─── Cloudstore mutation queries ──────────────────────────────────────────────
// These are implemented directly on CloudStore in cloudstore/cloudstore.go.
// The migration adds a cloud_mutations table. See AddMutationMigrations().
