// engram-projects MCP tools (RFC rfc-engram-projects.md §5): the 10 tools
// registered under the `projects` --tools profile. Every tool follows the
// conventions in RFC §5.0: project resolution via the same resolvers as the
// rest of this package, an envelope of {"project", "project_source",
// "project_path", "result"} on success, and {"error", "code", ...fields} on
// failure — a fresh, simpler shape than errorWithMeta's, used exclusively by
// this tool family per the RFC.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	projectpkg "github.com/Gentleman-Programming/engram/internal/project"
	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ─── Shared validation ───────────────────────────────────────────────────────

var (
	projectSlugPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	jiraKeyToolPattern  = regexp.MustCompile(`^[A-Z][A-Z0-9]+-[0-9]+$`)
	sddChangePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	taskSyncIDToolPattern = regexp.MustCompile(`^task-[0-9a-f]{16}$`)
	obsSyncIDPattern    = regexp.MustCompile(`^obs-[0-9a-f]{16,32}$`)
	sha256ToolPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	runbookIDToolPattern = regexp.MustCompile(`^RB-[0-9]{3}$`)
)

var reservedProjectSlugs = map[string]bool{"migrate": true, "current": true}

func isValidProjectSlug(slug string) bool {
	return projectSlugPattern.MatchString(slug) && !reservedProjectSlugs[slug]
}

var taskKindEnum = []string{"feature", "bugfix", "refactor", "incident", "migration", "spike"}
var taskStateEnum = []string{"open", "analysis", "in_progress", "review", "verified", "done", "blocked", "cancelled"}
var taskListStateEnum = append([]string{"active"}, taskStateEnum...)
var jiraStatusCategoryEnum = []string{"new", "indeterminate", "done"}
var taskLinkRoleEnum = []string{"context", "decision", "root_cause", "evidence", "summary"}
var evidenceKindEnum = []string{"png", "gif", "mp4", "json", "log", "txt"}
var runbookCategoryEnum = []string{"auth", "database", "queue", "network", "performance", "data-integrity", "registration"}
var runbookPatternEnum = []string{"missing-files", "auth-access", "file-save-failure", "sync-upload", "registration-subscription", "other"}
var runbookSeverityEnum = []string{"P1", "P2", "P3", "P4"}
var runbookStatusEnum = []string{"draft", "verified", "outdated"}
var runbookAutomationLevelEnum = []string{"manual", "assisted", "autonomous-with-gate"}
var runbookSourceEnum = []string{"knowledge-mcp", "vault-fs"}
var matchModeEnum = []string{"all", "any"}
var contextPackSectionEnum = []string{"header", "card", "pointers", "pinned", "observations", "evidence", "runbooks", "refs", "footer"}
var contextPackFormatEnum = []string{"markdown", "json"}

func enumContains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

// ─── Argument helpers (complement intArg/boolArg in mcp.go) ─────────────────

func optString(req mcp.CallToolRequest, key string) string {
	v, _ := req.GetArguments()[key].(string)
	return v
}

func optStringPtr(req mcp.CallToolRequest, key string) *string {
	v, ok := req.GetArguments()[key].(string)
	if !ok || v == "" {
		return nil
	}
	return &v
}

func optInt64Ptr(req mcp.CallToolRequest, key string) *int64 {
	v, ok := req.GetArguments()[key].(float64)
	if !ok {
		return nil
	}
	iv := int64(v)
	return &iv
}

func optBoolPtr(req mcp.CallToolRequest, key string) *bool {
	v, ok := req.GetArguments()[key].(bool)
	if !ok {
		return nil
	}
	return &v
}

func hasArg(req mcp.CallToolRequest, key string) bool {
	_, ok := req.GetArguments()[key]
	return ok
}

