package cloudserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// chunkIDRE validates that a chunk_id is exactly 8 hex characters.
var chunkIDRE = regexp.MustCompile(`^[0-9a-fA-F]{8}$`)

// maxPushBody is the maximum allowed body size for push requests (50 MB).
const maxPushBody = 50 << 20

// ─── Push Types ─────────────────────────────────────────────────────────────

// pushRequest represents the JSON body for POST /sync/push.
type pushRequest struct {
	ChunkID   string   `json:"chunk_id"`
	CreatedBy string   `json:"created_by"`
	Data      pushData `json:"data"`
}

// pushData holds the decomposed chunk content.
type pushData struct {
	Sessions     []pushSession     `json:"sessions"`
	Observations []pushObservation `json:"observations"`
	Prompts      []pushPrompt      `json:"prompts"`
}

type pushSession struct {
	ID        string  `json:"id"`
	Project   string  `json:"project"`
	Directory string  `json:"directory"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
	Summary   *string `json:"summary,omitempty"`
}

type pushObservation struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	ToolName  string `json:"tool_name,omitempty"`
	Project   string `json:"project,omitempty"`
	Scope     string `json:"scope,omitempty"`
	TopicKey  string `json:"topic_key,omitempty"`
}

type pushPrompt struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Project   string `json:"project,omitempty"`
}

func (s *CloudServer) ensureLegacyPushProjectsEnabled(req pushRequest) error {
	seen := map[string]struct{}{}
	projects := make([]string, 0)
	for _, sess := range req.Data.Sessions {
		if project := strings.TrimSpace(sess.Project); project != "" {
			if _, ok := seen[project]; !ok {
				seen[project] = struct{}{}
				projects = append(projects, project)
			}
		}
	}
	for _, obs := range req.Data.Observations {
		if project := strings.TrimSpace(obs.Project); project != "" {
			if _, ok := seen[project]; !ok {
				seen[project] = struct{}{}
				projects = append(projects, project)
			}
		}
	}
	for _, prompt := range req.Data.Prompts {
		if project := strings.TrimSpace(prompt.Project); project != "" {
			if _, ok := seen[project]; !ok {
				seen[project] = struct{}{}
				projects = append(projects, project)
			}
		}
	}
	for _, project := range projects {
		enabled, err := s.store.IsProjectSyncEnabled(project)
		if err != nil {
			return err
		}
		if !enabled {
			return fmt.Errorf("%w: %s", cloudstore.ErrProjectSyncPaused, project)
		}
	}
	return nil
}

// ─── Push Handler ───────────────────────────────────────────────────────────

// handlePush receives a sync chunk from a client. It validates the chunk_id
// format, decomposes the data into sessions/observations/prompts, inserts
// them into the cloudstore, and stores the raw chunk for pull replay.
//
// Body limit: 50 MB via http.MaxBytesReader.
// Idempotent: duplicate chunk_id for the same user is a no-op via ON CONFLICT.
func (s *CloudServer) handlePush(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)

	// Enforce 50 MB body limit.
	r.Body = http.MaxBytesReader(w, r.Body, maxPushBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		// MaxBytesReader returns a specific error when the limit is exceeded.
		if err.Error() == "http: request body too large" {
			jsonError(w, http.StatusRequestEntityTooLarge, "request body too large (max 50MB)")
			return
		}
		jsonError(w, http.StatusBadRequest, "failed to read body: "+err.Error())
		return
	}

	var req pushRequest
	if err := json.Unmarshal(body, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	// Validate chunk_id format.
	if !chunkIDRE.MatchString(req.ChunkID) {
		jsonError(w, http.StatusBadRequest, "chunk_id must be 8 hex characters")
		return
	}
	if err := s.ensureLegacyPushProjectsEnabled(req); err != nil {
		if errors.Is(err, cloudstore.ErrProjectSyncPaused) {
			jsonError(w, http.StatusConflict, err.Error())
			return
		}
		writeStoreError(w, err, "failed to validate project policy")
		return
	}

	// Insert sessions.
	sessCount := 0
	for _, sess := range req.Data.Sessions {
		if sess.ID == "" || sess.Project == "" {
			continue
		}
		if err := s.store.CreateSession(userID, sess.ID, sess.Project, sess.Directory); err != nil {
			writeStoreError(w, err, "failed to store session: "+err.Error())
			return
		}
		// If the session has an end, mark it.
		if sess.EndedAt != nil {
			summary := ""
			if sess.Summary != nil {
				summary = *sess.Summary
			}
			if err := s.store.EndSession(userID, sess.ID, summary); err != nil {
				writeStoreError(w, err, "failed to finalize session: "+err.Error())
				return
			}
		}
		sessCount++
	}

	// Insert observations.
	obsCount := 0
	for _, obs := range req.Data.Observations {
		if obs.SessionID == "" || obs.Title == "" || obs.Content == "" {
			continue
		}
		_, err := s.store.AddObservation(userID, cloudstore.AddCloudObservationParams{
			SessionID: obs.SessionID,
			Type:      obs.Type,
			Title:     obs.Title,
			Content:   obs.Content,
			ToolName:  obs.ToolName,
			Project:   obs.Project,
			Scope:     obs.Scope,
			TopicKey:  obs.TopicKey,
		})
		if err != nil {
			writeStoreError(w, err, "failed to store observation: "+err.Error())
			return
		}
		obsCount++
	}

	// Insert prompts.
	promptCount := 0
	for _, p := range req.Data.Prompts {
		if p.SessionID == "" || p.Content == "" {
			continue
		}
		_, err := s.store.AddPrompt(userID, cloudstore.AddCloudPromptParams{
			SessionID: p.SessionID,
			Content:   p.Content,
			Project:   p.Project,
		})
		if err != nil {
			writeStoreError(w, err, "failed to store prompt: "+err.Error())
			return
		}
		promptCount++
	}

	// Store raw chunk (idempotent via ON CONFLICT DO NOTHING).
	if err := s.store.StoreChunk(userID, req.ChunkID, req.CreatedBy, body, sessCount, obsCount, promptCount); err != nil {
		writeStoreError(w, err, "failed to store chunk: "+err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"status":              "accepted",
		"chunk_id":            req.ChunkID,
		"sessions_stored":     sessCount,
		"observations_stored": obsCount,
		"prompts_stored":      promptCount,
	})
}

// ─── Pull Handlers ──────────────────────────────────────────────────────────

// handlePullManifest returns the authenticated user's chunk manifest.
func (s *CloudServer) handlePullManifest(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)

	chunks, err := s.store.ListChunks(userID)
	if err != nil {
		writeStoreError(w, err, err.Error())
		return
	}

	// Build manifest entries.
	type manifestEntry struct {
		ID        string `json:"id"`
		CreatedBy string `json:"created_by"`
		CreatedAt string `json:"created_at"`
		Sessions  int    `json:"sessions"`
		Memories  int    `json:"memories"`
		Prompts   int    `json:"prompts"`
	}

	entries := make([]manifestEntry, 0, len(chunks))
	for _, c := range chunks {
		entries = append(entries, manifestEntry{
			ID:        c.ChunkID,
			CreatedBy: c.CreatedBy,
			CreatedAt: c.ImportedAt,
			Sessions:  c.Sessions,
			Memories:  c.Memories,
			Prompts:   c.Prompts,
		})
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"version": 1,
		"chunks":  entries,
	})
}

// handlePullChunk returns the raw chunk data for a specific chunk_id.
// Returns 404 if the chunk does not exist or belongs to another user.
func (s *CloudServer) handlePullChunk(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)
	chunkID := r.PathValue("chunk_id")

	data, err := s.store.GetChunk(userID, chunkID)
	if err != nil {
		if isDBConnectionError(err) {
			jsonError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		if errors.Is(err, sql.ErrNoRows) || data == nil {
			jsonError(w, http.StatusNotFound, "chunk not found")
			return
		}
		jsonError(w, http.StatusNotFound, "chunk not found")
		return
	}

	// Return the raw stored chunk data as JSON.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// ─── Mutation Push/Pull Handlers ────────────────────────────────────────────

// maxMutationPushBody is the maximum allowed body size for mutation push (10 MB).
const maxMutationPushBody = 10 << 20

// handleMutationPush receives a batch of mutations from a client, appends them
// to the cloud ledger, and optionally materializes them into relational tables.
// POST /sync/mutations/push
//
// Request body: {"mutations": [{"entity":"session","entity_key":"s1","op":"upsert","payload":{...}}, ...]}
// Response: {"accepted": N, "last_seq": M}
func (s *CloudServer) handleMutationPush(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)

	r.Body = http.MaxBytesReader(w, r.Body, maxMutationPushBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if err.Error() == "http: request body too large" {
			jsonError(w, http.StatusRequestEntityTooLarge, "request body too large (max 10MB)")
			return
		}
		jsonError(w, http.StatusBadRequest, "failed to read body: "+err.Error())
		return
	}

	var req cloudstore.PushMutationsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	if len(req.Mutations) == 0 {
		jsonResponse(w, http.StatusOK, &cloudstore.PushMutationsResult{Accepted: 0, LastSeq: 0})
		return
	}

	result, err := s.store.AppendMutationBatch(userID, req.Mutations)
	if err != nil {
		if errors.Is(err, cloudstore.ErrProjectSyncPaused) {
			jsonError(w, http.StatusConflict, err.Error())
			return
		}
		writeStoreError(w, err, "failed to append mutations: "+err.Error())
		return
	}

	// Best-effort materialization into relational tables.
	// Errors here don't fail the push — the ledger is the source of truth.
	for _, m := range req.Mutations {
		_ = s.store.ApplyMutationPayload(userID, m.Entity, m.Op, m.Payload)
	}

	jsonResponse(w, http.StatusOK, result)
}

// handleMutationPull returns mutations for the authenticated user with seq > since_seq.
// When team_sync=true (default), also includes observation mutations from teammates
// enrolled in the same projects (excludes personal-scoped observations).
// GET /sync/mutations/pull?since_seq=N&limit=M&team_sync=true
//
// Response: {"mutations": [...], "has_more": bool}
func (s *CloudServer) handleMutationPull(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)

	sinceSeqStr := r.URL.Query().Get("since_seq")
	var sinceSeq int64
	if sinceSeqStr != "" {
		var err error
		sinceSeq, err = strconv.ParseInt(sinceSeqStr, 10, 64)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "invalid since_seq parameter")
			return
		}
	}

	limit := queryInt(r, "limit", 100)
	if limit > 1000 {
		limit = 1000
	}

	// Check if team sync is requested (defaults to true).
	teamSync := r.URL.Query().Get("team_sync")
	useTeamSync := teamSync != "false"

	var result *cloudstore.PullMutationsResult
	var err error
	if useTeamSync {
		result, err = s.store.PullMutationsWithTeamSync(userID, sinceSeq, limit)
	} else {
		result, err = s.store.PullMutations(userID, sinceSeq, limit)
	}
	if err != nil {
		writeStoreError(w, err, "failed to pull mutations: "+err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

// ─── Enrollment Sync Handler ─────────────────────────────────────────────────

// handleEnrollmentSync receives a list of enrolled projects from a client and
// syncs them to the server. This enables cross-user team sync.
// POST /sync/enrollments
//
// Request body: {"projects": ["project-a", "project-b"]}
// Response: {"status": "synced", "project_count": N}
func (s *CloudServer) handleEnrollmentSync(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit
	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "failed to read body: "+err.Error())
		return
	}

	var req struct {
		Projects []string `json:"projects"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	if err := s.store.SyncEnrollments(userID, req.Projects); err != nil {
		writeStoreError(w, err, "failed to sync enrollments: "+err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"status":        "synced",
		"project_count": len(req.Projects),
	})
}

// handleEnrollmentList returns the user's current server-side enrollment list.
// GET /sync/enrollments
//
// Response: {"projects": ["project-a", "project-b"]}
func (s *CloudServer) handleEnrollmentList(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)

	projects, err := s.store.ListEnrolledProjects(userID)
	if err != nil {
		writeStoreError(w, err, "failed to list enrollments: "+err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"projects": projects,
	})
}
