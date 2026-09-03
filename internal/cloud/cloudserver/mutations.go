package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/v2/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v2/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v2/internal/project"
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
// REQ-200: bearer auth, configurable body limit defaulting to 8 MiB, batch size cap 100, pause gate (409 on sync_enabled=false).
// BC2: project authorization is enforced for every distinct project in the batch.
// BW9: 409 pause response uses writeActionableError for structured error envelope.
func (s *CloudServer) handleMutationPush(w http.ResponseWriter, r *http.Request) {
	maxPushBodyBytes := s.pushBodyLimit()
	r.Body = http.MaxBytesReader(w, r.Body, maxPushBodyBytes)

	var req mutationPushEnvelope
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeActionableError(w, http.StatusRequestEntityTooLarge, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadTooLarge, fmt.Sprintf("push payload too large (max %d bytes)", maxPushBodyBytes))
			return
		}
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
		if !s.authorizeProjectScope(r.Context(), w, project) {
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

	// Canonicalize every entry before storage so accepted legacy sparse payloads
	// materialize the same way as later chunk replay. Any failure rejects the
	// ENTIRE batch before InsertMutationBatch is called.
	var invalid []map[string]any
	normalizedEntries := make([]MutationEntry, 0, len(req.Entries))
	for i, entry := range req.Entries {
		if strings.TrimSpace(entry.Entity) == "relation" {
			if field, ok := validateRelationPayload(entry.Payload); !ok {
				invalid = append(invalid, map[string]any{"index": i, "field": field, "entity": strings.TrimSpace(entry.Entity)})
				continue
			}
		}
		normalized, err := canonicalMutationEntry(entry)
		if err != nil {
			invalid = append(invalid, map[string]any{
				"index":  i,
				"field":  "payload",
				"entity": strings.TrimSpace(entry.Entity),
			})
			continue
		}
		normalizedEntries = append(normalizedEntries, normalized)
	}
	if len(invalid) > 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error_class": constants.UpgradeErrorClassRepairable,
			"error_code":  constants.UpgradeErrorCodePayloadInvalid,
			"error":       "invalid mutation payload",
			"reason_code": "validation_error",
			"invalid":     invalid,
		})
		return
	}

	acceptedSeqs, err := ms.InsertMutationBatch(r.Context(), normalizedEntries)
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

// canonicalMutationEntry reuses the production chunk canonicalizer instead of
// storing a payload form that would later fail during chunk materialization.
func canonicalMutationEntry(entry MutationEntry) (MutationEntry, error) {
	project := strings.TrimSpace(entry.Project)
	payload, err := normalizeLegacyMutationPayload(entry, project)
	if err != nil {
		return MutationEntry{}, err
	}
	encoded, err := json.Marshal(map[string]any{"mutations": []map[string]string{{
		"project": project, "entity": entry.Entity, "entity_key": entry.EntityKey,
		"op": entry.Op, "payload": payload,
	}}})
	if err != nil {
		return MutationEntry{}, err
	}
	canonical, err := chunkcodec.CanonicalizeForProject(encoded, project)
	if err != nil {
		return MutationEntry{}, err
	}
	var chunk struct {
		Mutations []struct {
			Project   string `json:"project"`
			Entity    string `json:"entity"`
			EntityKey string `json:"entity_key"`
			Op        string `json:"op"`
			Payload   string `json:"payload"`
		} `json:"mutations"`
	}
	if err := json.Unmarshal(canonical, &chunk); err != nil || len(chunk.Mutations) != 1 {
		if err == nil {
			err = fmt.Errorf("expected one canonical mutation")
		}
		return MutationEntry{}, err
	}
	mutation := chunk.Mutations[0]
	return MutationEntry{Project: mutation.Project, Entity: mutation.Entity, EntityKey: mutation.EntityKey, Op: mutation.Op, Payload: json.RawMessage(mutation.Payload)}, nil
}

// normalizeLegacyMutationPayload supplies deterministic placeholders only for
// absent legacy upsert fields; supplied malformed values remain invalid.
func normalizeLegacyMutationPayload(entry MutationEntry, project string) (string, error) {
	entity, op := strings.TrimSpace(entry.Entity), strings.TrimSpace(entry.Op)
	payload := strings.TrimSpace(string(entry.Payload))
	if entity != "session" && entity != "observation" && entity != "prompt" {
		return payload, nil
	}
	if strings.HasPrefix(payload, `"`) {
		if err := json.Unmarshal([]byte(payload), &payload); err != nil {
			return "", err
		}
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(payload), &fields); err != nil || fields == nil {
		if err == nil {
			err = fmt.Errorf("payload must be an object")
		}
		return "", err
	}
	key := strings.TrimSpace(entry.EntityKey)
	if op == "upsert" {
		switch entity {
		case "session":
			if _, ok := fields["id"]; !ok {
				fields["id"] = firstLegacyID(fields, key)
			}
			if _, ok := fields["directory"]; !ok {
				fields["directory"] = "."
			}
		case "observation":
			if _, ok := fields["sync_id"]; !ok {
				fields["sync_id"] = key
			}
			id := firstLegacyID(fields, key)
			for field, value := range map[string]string{"session_id": "legacy-" + id, "type": "legacy", "title": id, "content": id, "scope": "project"} {
				if _, ok := fields[field]; !ok {
					fields[field] = value
				}
			}
		case "prompt":
			if _, ok := fields["sync_id"]; !ok {
				fields["sync_id"] = key
			}
			id := firstLegacyID(fields, key)
			for field, value := range map[string]string{"session_id": "legacy-" + id, "content": id, "project": project} {
				if _, ok := fields[field]; !ok {
					fields[field] = value
				}
			}
		}
	}
	encoded, err := json.Marshal(fields)
	return string(encoded), err
}

func firstLegacyID(fields map[string]any, fallback string) string {
	if id, ok := fields["sync_id"].(string); ok && strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id)
	}
	return fallback
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
	legacyProjectAuth := s.projectAuth != nil
	if s.principalProject != nil {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, "forbidden: principal is required")
			return
		}
		if usesManagedProjectGrants(principal) {
			projects, err := s.principalProject.EnrolledProjectsForPrincipal(r.Context(), principal)
			if err != nil {
				writeActionableError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden, "forbidden: project is not allowed")
				return
			}
			if projects == nil {
				projects = []string{}
			}
			allowedProjects = projects
			legacyProjectAuth = false
		}
	}
	if legacyProjectAuth {
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

// validateRelationPayload checks that all required relation fields are present
// and non-empty in the decoded payload map using the canonical chunk validator.
// Returns (missingField, false) when any required field is absent or empty,
// or ("", true) when all required fields are present.
func validateRelationPayload(payload json.RawMessage) (string, bool) {
	return chunkcodec.ValidateRelationPayload(payload)
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