func optStringSlice(req mcp.CallToolRequest, key string) []string {
	raw, ok := req.GetArguments()[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func clampInt(v, min, max, def int) int {
	if v == 0 {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// ─── Envelope helpers ────────────────────────────────────────────────────────

// respondProjectResult builds the {"project","project_source","project_path","result"}
// envelope shared by every engram-projects tool (RFC §5.0). result may be any
// JSON-marshalable value: a structured object (card, task, ...) or a plain
// string (mem_context_pack's markdown format).
func respondProjectResult(res projectpkg.DetectionResult, result any) *mcp.CallToolResult {
	envelope := map[string]any{
		"project":        res.Project,
		"project_source": res.Source,
		"project_path":   res.Path,
		"result":         result,
	}
	if res.Warning != "" {
		envelope["warning"] = res.Warning
	}
	out, _ := jsonMarshal(envelope)
	return mcp.NewToolResultText(string(out))
}

// projectToolError builds the {"error", "code", ...fields} envelope used by
// every engram-projects tool error (RFC §5.0) — distinct from errorWithMeta's
// {"error_code","message",...} shape used by the rest of this package.
func projectToolError(code, message string, fields map[string]any) *mcp.CallToolResult {
	envelope := map[string]any{"error": message, "code": code}
	for k, v := range fields {
		envelope[k] = v
	}
	out, _ := jsonMarshal(envelope)
	result := mcp.NewToolResultText(string(out))
	result.IsError = true
	return result
}

// resolveProjectsToolReadProject resolves the project for a read-only
// engram-projects tool, returning a ready-to-send error result on failure.
func resolveProjectsToolReadProject(s *store.Store, cfg MCPConfig, override string) (projectpkg.DetectionResult, *mcp.CallToolResult) {
	res, err := resolveReadProjectWithProcessOverride(s, override, cfg.DefaultProject)
	if err == nil {
		res.Project, _ = store.NormalizeProject(res.Project)
		return res, nil
	}
	var upe *unknownProjectError
	if errors.As(err, &upe) {
		return res, projectToolError("unknown_project",
			fmt.Sprintf("Project %q not found in store", upe.Name),
			map[string]any{"available_projects": upe.AvailableProjects})
	}
	if errors.Is(err, projectpkg.ErrInvalidConfig) {
		return res, projectToolError("invalid_project_config", err.Error(), nil)
	}
	return res, projectToolError("ambiguous_project",
		fmt.Sprintf("Cannot determine project: %s", err),
		map[string]any{"available_projects": res.AvailableProjects})
}

// resolveProjectsToolCreateProject resolves the project for a tool that may
// create a brand-new project_cards/tasks row (mem_project_upsert,
// mem_task_upsert). Per RFC §5.0/§5.2: an explicit project is accepted when
// it is already backed by a card or observations, OR when it matches the
// project detected from cwd/ENGRAM_PROJECT; otherwise the write is refused
// loudly rather than silently bucketing data under an unrelated project.
func resolveProjectsToolCreateProject(s *store.Store, cfg MCPConfig, explicit string) (projectpkg.DetectionResult, *mcp.CallToolResult) {
	if strings.TrimSpace(explicit) == "" {
		res, err := resolveReadProjectWithProcessOverride(s, "", cfg.DefaultProject)
		if err != nil {
			if errors.Is(err, projectpkg.ErrInvalidConfig) {
				return res, projectToolError("invalid_project_config", err.Error(), nil)
			}
			return res, projectToolError("ambiguous_project",
				fmt.Sprintf("Cannot determine project: %s", err),
				map[string]any{"available_projects": res.AvailableProjects})
		}
		res.Project, _ = store.NormalizeProject(res.Project)
		return res, nil
	}

	normalized, _ := store.NormalizeProject(explicit)
	if backed, _ := s.ProjectExists(normalized); backed {
		return projectpkg.DetectionResult{Project: normalized, Source: projectpkg.SourceExplicitOverride}, nil
	}
	if cardExists, _ := s.ProjectCardExists(normalized); cardExists {
		return projectpkg.DetectionResult{Project: normalized, Source: projectpkg.SourceExplicitOverride}, nil
	}

	detected, err := resolveWriteProjectWithProcessOverride(cfg.DefaultProject)
	if err == nil {
		detectedNormalized, _ := store.NormalizeProject(detected.Project)
		if detectedNormalized == normalized {
			detected.Project = normalized
			return detected, nil
		}
	}

	stats, _ := s.Stats()
	return projectpkg.DetectionResult{}, projectToolError("unknown_project",
		fmt.Sprintf("Project %q is not backed by an existing card or observations, and does not match the project detected from cwd/ENGRAM_PROJECT", normalized),
		map[string]any{"available_projects": stats.Projects})
}

// ─── Tool registration ───────────────────────────────────────────────────────

// registerProjectTools registers the 10 `projects` profile tools (RFC §5.11).
// mem_context_pack and mem_task_upsert are eager (no WithDeferLoading); the
// rest are deferred, matching serverInstructions' load-on-demand guidance.
func registerProjectTools(srv *server.MCPServer, s *store.Store, cfg MCPConfig, allowlist map[string]bool, writeQueue *writeQueue) {
	if shouldRegister("mem_project_card", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_project_card",
				mcp.WithDescription("Return a project's dashboard card: repo/knowledge/graph pointers, task/evidence/runbook counters, and cloud sync status. Call this before starting work on a project repo to orient yourself."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Project Card"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("project", mcp.Description("Slug; optional, resolved by precedence when omitted")),
				mcp.WithBoolean("include_counts", mcp.DefaultBool(true), mcp.Description("Include the counts section")),
				mcp.WithBoolean("include_graph_summary", mcp.DefaultBool(false), mcp.Description("Include the graph_summary blob on the card, when present")),
			),
			handleProjectCard(s, cfg),
		)
	}

	if shouldRegister("mem_project_upsert", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_project_upsert",
				mcp.WithDescription("Create or update a project's dashboard card. Idempotent: omitted fields are left untouched. With sync_graph=true, also stamps the code graph commit from graphify-out/graph.json."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Upsert Project Card"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("project", mcp.Pattern(`^[a-z0-9][a-z0-9-]{0,63}$`), mcp.Description("Project slug")),
				mcp.WithString("display_name", mcp.MaxLength(120)),
				mcp.WithString("repo_url", mcp.MaxLength(300)),
				mcp.WithString("default_branch", mcp.DefaultString("master")),
				mcp.WithString("jira_project", mcp.DefaultString("PROJ")),
				mcp.WithString("jira_component"),
				mcp.WithString("knowledge_hub_path", mcp.Description("Vault-relative path of the type: service hub")),
				mcp.WithString("owner"),
				mcp.WithString("graph_path", mcp.DefaultString("graphify-out/graph.json"), mcp.Description("Repo-relative path")),
				mcp.WithBoolean("sync_graph", mcp.DefaultBool(false)),
				mcp.WithString("repo_dir", mcp.Description("Absolute repo root used only when sync_graph=true; defaults to project_path")),
			),
			queuedWriteHandler(writeQueue, handleProjectUpsert(s, cfg)),
		)
	}

	if shouldRegister("mem_task_upsert", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_task_upsert",
				mcp.WithDescription("Create or update a task (Jira ticket or SDD change) inside a project. Upsert key precedence: sync_id -> jira_key -> (project, sdd_change) -> new. title and kind are required when creating."),
				mcp.WithTitleAnnotation("Upsert Task"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("project", mcp.Description("Slug; optional, resolved by precedence when omitted")),
				mcp.WithString("sync_id", mcp.Pattern(`^task-[0-9a-f]{16}$`)),
				mcp.WithString("jira_key", mcp.Pattern(`^[A-Z][A-Z0-9]+-[0-9]+$`)),
				mcp.WithString("sdd_change", mcp.Pattern(`^[a-z0-9][a-z0-9-]*$`)),
				mcp.WithString("title", mcp.MaxLength(200)),
				mcp.WithString("kind", mcp.Enum(taskKindEnum...)),
				mcp.WithString("state", mcp.Enum(taskStateEnum...)),
				mcp.WithString("jira_status", mcp.Description("Literal Jira status.name; derives state when state is omitted")),
				mcp.WithString("jira_status_category", mcp.Enum(jiraStatusCategoryEnum...)),
				mcp.WithString("branch"),
				mcp.WithString("pr_url"),
				mcp.WithString("knowledge_ref"),
				mcp.WithString("assignee"),
			),
			queuedWriteHandler(writeQueue, handleTaskUpsert(s, cfg)),
		)
	}

	if shouldRegister("mem_task_list", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_task_list",
				mcp.WithDescription("List tasks in a project with filters and Jira-mirror freshness (state_stale)."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("List Tasks"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("project", mcp.Description("Slug; optional, resolved by precedence when omitted")),
				mcp.WithString("state", mcp.Enum(taskListStateEnum...), mcp.DefaultString("active"), mcp.Description("active = every state except done and cancelled")),
				mcp.WithString("kind", mcp.Enum(taskKindEnum...)),
				mcp.WithString("jira_key"),
				mcp.WithString("query", mcp.Description("FTS5 over tasks_fts (title, jira_key, sdd_change, branch)")),
				mcp.WithNumber("limit", mcp.Min(1), mcp.Max(100), mcp.DefaultNumber(20)),
				mcp.WithNumber("offset", mcp.Min(0), mcp.DefaultNumber(0)),
				mcp.WithNumber("stale_after_hours", mcp.Min(1), mcp.DefaultNumber(24)),
			),
			handleTaskList(s, cfg),
		)
	}

	if shouldRegister("mem_task_link", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_task_link",
				mcp.WithDescription("Link an existing observation to a task and optionally record external references (knowledge_ref, graph_ref+graph_commit, runbook_id, jira_ref). The only way to stamp graph facts and knowledge_ref without touching mem_save."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Link Task Observation"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("task", mcp.Required(), mcp.Description("PROJ-10336 | task-<hex> | #42 | change:<sdd_change>")),
				mcp.WithNumber("observation_id", mcp.Min(1)),
				mcp.WithString("observation_sync_id", mcp.Pattern(`^obs-[0-9a-f]{16,32}$`)),
				mcp.WithString("role", mcp.Enum(taskLinkRoleEnum...), mcp.DefaultString("context")),
				mcp.WithString("knowledge_ref", mcp.Description("Vault-relative path documenting the fact")),
				mcp.WithString("graph_ref", mcp.Description("Symbol or community label taken from graphify")),
				mcp.WithString("graph_commit", mcp.Pattern(`^[0-9a-f]{40}$`), mcp.Description("Required when graph_ref is present")),
				mcp.WithString("runbook_id", mcp.Pattern(`^RB-[0-9]{3}$`)),
				mcp.WithString("jira_ref", mcp.Pattern(`^[A-Z][A-Z0-9]+-[0-9]+$`)),
			),
			queuedWriteHandler(writeQueue, handleTaskLink(s, cfg)),
		)
	}

	if shouldRegister("mem_evidence_add", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_evidence_add",
				mcp.WithDescription("Register an already-captured evidence file (D-06). Idempotent by (task, sha256)."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Add Evidence"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("task", mcp.Required()),
				mcp.WithString("path", mcp.Required(), mcp.Description("Relative to ${CD_EVIDENCE_DIR:-~/.clarodrive/evidence}; absolute paths are rejected")),
				mcp.WithString("sha256", mcp.Required(), mcp.Pattern(`^[0-9a-f]{64}$`)),
				mcp.WithString("kind", mcp.Required(), mcp.Enum(evidenceKindEnum...)),
				mcp.WithString("proves", mcp.Required(), mcp.MaxLength(300)),
				mcp.WithString("config_stamp", mcp.MaxLength(300), mcp.Description("Observed configuration value stamped inside the capture")),
				mcp.WithString("captured_at", mcp.Description("ISO-8601; defaults to now")),
				mcp.WithNumber("size_bytes", mcp.Min(0)),
				mcp.WithString("manifest_path", mcp.Description("Relative path of manifest.json for the ticket")),
				mcp.WithBoolean("attached_jira", mcp.DefaultBool(false)),
				mcp.WithString("attached_confluence_url"),
			),
			queuedWriteHandler(writeQueue, handleEvidenceAdd(s, cfg)),
		)
	}

	if shouldRegister("mem_evidence_list", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_evidence_list",
				mcp.WithDescription("List evidence for a project, or for one task when task is given."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("List Evidence"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("project", mcp.Description("Slug; optional, resolved by precedence when omitted")),
				mcp.WithString("task"),
				mcp.WithBoolean("attached_jira"),
				mcp.WithString("kind", mcp.Enum(evidenceKindEnum...)),
				mcp.WithNumber("limit", mcp.Min(1), mcp.Max(200), mcp.DefaultNumber(50)),
				mcp.WithNumber("offset", mcp.Min(0), mcp.DefaultNumber(0)),
			),
			handleEvidenceList(s, cfg),
		)
	}

	if shouldRegister("mem_runbook_index_sync", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_runbook_index_sync",
				mcp.WithDescription("Rebuild the runbook index from entries obtained from the knowledge vault (search_by_metadata/get_document/get_stale_docs, or a vault-fs checkout). Filters templates and recomputes exec_count/last_exec_at from runbook/RB-NNN/exec/% observations."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Sync Runbook Index"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("project", mcp.Description("Optional filter; entries carry their own service")),
				mcp.WithString("source", mcp.Enum(runbookSourceEnum...), mcp.DefaultString("knowledge-mcp")),
				mcp.WithBoolean("prune_missing", mcp.DefaultBool(false), mcp.Description("Delete index rows for the given services that are absent from entries")),
				mcp.WithArray("entries", mcp.Required(), mcp.MinItems(1), mcp.MaxItems(500),
					mcp.Items(map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":               map[string]any{"type": "string", "pattern": "^RB-[0-9]{3}$"},
							"vault_path":       map[string]any{"type": "string"},
							"title":            map[string]any{"type": "string"},
							"service":          map[string]any{"type": "string"},
							"category":         map[string]any{"type": "string", "enum": runbookCategoryEnum},
							"pattern":          map[string]any{"type": "string", "enum": runbookPatternEnum},
							"severity":         map[string]any{"type": "string", "enum": runbookSeverityEnum},
							"status":           map[string]any{"type": "string", "enum": runbookStatusEnum},
							"symptoms":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"tags":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"owner":            map[string]any{"type": "string"},
							"automation_level": map[string]any{"type": "string", "enum": runbookAutomationLevelEnum},
							"last_updated":     map[string]any{"type": "string"},
							"last_verified":    map[string]any{"type": "string"},
							"needs_review":     map[string]any{"type": "boolean"},
							"age_days":         map[string]any{"type": "integer", "minimum": 0},
						},
						"required":             []string{"id", "vault_path", "title", "service", "category", "status"},
						"additionalProperties": false,
					}),
				),
			),
			queuedWriteHandler(writeQueue, handleRunbookIndexSync(s)),
		)
	}

	if shouldRegister("mem_runbook_find", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_runbook_find",
				mcp.WithDescription("Find candidate runbooks by symptoms using BM25 over the runbook index."),
				mcp.WithDeferLoading(true),
				mcp.WithTitleAnnotation("Find Runbooks"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("query", mcp.Required(), mcp.MinLength(3), mcp.Description("Error text, log line or symptom")),
				mcp.WithString("project", mcp.Description("Optional; omit to search across services")),
				mcp.WithString("category", mcp.Enum(runbookCategoryEnum...)),
				mcp.WithString("pattern", mcp.Enum(runbookPatternEnum...)),
				mcp.WithBoolean("include_stale", mcp.DefaultBool(true)),
				mcp.WithString("match_mode", mcp.Enum(matchModeEnum...), mcp.DefaultString("any")),
				mcp.WithNumber("limit", mcp.Min(1), mcp.Max(20), mcp.DefaultNumber(5)),
			),
			handleRunbookFind(s),
		)
	}

	if shouldRegister("mem_context_pack", allowlist) {
		srv.AddTool(
			mcp.NewTool("mem_context_pack",
				mcp.WithDescription("Compose a task's context pack (card, pointers, pinned decisions, linked observations, evidence, candidate runbooks, refs) as markdown or JSON, budgeted to max_chars. Call this before starting work on a ticket so you don't rediscover context another session already found."),
				mcp.WithTitleAnnotation("Context Pack"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("task", mcp.Required()),
				mcp.WithString("project", mcp.Description("Slug; optional, resolved by precedence when omitted")),
				mcp.WithNumber("max_chars", mcp.Min(2000), mcp.Max(40000), mcp.DefaultNumber(12000)),
				mcp.WithNumber("observations_limit", mcp.Min(1), mcp.Max(30), mcp.DefaultNumber(8)),
				mcp.WithNumber("observation_chars", mcp.Min(200), mcp.Max(4000), mcp.DefaultNumber(600)),
				mcp.WithArray("sections", mcp.Items(map[string]any{"type": "string", "enum": contextPackSectionEnum}),
					mcp.Description("Defaults to all sections in this order")),
				mcp.WithBoolean("include_runbooks", mcp.DefaultBool(true)),
				mcp.WithString("format", mcp.Enum(contextPackFormatEnum...), mcp.DefaultString("markdown")),
			),
			handleContextPack(s, cfg),
		)
	}
}

