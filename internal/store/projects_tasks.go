package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/tasks"
)

// MissingFieldError is returned when a required field is absent on create
// (RFC §5.3: title/kind are required when a task does not exist yet).
type MissingFieldError struct {
	Field string
}

func (e *MissingFieldError) Error() string {
	return fmt.Sprintf("missing required field: %s", e.Field)
}

// Task mirrors the tasks row (RFC §5.3).
type Task struct {
	ID                 int64   `json:"id"`
	SyncID             string  `json:"sync_id"`
	Project            string  `json:"project"`
	JiraKey            *string `json:"jira_key,omitempty"`
	SDDChange          *string `json:"sdd_change,omitempty"`
	Title              string  `json:"title"`
	Kind               string  `json:"kind"`
	State              string  `json:"state"`
	JiraStatus         *string `json:"jira_status,omitempty"`
	JiraStatusCategory *string `json:"jira_status_category,omitempty"`
	StateSyncedAt      *string `json:"state_synced_at,omitempty"`
	Branch             *string `json:"branch,omitempty"`
	PRUrl              *string `json:"pr_url,omitempty"`
	KnowledgeRef       *string `json:"knowledge_ref,omitempty"`
	Assignee           *string `json:"assignee,omitempty"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	ClosedAt           *string `json:"closed_at,omitempty"`
}

// UpsertTaskParams holds the optional fields of mem_task_upsert. A nil
// pointer means "omitted" and is left untouched on update.
type UpsertTaskParams struct {
	Project            string
	SyncID             *string
	JiraKey            *string
	SDDChange          *string
	Title              *string
	Kind               *string
	State              *string
	JiraStatus         *string
	JiraStatusCategory *string
	Branch             *string
	PRUrl              *string
	KnowledgeRef       *string
	Assignee           *string
}

// UpsertTaskResult is the outcome of UpsertTask.
type UpsertTaskResult struct {
	Task        Task
	Created     bool
	CardCreated bool
}

const taskSelectColumns = `id, sync_id, project, jira_key, sdd_change, title, kind, state,
	jira_status, jira_status_category, state_synced_at, branch, pr_url, knowledge_ref, assignee,
	created_at, updated_at, closed_at`

func scanTask(row interface{ Scan(dest ...any) error }) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.SyncID, &t.Project, &t.JiraKey, &t.SDDChange, &t.Title, &t.Kind, &t.State,
		&t.JiraStatus, &t.JiraStatusCategory, &t.StateSyncedAt, &t.Branch, &t.PRUrl, &t.KnowledgeRef, &t.Assignee,
		&t.CreatedAt, &t.UpdatedAt, &t.ClosedAt)
	return t, err
}

func (s *Store) getTaskByID(id int64) (Task, error) {
	t, err := scanTask(s.db.QueryRow(`SELECT `+taskSelectColumns+` FROM tasks WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrUnknownTask
	}
	if err != nil {
		return Task{}, fmt.Errorf("engram-projects: get task: %w", err)
	}
	return t, nil
}

func isClosedState(state string) bool {
	return tasks.ClosedStates[state]
}

