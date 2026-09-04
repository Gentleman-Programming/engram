// Package store: engram-projects (RFC rfc-engram-projects.md) data access.
//
// This file implements the CRUD and query methods backing the 10 MCP tools
// registered under the `projects` profile (internal/mcp/projects_tools.go):
// project cards, tasks, evidence, the runbook index, and the task<->
// observation link with its external references. The schema itself lives in
// projects_schema.go (EP-001); this file never issues DDL.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ─── Sentinel errors ─────────────────────────────────────────────────────────
//
// These map 1:1 to the error `code` values documented in RFC §5.0. Callers in
// internal/mcp translate them (via errors.As/Is) into the structured
// {"error", "code", ...} envelope; store methods never format that envelope
// themselves so the same data layer can back the HTTP API (T-04.03) later.
var (
	ErrNoProjectCard       = errors.New("no project card")
	ErrGraphNotFound       = errors.New("graph.json not found")
	ErrGraphMissingCommit  = errors.New("graph.json missing built_at_commit")
	ErrUnknownTask         = errors.New("unknown task")
	ErrUnknownObservation  = errors.New("unknown observation")
	ErrCrossProjectLink    = errors.New("link rejected: observation and task belong to different projects")
	ErrGraphCommitRequired = errors.New("graph_ref requires graph_commit")
)

// TaskKeyConflictError is returned by UpsertTask when the incoming jira_key
// already belongs to a task in a different project.
type TaskKeyConflictError struct {
	JiraKey         string
	ExistingProject string
}

func (e *TaskKeyConflictError) Error() string {
	return fmt.Sprintf("jira_key %q already belongs to project %q", e.JiraKey, e.ExistingProject)
}

// ─── Project card ────────────────────────────────────────────────────────────

// ProjectCard mirrors the project_cards row (RFC §5.1).
type ProjectCard struct {
	Slug             string  `json:"slug"`
	DisplayName      string  `json:"display_name"`
	RepoURL          *string `json:"repo_url,omitempty"`
	DefaultBranch    string  `json:"default_branch"`
	JiraProject      string  `json:"jira_project"`
	JiraComponent    *string `json:"jira_component,omitempty"`
	KnowledgeHubPath *string `json:"knowledge_hub_path,omitempty"`
	GraphPath        string  `json:"graph_path"`
	GraphCommit      *string `json:"graph_commit,omitempty"`
	GraphBuiltAt     *string `json:"graph_built_at,omitempty"`
	GraphSummary     *string `json:"graph_summary,omitempty"`
	Owner            *string `json:"owner,omitempty"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// ProjectCardCounts backs the `counts` section of mem_project_card.
type ProjectCardCounts struct {
	Observations       int `json:"observations"`
	Pinned             int `json:"pinned"`
	TasksActive        int `json:"tasks_active"`
	TasksTotal         int `json:"tasks_total"`
	Evidence           int `json:"evidence"`
	EvidenceUnattached int `json:"evidence_unattached"`
	Runbooks           int `json:"runbooks"`
	RunbooksStale      int `json:"runbooks_stale"`
}

// ProjectSyncSummary backs the `sync` section of mem_project_card. Full
// project-scoped mutation sync is T-04.05; today this only reports whether
// the project is enrolled for cloud sync and the shared sync_state lifecycle
// engram already tracks for it.
type ProjectSyncSummary struct {
	Enrolled     bool   `json:"enrolled"`
	LastAckedSeq int64  `json:"last_acked_seq"`
	Lifecycle    string `json:"lifecycle"`
}

const cloudSyncTargetKeyPrefix = "cloud"

// UpsertProjectCardParams holds the optional fields of mem_project_upsert.
// A nil pointer means "omitted": UpsertProjectCard leaves that column
// untouched on update, or applies its schema default on create.
type UpsertProjectCardParams struct {
	Slug             string
	DisplayName      *string
	RepoURL          *string
	DefaultBranch    *string
	JiraProject      *string
	JiraComponent    *string
	KnowledgeHubPath *string
	Owner            *string
	GraphPath        *string
}

// UpsertProjectCard creates or updates a project_cards row. It is idempotent:
// omitted fields are never overwritten on an existing card.
// DefaultJiraProject is the Jira project key applied when a card does not
// carry one. It is read from the environment so a deployment is not tied to
// one Jira project: set ENGRAM_JIRA_PROJECT to your own key.
func DefaultJiraProject() string {
	if v := strings.TrimSpace(os.Getenv("ENGRAM_JIRA_PROJECT")); v != "" {
		return v
	}
	return "PROJ"
}

func (s *Store) UpsertProjectCard(p UpsertProjectCardParams) (ProjectCard, bool, error) {
	existing, err := s.GetProjectCard(p.Slug)
	created := false
	switch {
	case err == nil:
		// update path below
	case errors.Is(err, ErrNoProjectCard):
		created = true
	default:
		return ProjectCard{}, false, err
	}

	now := s.nowUTC()
	if created {
		displayName := p.Slug
		if p.DisplayName != nil && strings.TrimSpace(*p.DisplayName) != "" {
			displayName = strings.TrimSpace(*p.DisplayName)
		}
		defaultBranch := "master"
		if p.DefaultBranch != nil {
			defaultBranch = *p.DefaultBranch
		}
		jiraProject := DefaultJiraProject()
		if p.JiraProject != nil {
			jiraProject = *p.JiraProject
		}
		graphPath := "graphify-out/graph.json"
		if p.GraphPath != nil {
			graphPath = *p.GraphPath
		}
		_, err := s.db.Exec(`
			INSERT INTO project_cards
				(slug, sync_id, display_name, repo_url, default_branch, jira_project,
				 jira_component, knowledge_hub_path, graph_path, owner, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.Slug, newSyncID("proj"), displayName, nullableStr(p.RepoURL), defaultBranch, jiraProject,
			nullableStr(p.JiraComponent), nullableStr(p.KnowledgeHubPath), graphPath, nullableStr(p.Owner), now, now,
		)
		if err != nil {
			return ProjectCard{}, false, fmt.Errorf("engram-projects: insert project card: %w", err)
		}
	} else {
		sets := []string{"updated_at = ?"}
		args := []any{now}
		addSet := func(col string, v *string) {
			if v == nil {
				return
			}
			sets = append(sets, col+" = ?")
			args = append(args, *v)
		}
		addSet("display_name", p.DisplayName)
		addSet("repo_url", p.RepoURL)
		addSet("default_branch", p.DefaultBranch)
		addSet("jira_project", p.JiraProject)
		addSet("jira_component", p.JiraComponent)
		addSet("knowledge_hub_path", p.KnowledgeHubPath)
		addSet("graph_path", p.GraphPath)
		addSet("owner", p.Owner)
		args = append(args, p.Slug)
		_, err := s.db.Exec(`UPDATE project_cards SET `+strings.Join(sets, ", ")+` WHERE slug = ?`, args...)
		if err != nil {
			return ProjectCard{}, false, fmt.Errorf("engram-projects: update project card: %w", err)
		}
	}
	_ = existing

	card, err := s.GetProjectCard(p.Slug)
	if err != nil {
		return ProjectCard{}, false, err
	}
	return card, created, nil
}