// ─── 5.1 mem_project_card ────────────────────────────────────────────────────

func handleProjectCard(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		detRes, errResult := resolveProjectsToolReadProject(s, cfg, optString(req, "project"))
		if errResult != nil {
			return errResult, nil
		}
		project := detRes.Project

		card, err := s.GetProjectCard(project)
		if errors.Is(err, store.ErrNoProjectCard) {
			return projectToolError("no_card", fmt.Sprintf("no project card for %s", project),
				map[string]any{"hint": "call mem_project_upsert"}), nil
		}
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if !optBoolDefault(req, "include_graph_summary", false) {
			card.GraphSummary = nil
		}

		result := map[string]any{"card": card}
		if optBoolDefault(req, "include_counts", true) {
			counts, err := s.ProjectCardCounts(project)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			result["counts"] = counts
		}
		sync, err := s.ProjectSyncSummary(project)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		result["sync"] = sync

		return respondProjectResult(detRes, result), nil
	}
}

func optBoolDefault(req mcp.CallToolRequest, key string, def bool) bool {
	v, ok := req.GetArguments()[key].(bool)
	if !ok {
		return def
	}
	return v
}

// ─── 5.2 mem_project_upsert ──────────────────────────────────────────────────

func handleProjectUpsert(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		detRes, errResult := resolveProjectsToolCreateProject(s, cfg, optString(req, "project"))
		if errResult != nil {
			return errResult, nil
		}
		project := detRes.Project
		if !isValidProjectSlug(project) {
			return projectToolError("invalid_slug", fmt.Sprintf("invalid project slug %q", project), nil), nil
		}

		params := store.UpsertProjectCardParams{
			Slug:             project,
			DisplayName:      optStringPtr(req, "display_name"),
			RepoURL:          optStringPtr(req, "repo_url"),
			DefaultBranch:    optStringPtr(req, "default_branch"),
			JiraProject:      optStringPtr(req, "jira_project"),
			JiraComponent:    optStringPtr(req, "jira_component"),
			KnowledgeHubPath: optStringPtr(req, "knowledge_hub_path"),
			Owner:            optStringPtr(req, "owner"),
			GraphPath:        optStringPtr(req, "graph_path"),
		}
		card, created, err := s.UpsertProjectCard(params)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result := map[string]any{"card": card, "created": created}

		if optBoolDefault(req, "sync_graph", false) {
			repoDir := optString(req, "repo_dir")
			if repoDir == "" {
				repoDir = detRes.Path
			}
			graphResult, err := s.SyncProjectGraph(project, repoDir, card.GraphPath)
			switch {
			case errors.Is(err, store.ErrGraphNotFound):
				return projectToolError("graph_not_found", "graph.json not found", map[string]any{"repo_dir": repoDir, "graph_path": card.GraphPath}), nil
			case errors.Is(err, store.ErrGraphMissingCommit):
				return projectToolError("graph_missing_commit", "graph.json has no built_at_commit; no graph fact was persisted", nil), nil
			case err != nil:
				return mcp.NewToolResultError(err.Error()), nil
			}
			result["graph"] = graphResult
			card, err = s.GetProjectCard(project)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			result["card"] = card
		}

		return respondProjectResult(detRes, result), nil
	}
}