func strVal(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// UpsertTask creates or updates a task using the precedence documented in
// RFC §5.3: sync_id -> jira_key -> (project, sdd_change) -> new row.
func (s *Store) UpsertTask(p UpsertTaskParams) (UpsertTaskResult, error) {
	var existingID int64
	var existingProject string
	found := false

	lookup := func(col, val string) error {
		err := s.db.QueryRow(`SELECT id, project FROM tasks WHERE `+col+` = ? AND deleted_at IS NULL`, val).
			Scan(&existingID, &existingProject)
		if err == nil {
			found = true
			return nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}

	if p.SyncID != nil && strings.TrimSpace(*p.SyncID) != "" {
		if err := lookup("sync_id", *p.SyncID); err != nil {
			return UpsertTaskResult{}, err
		}
		if found && existingProject != p.Project {
			return UpsertTaskResult{}, &TaskKeyConflictError{JiraKey: *p.SyncID, ExistingProject: existingProject}
		}
	}
	if !found && p.JiraKey != nil && strings.TrimSpace(*p.JiraKey) != "" {
		if err := lookup("jira_key", *p.JiraKey); err != nil {
			return UpsertTaskResult{}, err
		}
		if found && existingProject != p.Project {
			return UpsertTaskResult{}, &TaskKeyConflictError{JiraKey: *p.JiraKey, ExistingProject: existingProject}
		}
	}
	if !found && p.SDDChange != nil && strings.TrimSpace(*p.SDDChange) != "" {
		var id int64
		err := s.db.QueryRow(`SELECT id FROM tasks WHERE project = ? AND sdd_change = ? AND deleted_at IS NULL`,
			p.Project, *p.SDDChange).Scan(&id)
		if err == nil {
			found = true
			existingID = id
			existingProject = p.Project
		} else if !errors.Is(err, sql.ErrNoRows) {
			return UpsertTaskResult{}, err
		}
	}

	cardCreated, err := s.ensureMinimalProjectCard(p.Project)
	if err != nil {
		return UpsertTaskResult{}, err
	}

	now := s.nowUTC()

	if !found {
		if p.Title == nil || strings.TrimSpace(*p.Title) == "" {
			return UpsertTaskResult{}, &MissingFieldError{Field: "title"}
		}
		if p.Kind == nil || strings.TrimSpace(*p.Kind) == "" {
			return UpsertTaskResult{}, &MissingFieldError{Field: "kind"}
		}
		state := "open"
		if p.State != nil && *p.State != "" {
			state = *p.State
		} else if p.JiraStatus != nil {
			if derived, ok := tasks.DeriveState(*p.JiraStatus, strVal(p.JiraStatusCategory)); ok {
				state = derived
			}
		}
		var stateSyncedAt any
		if p.JiraStatus != nil {
			stateSyncedAt = now
		}
		var closedAt any
		if isClosedState(state) {
			closedAt = now
		}
		syncID := newSyncID("task")
		res, err := s.db.Exec(`
			INSERT INTO tasks (sync_id, project, jira_key, sdd_change, title, kind, state, jira_status,
				jira_status_category, state_synced_at, branch, pr_url, knowledge_ref, assignee,
				created_at, updated_at, closed_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			syncID, p.Project, nullableStr(p.JiraKey), nullableStr(p.SDDChange), *p.Title, *p.Kind, state,
			nullableStr(p.JiraStatus), nullableStr(p.JiraStatusCategory), stateSyncedAt, nullableStr(p.Branch),
			nullableStr(p.PRUrl), nullableStr(p.KnowledgeRef), nullableStr(p.Assignee), now, now, closedAt)
		if err != nil {
			return UpsertTaskResult{}, fmt.Errorf("engram-projects: insert task: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return UpsertTaskResult{}, err
		}
		existingID = id
	} else {
		sets := []string{"updated_at = ?"}
		args := []any{now}
		if p.Title != nil {
			sets = append(sets, "title = ?")
			args = append(args, *p.Title)
		}
		if p.Kind != nil {
			sets = append(sets, "kind = ?")
			args = append(args, *p.Kind)
		}
		var stateToSet *string
		if p.State != nil {
			stateToSet = p.State
		}
		if p.JiraStatus != nil {
			sets = append(sets, "jira_status = ?", "state_synced_at = ?")
			args = append(args, *p.JiraStatus, now)
			if stateToSet == nil {
				if derived, ok := tasks.DeriveState(*p.JiraStatus, strVal(p.JiraStatusCategory)); ok {
					stateToSet = &derived
				}
			}
		}
		if p.JiraStatusCategory != nil {
			sets = append(sets, "jira_status_category = ?")
			args = append(args, *p.JiraStatusCategory)
		}
		if stateToSet != nil {
			sets = append(sets, "state = ?")
			args = append(args, *stateToSet)
			if isClosedState(*stateToSet) {
				sets = append(sets, "closed_at = ?")
				args = append(args, now)
			} else {
				sets = append(sets, "closed_at = NULL")
			}
		}
		if p.Branch != nil {
			sets = append(sets, "branch = ?")
			args = append(args, *p.Branch)
		}
		if p.PRUrl != nil {
			sets = append(sets, "pr_url = ?")
			args = append(args, *p.PRUrl)
		}
		if p.KnowledgeRef != nil {
			sets = append(sets, "knowledge_ref = ?")
			args = append(args, *p.KnowledgeRef)
		}
		if p.Assignee != nil {
			sets = append(sets, "assignee = ?")
			args = append(args, *p.Assignee)
		}
		args = append(args, existingID)
		if _, err := s.db.Exec(`UPDATE tasks SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
			return UpsertTaskResult{}, fmt.Errorf("engram-projects: update task: %w", err)
		}
	}

	task, err := s.getTaskByID(existingID)
	if err != nil {
		return UpsertTaskResult{}, err
	}
	return UpsertTaskResult{Task: task, Created: !found, CardCreated: cardCreated}, nil
}

// TaskListFilter holds mem_task_list's filter parameters.
type TaskListFilter struct {
	State           string // "" or "active" -> every state except done/cancelled
	Kind            string
	JiraKey         string
	Query           string
	Limit           int
	Offset          int
	StaleAfterHours int
}

// TaskListItem is one row of mem_task_list's items array.
type TaskListItem struct {
	Task
	Observations int  `json:"observations"`
	Evidence     int  `json:"evidence"`
	StateStale   bool `json:"state_stale"`
}

// ListTasks lists tasks for a project applying TaskListFilter (RFC §5.4).
func (s *Store) ListTasks(project string, f TaskListFilter) ([]TaskListItem, int, error) {
	where := []string{"project = ?", "deleted_at IS NULL"}
	args := []any{project}

	switch f.State {
	case "", "active":
		where = append(where, "state NOT IN ('done','cancelled')")
	default:
		where = append(where, "state = ?")
		args = append(args, f.State)
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.JiraKey != "" {
		where = append(where, "jira_key = ?")
		args = append(args, f.JiraKey)
	}
	if strings.TrimSpace(f.Query) != "" {
		where = append(where, "id IN (SELECT rowid FROM tasks_fts WHERE tasks_fts MATCH ?)")
		args = append(args, sanitizeFTS(f.Query))
	}
	whereSQL := strings.Join(where, " AND ")

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("engram-projects: count tasks: %w", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	listArgs := append(append([]any{}, args...), limit, f.Offset)
	rows, err := s.db.Query(`SELECT `+taskSelectColumns+` FROM tasks WHERE `+whereSQL+
		` ORDER BY updated_at DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("engram-projects: list tasks: %w", err)
	}
	// The store pool is capped at one connection (Store.New), so every row
	// must be drained and rows.Close()d before issuing the nested per-task
	// count queries below — otherwise the second query blocks forever
	// waiting for a connection this same open cursor is holding.
	var tasksPage []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		tasksPage = append(tasksPage, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()

	staleAfterHours := f.StaleAfterHours
	if staleAfterHours <= 0 {
		staleAfterHours = 24
	}

	items := make([]TaskListItem, 0, len(tasksPage))
	for _, t := range tasksPage {
		item := TaskListItem{Task: t}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_observations WHERE task_id = ?`, t.ID).
			Scan(&item.Observations); err != nil {
			return nil, 0, err
		}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM evidence WHERE task_id = ? AND deleted_at IS NULL`, t.ID).
			Scan(&item.Evidence); err != nil {
			return nil, 0, err
		}
		item.StateStale = isTaskStateStale(t.StateSyncedAt, staleAfterHours)
		items = append(items, item)
	}
	return items, total, nil
}

func isTaskStateStale(stateSyncedAt *string, staleAfterHours int) bool {
	if stateSyncedAt == nil || strings.TrimSpace(*stateSyncedAt) == "" {
		return true
	}
	t, err := parseObservationTime(*stateSyncedAt)
	if err != nil {
		return true
	}
	return time.Since(t) > time.Duration(staleAfterHours)*time.Hour
}

var (
	taskSyncIDRefPattern = regexp.MustCompile(`^task-[0-9a-f]{16}$`)
	jiraKeyRefPattern    = regexp.MustCompile(`^[A-Z][A-Z0-9]+-[0-9]+$`)
	localIDRefPattern    = regexp.MustCompile(`^#([0-9]+)$`)
	changeRefRefPattern  = regexp.MustCompile(`^change:([a-z0-9][a-z0-9-]*)$`)
)

// ResolveTaskRef resolves a task reference string, scoped to project, using
// one of the four forms documented in RFC §5.0: jira_key, sync_id, "#id", or
// "change:sdd_change".
func (s *Store) ResolveTaskRef(project, ref string) (Task, error) {
	ref = strings.TrimSpace(ref)
	switch {
	case taskSyncIDRefPattern.MatchString(ref):
		return s.getTaskByProjectColumn(project, "sync_id", ref)
	case jiraKeyRefPattern.MatchString(ref):
		return s.getTaskByProjectColumn(project, "jira_key", ref)
	case localIDRefPattern.MatchString(ref):
		m := localIDRefPattern.FindStringSubmatch(ref)
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return Task{}, ErrUnknownTask
		}
		t, err := s.getTaskByID(id)
		if err != nil {
			return Task{}, err
		}
		if t.Project != project {
			return Task{}, ErrUnknownTask
		}
		return t, nil
	case changeRefRefPattern.MatchString(ref):
		m := changeRefRefPattern.FindStringSubmatch(ref)
		return s.getTaskByProjectColumn(project, "sdd_change", m[1])
	default:
		return Task{}, ErrUnknownTask
	}
}

func (s *Store) getTaskByProjectColumn(project, col, val string) (Task, error) {
	t, err := scanTask(s.db.QueryRow(
		`SELECT `+taskSelectColumns+` FROM tasks WHERE project = ? AND `+col+` = ? AND deleted_at IS NULL`,
		project, val))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrUnknownTask
	}
	if err != nil {
		return Task{}, fmt.Errorf("engram-projects: resolve task ref: %w", err)
	}
	return t, nil
}

// ─── mem_task_link ───────────────────────────────────────────────────────────

// LinkTaskObservationParams holds mem_task_link's input, already resolved to
// concrete Task/Observation rows by the MCP handler.
type LinkTaskObservationParams struct {
	Task          Task
	ObservationID int64
	Role          string // "" triggers topic_key-based default
	KnowledgeRef  *string
	GraphRef      *string
	GraphCommit   *string
	RunbookID     *string
	JiraRef       *string
}

// ObservationRefOut is one row of mem_task_link's `refs` output array.
type ObservationRefOut struct {
	RefKind     string  `json:"ref_kind"`
	Ref         string  `json:"ref"`
	GraphCommit *string `json:"graph_commit"`
}

// LinkTaskObservationResult is the outcome of LinkTaskObservation.
type LinkTaskObservationResult struct {
	Linked            bool
	TaskSyncID        string
	ObservationSyncID string
	Role              string
	RefsAdded         int
	Refs              []ObservationRefOut
}

// LinkTaskObservation links an observation to a task and optionally records
// observation_refs (RFC §5.5).
func (s *Store) LinkTaskObservation(p LinkTaskObservationParams) (LinkTaskObservationResult, error) {
	obs, err := s.GetObservation(p.ObservationID)
	if err != nil {
		return LinkTaskObservationResult{}, ErrUnknownObservation
	}
	obsProject := ""
	if obs.Project != nil {
		obsProject, _ = NormalizeProject(*obs.Project)
	}
	taskProject, _ := NormalizeProject(p.Task.Project)
	if obsProject != taskProject {
		return LinkTaskObservationResult{}, ErrCrossProjectLink
	}

	role := strings.TrimSpace(p.Role)
	if role == "" {
		role = "context"
		if obs.TopicKey != nil {
			switch {
			case strings.HasPrefix(*obs.TopicKey, "incident/"):
				role = "root_cause"
			case strings.HasPrefix(*obs.TopicKey, "evidence/"):
				role = "evidence"
			}
		}
	}

	now := s.nowUTC()
	res, err := s.db.Exec(`
		INSERT OR IGNORE INTO task_observations (task_id, observation_id, task_sync_id, observation_sync_id, role, linked_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.Task.ID, p.ObservationID, p.Task.SyncID, obs.SyncID, role, now)
	if err != nil {
		return LinkTaskObservationResult{}, fmt.Errorf("engram-projects: link task observation: %w", err)
	}
	affected, _ := res.RowsAffected()

	result := LinkTaskObservationResult{
		Linked:            affected > 0,
		TaskSyncID:        p.Task.SyncID,
		ObservationSyncID: obs.SyncID,
		Role:              role,
	}

	type refCandidate struct {
		kind        string
		ref         string
		graphCommit *string
	}
	var candidates []refCandidate
	if p.KnowledgeRef != nil && strings.TrimSpace(*p.KnowledgeRef) != "" {
		candidates = append(candidates, refCandidate{"knowledge", *p.KnowledgeRef, nil})
	}
	if p.GraphRef != nil && strings.TrimSpace(*p.GraphRef) != "" {
		if p.GraphCommit == nil || strings.TrimSpace(*p.GraphCommit) == "" {
			return LinkTaskObservationResult{}, ErrGraphCommitRequired
		}
		candidates = append(candidates, refCandidate{"graph", *p.GraphRef, p.GraphCommit})
	}
	if p.RunbookID != nil && strings.TrimSpace(*p.RunbookID) != "" {
		candidates = append(candidates, refCandidate{"runbook", *p.RunbookID, nil})
	}
	if p.JiraRef != nil && strings.TrimSpace(*p.JiraRef) != "" {
		candidates = append(candidates, refCandidate{"jira", *p.JiraRef, nil})
	}

	for _, c := range candidates {
		res, err := s.db.Exec(`
			INSERT OR IGNORE INTO observation_refs (observation_sync_id, ref_kind, ref, graph_commit, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			obs.SyncID, c.kind, c.ref, nullableStr(c.graphCommit), now)
		if err != nil {
			return LinkTaskObservationResult{}, fmt.Errorf("engram-projects: add observation ref: %w", err)
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			result.RefsAdded++
			result.Refs = append(result.Refs, ObservationRefOut{RefKind: c.kind, Ref: c.ref, GraphCommit: c.graphCommit})
		}
	}

	return result, nil
}