// GetProjectCard returns ErrNoProjectCard when the slug has no card yet.
func (s *Store) GetProjectCard(slug string) (ProjectCard, error) {
	var c ProjectCard
	err := s.db.QueryRow(`
		SELECT slug, display_name, repo_url, default_branch, jira_project, jira_component,
		       knowledge_hub_path, graph_path, graph_commit, graph_built_at, graph_summary,
		       owner, created_at, updated_at
		FROM project_cards WHERE slug = ? AND deleted_at IS NULL`, slug,
	).Scan(&c.Slug, &c.DisplayName, &c.RepoURL, &c.DefaultBranch, &c.JiraProject, &c.JiraComponent,
		&c.KnowledgeHubPath, &c.GraphPath, &c.GraphCommit, &c.GraphBuiltAt, &c.GraphSummary,
		&c.Owner, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectCard{}, ErrNoProjectCard
	}
	if err != nil {
		return ProjectCard{}, fmt.Errorf("engram-projects: get project card: %w", err)
	}
	return c, nil
}

// ProjectCardExists reports whether a project_cards row exists for slug,
// without erroring when it does not (unlike GetProjectCard).
func (s *Store) ProjectCardExists(slug string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM project_cards WHERE slug = ? AND deleted_at IS NULL`, slug).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ensureMinimalProjectCard creates a card with display_name = slug when none
// exists yet. Returns cardCreated=true when it had to create one. Used by
// UpsertTask and AddEvidence, whose parent RFC sections require a task's
// project to always resolve to a real card.
func (s *Store) ensureMinimalProjectCard(slug string) (bool, error) {
	exists, err := s.ProjectCardExists(slug)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	_, _, err = s.UpsertProjectCard(UpsertProjectCardParams{Slug: slug})
	if err != nil {
		return false, err
	}
	return true, nil
}

// ProjectCardCounts computes the dashboard counters for mem_project_card.
func (s *Store) ProjectCardCounts(slug string) (ProjectCardCounts, error) {
	var c ProjectCardCounts
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM observations WHERE LOWER(project) = ? AND deleted_at IS NULL`, slug,
	).Scan(&c.Observations); err != nil {
		return c, err
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM observations WHERE LOWER(project) = ? AND deleted_at IS NULL AND pinned = 1`, slug,
	).Scan(&c.Pinned); err != nil {
		return c, err
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM tasks WHERE project = ? AND deleted_at IS NULL`, slug,
	).Scan(&c.TasksTotal); err != nil {
		return c, err
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM tasks WHERE project = ? AND deleted_at IS NULL AND state NOT IN ('done','cancelled')`, slug,
	).Scan(&c.TasksActive); err != nil {
		return c, err
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM evidence WHERE project = ? AND deleted_at IS NULL`, slug,
	).Scan(&c.Evidence); err != nil {
		return c, err
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM evidence WHERE project = ? AND deleted_at IS NULL AND attached_jira = 0 AND attached_confluence_url IS NULL`, slug,
	).Scan(&c.EvidenceUnattached); err != nil {
		return c, err
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM runbook_index WHERE project = ?`, slug,
	).Scan(&c.Runbooks); err != nil {
		return c, err
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM runbook_index WHERE project = ? AND stale = 1`, slug,
	).Scan(&c.RunbooksStale); err != nil {
		return c, err
	}
	return c, nil
}

// ProjectSyncSummary reports the cloud-sync enrollment and lifecycle for a
// project without mutating sync_state (unlike GetSyncState, which
// bootstraps a row on first read — not appropriate from a read-only tool).
func (s *Store) ProjectSyncSummary(slug string) (ProjectSyncSummary, error) {
	var out ProjectSyncSummary
	enrolled, err := s.IsProjectEnrolled(slug)
	if err != nil {
		return out, err
	}
	out.Enrolled = enrolled
	out.Lifecycle = "disabled"
	if !enrolled {
		return out, nil
	}
	targetKey := cloudSyncTargetKeyPrefix + ":" + slug
	var lifecycle string
	var lastAcked int64
	err = s.db.QueryRow(
		`SELECT lifecycle, last_acked_seq FROM sync_state WHERE target_key = ?`, targetKey,
	).Scan(&lifecycle, &lastAcked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		out.Lifecycle = "idle"
		return out, nil
	case err != nil:
		return out, err
	default:
		out.Lifecycle = lifecycle
		out.LastAckedSeq = lastAcked
		return out, nil
	}
}

// GraphSyncResult backs the `graph` section of mem_project_upsert and
// POST /projects/{slug}/graph/sync.
//
// The graph_summary blob itself (god nodes, labeled communities,
// GRAPH_REPORT.md parsing — RFC §8) is computed by
// internal/project.SyncGraph, which calls StampProjectGraph below to persist
// it; that function returns this same struct so callers don't see a
// difference in shape.
type GraphSyncResult struct {
	Synced         bool   `json:"synced"`
	GraphCommit    string `json:"graph_commit"`
	HeadCommit     string `json:"head_commit"`
	Stale          bool   `json:"stale"`
	NodeCount      int    `json:"node_count"`
	EdgeCount      int    `json:"edge_count"`
	CommunityCount int    `json:"community_count"`
}

// StampProjectGraph persists the code-graph pointer (graph_commit,
// graph_built_at) and, when non-nil, the graph_summary blob for slug, in a
// single UPDATE. It is the only write path for these three columns.
//
// The parsing and aggregation that produces graphSummary lives in
// internal/project (see graph_summary.go there), not here: internal/project
// already depends on internal/store for card/task/evidence access, so store
// cannot depend back on project without an import cycle. This method is
// store's half of that split — plain persistence, no graphify-specific
// logic — which is also why it takes the summary pre-rendered as a JSON
// string rather than a struct.
//
// The DDL constraint `CHECK (graph_summary IS NULL OR graph_commit IS NOT
// NULL)` guarantees a summary is never stored without the commit it was
// computed from (D-02); this method's signature makes that pairing the only
// thing you can call it with in the first place.
func (s *Store) StampProjectGraph(slug, graphCommit, graphBuiltAt string, graphSummary *string) error {
	if _, err := s.db.Exec(
		`UPDATE project_cards SET graph_commit = ?, graph_built_at = ?, graph_summary = ?, updated_at = ? WHERE slug = ?`,
		graphCommit, graphBuiltAt, nullableStr(graphSummary), s.nowUTC(), slug,
	); err != nil {
		return fmt.Errorf("engram-projects: stamp graph commit: %w", err)
	}
	return nil
}

// nowUTC returns the current UTC time formatted like SQLite's datetime('now'),
// for Go-side timestamps that must match store column formatting exactly.
func (s *Store) nowUTC() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05")
}

func nullableStr(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