// ─── 5.3 mem_task_upsert ─────────────────────────────────────────────────────

func handleTaskUpsert(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		syncID := optStringPtr(req, "sync_id")
		jiraKey := optStringPtr(req, "jira_key")
		sddChange := optStringPtr(req, "sdd_change")
		if syncID == nil && jiraKey == nil && sddChange == nil {
			return projectToolError("missing_field", "one of sync_id, jira_key, or sdd_change is required", nil), nil
		}
		if syncID != nil && !taskSyncIDToolPattern.MatchString(*syncID) {
			return projectToolError("invalid_enum", fmt.Sprintf("sync_id %q does not match task-<16 hex>", *syncID), nil), nil
		}
		if jiraKey != nil && !jiraKeyToolPattern.MatchString(*jiraKey) {
			return projectToolError("invalid_enum", fmt.Sprintf("jira_key %q is not a valid Jira key", *jiraKey), nil), nil
		}
		if sddChange != nil && !sddChangePattern.MatchString(*sddChange) {
			return projectToolError("invalid_enum", fmt.Sprintf("sdd_change %q is not a valid slug", *sddChange), nil), nil
		}
		if kind := optString(req, "kind"); kind != "" && !enumContains(taskKindEnum, kind) {
			return projectToolError("invalid_enum", fmt.Sprintf("kind %q is invalid", kind), nil), nil
		}
		if state := optString(req, "state"); state != "" && !enumContains(taskStateEnum, state) {
			return projectToolError("invalid_enum", fmt.Sprintf("state %q is invalid", state), nil), nil
		}
		if cat := optString(req, "jira_status_category"); cat != "" && !enumContains(jiraStatusCategoryEnum, cat) {
			return projectToolError("invalid_enum", fmt.Sprintf("jira_status_category %q is invalid", cat), nil), nil
		}

		detRes, errResult := resolveProjectsToolCreateProject(s, cfg, optString(req, "project"))
		if errResult != nil {
			return errResult, nil
		}

		params := store.UpsertTaskParams{
			Project:            detRes.Project,
			SyncID:             syncID,
			JiraKey:            jiraKey,
			SDDChange:          sddChange,
			Title:              optStringPtr(req, "title"),
			Kind:               optStringPtr(req, "kind"),
			State:              optStringPtr(req, "state"),
			JiraStatus:         optStringPtr(req, "jira_status"),
			JiraStatusCategory: optStringPtr(req, "jira_status_category"),
			Branch:             optStringPtr(req, "branch"),
			PRUrl:              optStringPtr(req, "pr_url"),
			KnowledgeRef:       optStringPtr(req, "knowledge_ref"),
			Assignee:           optStringPtr(req, "assignee"),
		}
		result, err := s.UpsertTask(params)
		if err != nil {
			var missing *store.MissingFieldError
			if errors.As(err, &missing) {
				return projectToolError("missing_field", missing.Error(), map[string]any{"field": missing.Field}), nil
			}
			var conflict *store.TaskKeyConflictError
			if errors.As(err, &conflict) {
				return projectToolError("task_key_conflict", conflict.Error(),
					map[string]any{"existing_project": conflict.ExistingProject}), nil
			}
			return mcp.NewToolResultError(err.Error()), nil
		}

		return respondProjectResult(detRes, map[string]any{
			"task": result.Task, "created": result.Created, "card_created": result.CardCreated,
		}), nil
	}
}

