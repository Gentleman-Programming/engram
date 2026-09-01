package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec"
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

// rawMutationPushEnvelope retains each entry as raw JSON until project policy
// gates have completed. This keeps nested payload fields opaque during the
// authentication, authorization, and pause checks.
type rawMutationPushEnvelope struct {
	Entries   []json.RawMessage `json:"entries"`
	CreatedBy string            `json:"created_by,omitempty"`
}

type mutationEntryProject struct {
	Project string `json:"project"`
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

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeActionableError(w, http.StatusRequestEntityTooLarge, constants.UpgradeErrorClassRepairable, constants.UpgradeErrorCodePayloadTooLarge, fmt.Sprintf("push payload too large (max %d bytes)", maxPushBodyBytes))
			return
		}
		http.Error(w, fmt.Sprintf("read request body: %v", err), http.StatusBadRequest)
		return
	}

	var rawReq rawMutationPushEnvelope
	if err := json.Unmarshal(body, &rawReq); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if len(rawReq.Entries) > maxMutationBatchSize {
		http.Error(w, fmt.Sprintf("batch too large: max %d entries per request", maxMutationBatchSize), http.StatusBadRequest)
		return
	}

	// JC1: Empty batch is rejected early — empty batches carry no project info and
	// cannot be pause-gated or audited. Clients must send at least one entry.
	if len(rawReq.Entries) == 0 {
		writeActionableError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, "empty_batch",
			"mutation batch must contain at least one entry")
		return
	}

	// BR2-1: Extract only project metadata before auth/pause checks. Nested
	// payload fields remain raw so policy errors cannot disclose validation data.
	projects := make([]string, 0, len(rawReq.Entries))
	seen := make(map[string]struct{}, len(rawReq.Entries))
	for i, rawEntry := range rawReq.Entries {
		var metadata mutationEntryProject
		if err := json.Unmarshal(rawEntry, &metadata); err != nil {
			http.Error(w, fmt.Sprintf("invalid request entry %d: %v", i, err), http.StatusBadRequest)
			return
		}
		project := strings.TrimSpace(metadata.Project)
		if project == "" {
			writeActionableError(w, http.StatusBadRequest, "invalid_request", "empty_project",
				"mutation entries must specify a project")
			return
		}
		if _, ok := seen[project]; !ok {
			seen[project] = struct{}{}
			projects = append(projects, project)
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
	for _, project := range projects {
		if !s.authorizeProjectScope(r.Context(), w, project) {
			// authorizeProjectScope already wrote the 403 response.
			return
		}
	}

	// REQ-414: Resolve primary project from request body (first entry).
	// Server-side has no filesystem cwd semantics; source is always "request_body".
	// N3: The `if len(req.Entries) > 0` guard is removed — JC1 (above) guarantees
	// at least one entry exists at this point.
	primaryProject := projects[0]

	// Check sync pause per project (REQ-203 + BW9: use writeActionableError for 409).
	for _, proj := range projects {
		enabled, err := ms.IsProjectSyncEnabled(proj)
		if err != nil {
			http.Error(w, fmt.Sprintf("check project sync: %v", err), http.StatusInternalServerError)
			return
		}
		if !enabled {
			// REQ-404: emit audit entry for pause-rejection before writing 409 response.
			// Uses structural type assertion — MutationStore is NOT extended.
			contributor := strings.TrimSpace(rawReq.CreatedBy)
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
					EntryCount:  len(rawReq.Entries),
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

	// Decode entries only after all project policy gates have succeeded. The
	// payload remains raw through auth and pause checks, then the shared codec
	// seam owns the canonical mutation contract.
	req := mutationPushEnvelope{
		CreatedBy: rawReq.CreatedBy,
		Entries:   make([]MutationEntry, len(rawReq.Entries)),
	}
	for i, rawEntry := range rawReq.Entries {
		if err := json.Unmarshal(rawEntry, &req.Entries[i]); err != nil {
			http.Error(w, fmt.Sprintf("invalid request entry %d: %v", i, err), http.StatusBadRequest)
			return
		}
	}

	// REQ-006 / REQ-008: Validate each entry's payload before storage.
	// Any failure rejects the ENTIRE batch (atomic — no partial inserts).
	var invalid []map[string]any
	for i, entry := range req.Entries {
		issue, ok := chunkcodec.ValidateMutationEntry(entry.Entity, entry.Op, entry.EntityKey, entry.Payload)
		if !ok {
			invalid = append(invalid, map[string]any{
				"index":  i,
				"field":  issue.Field,
				"entity": entry.Entity,
			})
		}
	}
	if len(invalid) > 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error_class": constants.UpgradeErrorClassRepairable,
			"error_code":  constants.MutationErrorCodePayloadInvalid,
			"error":       "invalid mutation payload",
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

// validateMutationEntry is retained as the cloudserver-local compatibility
// helper while the canonical validation rules live in chunkcodec.
func validateMutationEntry(entry MutationEntry) (string, bool) {
	issue, ok := chunkcodec.ValidateMutationEntry(entry.Entity, entry.Op, entry.EntityKey, entry.Payload)
	return issue.Field, ok
}

// ─── Cloudstore mutation queries ──────────────────────────────────────────────
// These are implemented directly on CloudStore in cloudstore/cloudstore.go.
// The migration adds a cloud_mutations table. See AddMutationMigrations().