// ─── 5.4 mem_task_list ───────────────────────────────────────────────────────

func handleTaskList(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		detRes, errResult := resolveProjectsToolReadProject(s, cfg, optString(req, "project"))
		if errResult != nil {
			return errResult, nil
		}

		state := optString(req, "state")
		if state != "" && !enumContains(taskListStateEnum, state) {
			return projectToolError("invalid_enum", fmt.Sprintf("state %q is invalid", state), nil), nil
		}
		if kind := optString(req, "kind"); kind != "" && !enumContains(taskKindEnum, kind) {
			return projectToolError("invalid_enum", fmt.Sprintf("kind %q is invalid", kind), nil), nil
		}

		filter := store.TaskListFilter{
			State:           state,
			Kind:            optString(req, "kind"),
			JiraKey:         optString(req, "jira_key"),
			Query:           optString(req, "query"),
			Limit:           clampInt(intArg(req, "limit", 20), 1, 100, 20),
			Offset:          intArg(req, "offset", 0),
			StaleAfterHours: clampInt(intArg(req, "stale_after_hours", 24), 1, 1<<30, 24),
		}
		items, total, err := s.ListTasks(detRes.Project, filter)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return respondProjectResult(detRes, map[string]any{
			"items": items, "total": total, "limit": filter.Limit, "offset": filter.Offset,
		}), nil
	}
}

// ─── 5.5 mem_task_link ───────────────────────────────────────────────────────

func handleTaskLink(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskRef := optString(req, "task")
		if strings.TrimSpace(taskRef) == "" {
			return projectToolError("missing_field", "task is required", nil), nil
		}
		observationID := optInt64Ptr(req, "observation_id")
		observationSyncID := optStringPtr(req, "observation_sync_id")
		if observationID == nil && observationSyncID == nil {
			return projectToolError("missing_field", "one of observation_id or observation_sync_id is required", nil), nil
		}
		if observationSyncID != nil && !obsSyncIDPattern.MatchString(*observationSyncID) {
			return projectToolError("invalid_enum", fmt.Sprintf("observation_sync_id %q is invalid", *observationSyncID), nil), nil
		}
		role := optString(req, "role")
		if role != "" && !enumContains(taskLinkRoleEnum, role) {
			return projectToolError("invalid_enum", fmt.Sprintf("role %q is invalid", role), nil), nil
		}

		detRes, errResult := resolveProjectsToolReadProject(s, cfg, "")
		if errResult != nil {
			return errResult, nil
		}

		task, err := s.ResolveTaskRef(detRes.Project, taskRef)
		if errors.Is(err, store.ErrUnknownTask) {
			return projectToolError("unknown_task", fmt.Sprintf("task %q not found in project %s", taskRef, detRes.Project), nil), nil
		}
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		obsID := int64(0)
		if observationID != nil {
			obsID = *observationID
		} else {
			obs, err := s.GetObservationBySyncID(*observationSyncID)
			if err != nil {
				return projectToolError("unknown_observation", fmt.Sprintf("observation %q not found", *observationSyncID), nil), nil
			}
			obsID = obs.ID
		}

		result, err := s.LinkTaskObservation(store.LinkTaskObservationParams{
			Task:          task,
			ObservationID: obsID,
			Role:          role,
			KnowledgeRef:  optStringPtr(req, "knowledge_ref"),
			GraphRef:      optStringPtr(req, "graph_ref"),
			GraphCommit:   optStringPtr(req, "graph_commit"),
			RunbookID:     optStringPtr(req, "runbook_id"),
			JiraRef:       optStringPtr(req, "jira_ref"),
		})
		switch {
		case errors.Is(err, store.ErrUnknownObservation):
			return projectToolError("unknown_observation", "observation not found", nil), nil
		case errors.Is(err, store.ErrCrossProjectLink):
			return projectToolError("cross_project_link", "observation and task belong to different projects", nil), nil
		case errors.Is(err, store.ErrGraphCommitRequired):
			return projectToolError("graph_commit_required", "graph_ref requires graph_commit", nil), nil
		case err != nil:
			return mcp.NewToolResultError(err.Error()), nil
		}

		return respondProjectResult(detRes, map[string]any{
			"linked": result.Linked, "task_sync_id": result.TaskSyncID, "observation_sync_id": result.ObservationSyncID,
			"role": result.Role, "refs_added": result.RefsAdded, "refs": result.Refs,
		}), nil
	}
}

// ─── 5.6 mem_evidence_add ────────────────────────────────────────────────────

func handleEvidenceAdd(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskRef := optString(req, "task")
		path := optString(req, "path")
		sha256 := optString(req, "sha256")
		kind := optString(req, "kind")
		proves := optString(req, "proves")
		if strings.TrimSpace(taskRef) == "" || strings.TrimSpace(path) == "" || strings.TrimSpace(sha256) == "" ||
			strings.TrimSpace(kind) == "" || strings.TrimSpace(proves) == "" {
			return projectToolError("missing_field", "task, path, sha256, kind, and proves are required", nil), nil
		}
		if !sha256ToolPattern.MatchString(sha256) {
			return projectToolError("invalid_sha256", fmt.Sprintf("sha256 %q is not 64 lowercase hex chars", sha256), nil), nil
		}
		if !enumContains(evidenceKindEnum, kind) {
			return projectToolError("invalid_enum", fmt.Sprintf("kind %q is invalid", kind), nil), nil
		}
		if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "~") {
			return projectToolError("absolute_path_rejected", fmt.Sprintf("path %q must be relative to the evidence directory", path), nil), nil
		}

		detRes, errResult := resolveProjectsToolReadProject(s, cfg, "")
		if errResult != nil {
			return errResult, nil
		}
		task, err := s.ResolveTaskRef(detRes.Project, taskRef)
		if errors.Is(err, store.ErrUnknownTask) {
			return projectToolError("unknown_task", fmt.Sprintf("task %q not found in project %s", taskRef, detRes.Project), nil), nil
		}
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		evidence, duplicate, limits, err := s.AddEvidence(store.AddEvidenceParams{
			Task:                  task,
			Path:                  path,
			SHA256:                sha256,
			Kind:                  kind,
			Proves:                proves,
			ConfigStamp:           optStringPtr(req, "config_stamp"),
			CapturedAt:            optStringPtr(req, "captured_at"),
			SizeBytes:             optInt64Ptr(req, "size_bytes"),
			ManifestPath:          optStringPtr(req, "manifest_path"),
			AttachedJira:          optBoolDefault(req, "attached_jira", false),
			AttachedConfluenceURL: optStringPtr(req, "attached_confluence_url"),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return respondProjectResult(detRes, map[string]any{
			"evidence": evidence, "duplicate": duplicate, "limits": limits,
		}), nil
	}
}

// ─── 5.7 mem_evidence_list ───────────────────────────────────────────────────

func handleEvidenceList(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		detRes, errResult := resolveProjectsToolReadProject(s, cfg, optString(req, "project"))
		if errResult != nil {
			return errResult, nil
		}

		filter := store.EvidenceListFilter{
			AttachedJira: optBoolPtr(req, "attached_jira"),
			Kind:         optString(req, "kind"),
			Limit:        clampInt(intArg(req, "limit", 50), 1, 200, 50),
			Offset:       intArg(req, "offset", 0),
		}
		if kind := filter.Kind; kind != "" && !enumContains(evidenceKindEnum, kind) {
			return projectToolError("invalid_enum", fmt.Sprintf("kind %q is invalid", kind), nil), nil
		}
		if taskRef := optString(req, "task"); taskRef != "" {
			task, err := s.ResolveTaskRef(detRes.Project, taskRef)
			if errors.Is(err, store.ErrUnknownTask) {
				return projectToolError("unknown_task", fmt.Sprintf("task %q not found in project %s", taskRef, detRes.Project), nil), nil
			}
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			filter.TaskSyncID = task.SyncID
		}

		items, total, totalBytes, err := s.ListEvidence(detRes.Project, filter)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return respondProjectResult(detRes, map[string]any{
			"items": items, "total": total, "total_bytes": totalBytes, "limit": filter.Limit, "offset": filter.Offset,
		}), nil
	}
}

// ─── 5.8 mem_runbook_index_sync ──────────────────────────────────────────────

func handleRunbookIndexSync(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		source := optString(req, "source")
		if source == "" {
			source = "knowledge-mcp"
		}
		if !enumContains(runbookSourceEnum, source) {
			return projectToolError("invalid_enum", fmt.Sprintf("source %q is invalid", source), nil), nil
		}

		rawEntries, ok := req.GetArguments()["entries"].([]any)
		if !ok || len(rawEntries) == 0 {
			return projectToolError("entries_rejected", "entries must be a non-empty array", nil), nil
		}
		if len(rawEntries) > 500 {
			return projectToolError("payload_too_large", "entries exceeds 500 items", nil), nil
		}
		if raw, err := json.Marshal(rawEntries); err == nil && len(raw) > 1<<20 {
			return projectToolError("payload_too_large", "entries payload exceeds 1 MiB", nil), nil
		}

		entries, rejected := parseRunbookEntries(rawEntries)

		result, err := s.SyncRunbookIndex(store.RunbookIndexSyncParams{
			Project:      optString(req, "project"),
			Source:       source,
			PruneMissing: optBoolDefault(req, "prune_missing", false),
			Entries:      entries,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		result.Skipped = append(rejected, result.Skipped...)

		// entries_rejected only fires when every entry failed structural
		// parsing (missing required top-level fields). Template/invalid_status/
		// missing_service are expected business-rule filtering, not malformed
		// input, and must still return a normal success envelope with the
		// filtered rows surfaced in `skipped` (RFC §5.8's own example does
		// exactly this for a payload mixing valid entries and one template).
		if len(rejected) == len(rawEntries) {
			return projectToolError("entries_rejected", "all entries were rejected", map[string]any{"skipped": result.Skipped}), nil
		}

		out, _ := jsonMarshal(result)
		return mcp.NewToolResultText(string(out)), nil
	}
}

// parseRunbookEntries decodes the raw `entries` argument into typed structs.
// Entries missing a required field are reported directly as rejected rather
// than forwarded to the store layer.
func parseRunbookEntries(raw []any) ([]store.RunbookIndexEntryInput, []store.RunbookSkipped) {
	var entries []store.RunbookIndexEntryInput
	var rejected []store.RunbookSkipped
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			rejected = append(rejected, store.RunbookSkipped{Reason: "invalid_entry"})
			continue
		}
		id, _ := m["id"].(string)
		vaultPath, _ := m["vault_path"].(string)
		title, _ := m["title"].(string)
		service, _ := m["service"].(string)
		category, _ := m["category"].(string)
		status, _ := m["status"].(string)
		if id == "" || vaultPath == "" || title == "" || service == "" || category == "" || status == "" {
			rejected = append(rejected, store.RunbookSkipped{ID: id, VaultPath: vaultPath, Reason: "missing_field"})
			continue
		}
		entry := store.RunbookIndexEntryInput{
			ID: id, VaultPath: vaultPath, Title: title, Service: service, Category: category, Status: status,
		}
		entry.Pattern, _ = m["pattern"].(string)
		entry.Severity, _ = m["severity"].(string)
		entry.Owner, _ = m["owner"].(string)
		entry.AutomationLevel, _ = m["automation_level"].(string)
		entry.LastUpdated, _ = m["last_updated"].(string)
		entry.LastVerified, _ = m["last_verified"].(string)
		if v, ok := m["symptoms"].([]any); ok {
			for _, s := range v {
				if str, ok := s.(string); ok {
					entry.Symptoms = append(entry.Symptoms, str)
				}
			}
		}
		if v, ok := m["tags"].([]any); ok {
			for _, s := range v {
				if str, ok := s.(string); ok {
					entry.Tags = append(entry.Tags, str)
				}
			}
		}
		if v, ok := m["needs_review"].(bool); ok {
			entry.NeedsReview = &v
		}
		if v, ok := m["age_days"].(float64); ok {
			iv := int(v)
			entry.AgeDays = &iv
		}
		entries = append(entries, entry)
	}
	return entries, rejected
}

// ─── 5.9 mem_runbook_find ────────────────────────────────────────────────────

func handleRunbookFind(s *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := optString(req, "query")
		if strings.TrimSpace(query) == "" {
			return projectToolError("missing_field", "query is required", nil), nil
		}
		project := ""
		if raw := optString(req, "project"); raw != "" {
			normalized, _ := store.NormalizeProject(raw)
			exists, _ := s.ProjectExists(normalized)
			cardExists, _ := s.ProjectCardExists(normalized)
			if !exists && !cardExists {
				stats, _ := s.Stats()
				return projectToolError("unknown_project", fmt.Sprintf("Project %q not found in store", normalized),
					map[string]any{"available_projects": stats.Projects}), nil
			}
			project = normalized
		}
		category := optString(req, "category")
		if category != "" && !enumContains(runbookCategoryEnum, category) {
			return projectToolError("invalid_enum", fmt.Sprintf("category %q is invalid", category), nil), nil
		}
		pattern := optString(req, "pattern")
		if pattern != "" && !enumContains(runbookPatternEnum, pattern) {
			return projectToolError("invalid_enum", fmt.Sprintf("pattern %q is invalid", pattern), nil), nil
		}
		matchMode := optString(req, "match_mode")
		if matchMode == "" {
			matchMode = "any"
		}
		if !enumContains(matchModeEnum, matchMode) {
			return projectToolError("invalid_enum", fmt.Sprintf("match_mode %q is invalid", matchMode), nil), nil
		}

		items, total, err := s.FindRunbooks(store.RunbookFindParams{
			Query:        query,
			Project:      project,
			Category:     category,
			Pattern:      pattern,
			IncludeStale: optBoolDefault(req, "include_stale", true),
			MatchMode:    matchMode,
			Limit:        clampInt(intArg(req, "limit", 5), 1, 20, 5),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, _ := jsonMarshal(map[string]any{"items": items, "total": total})
		return mcp.NewToolResultText(string(out)), nil
	}
}

// ─── 5.10 mem_context_pack ───────────────────────────────────────────────────

func handleContextPack(s *store.Store, cfg MCPConfig) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskRef := optString(req, "task")
		if strings.TrimSpace(taskRef) == "" {
			return projectToolError("missing_field", "task is required", nil), nil
		}
		format := optString(req, "format")
		if format == "" {
			format = "markdown"
		}
		if !enumContains(contextPackFormatEnum, format) {
			return projectToolError("invalid_enum", fmt.Sprintf("format %q is invalid", format), nil), nil
		}
		sections := optStringSlice(req, "sections")
		for _, sec := range sections {
			if !enumContains(contextPackSectionEnum, sec) {
				return projectToolError("invalid_enum", fmt.Sprintf("section %q is invalid", sec), nil), nil
			}
		}

		detRes, errResult := resolveProjectsToolReadProject(s, cfg, optString(req, "project"))
		if errResult != nil {
			return errResult, nil
		}

		opts := projectpkg.DefaultContextPackOptions()
		opts.MaxChars = clampInt(intArg(req, "max_chars", opts.MaxChars), 2000, 40000, opts.MaxChars)
		opts.ObservationsLimit = clampInt(intArg(req, "observations_limit", opts.ObservationsLimit), 1, 30, opts.ObservationsLimit)
		opts.ObservationChars = clampInt(intArg(req, "observation_chars", opts.ObservationChars), 200, 4000, opts.ObservationChars)
		opts.Sections = sections
		opts.IncludeRunbooks = optBoolDefault(req, "include_runbooks", true)
		opts.Format = format

		pack, rendered, err := projectpkg.BuildContextPack(s, detRes.Project, taskRef, opts)
		if errors.Is(err, store.ErrUnknownTask) {
			return projectToolError("unknown_task", fmt.Sprintf("task %q not found in project %s", taskRef, detRes.Project), nil), nil
		}
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if format == "json" {
			return respondProjectResult(detRes, pack), nil
		}
		return respondProjectResult(detRes, rendered), nil
	}
}
