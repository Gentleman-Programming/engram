package project

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// jiraBaseURL is the Jira Cloud browse base used to build task links. It is
// read from the environment so a deployment is not tied to one Jira tenant:
// set ENGRAM_JIRA_BASE_URL to your own instance. See RFC
// rfc-engram-projects.md §5.10.
var jiraBaseURL = jiraBaseURLFromEnv()

func jiraBaseURLFromEnv() string {
	v := strings.TrimSpace(os.Getenv("ENGRAM_JIRA_BASE_URL"))
	if v == "" {
		return "https://your-org.atlassian.net/browse/"
	}
	if !strings.HasSuffix(v, "/") {
		v += "/"
	}
	return v
}

// ContextPackOptions holds mem_context_pack's tunable parameters.
type ContextPackOptions struct {
	MaxChars          int
	ObservationsLimit int
	ObservationChars  int
	Sections          []string // nil/empty = every canonical section
	IncludeRunbooks   bool
	Format            string // "markdown" | "json"
	RepoDir           string // optional: enables the graph HEAD staleness check
}

// DefaultContextPackOptions mirrors mem_context_pack's JSON Schema defaults.
func DefaultContextPackOptions() ContextPackOptions {
	return ContextPackOptions{
		MaxChars:          12000,
		ObservationsLimit: 8,
		ObservationChars:  600,
		IncludeRunbooks:   true,
		Format:            "markdown",
	}
}

// canonicalSections is the fixed rendering order (RFC §5.10 table).
var canonicalSections = []string{"header", "card", "pointers", "pinned", "observations", "evidence", "runbooks", "refs", "footer"}

var sectionBudgetPct = map[string]float64{
	"header": 0.04, "card": 0.05, "pointers": 0.06, "pinned": 0.15, "observations": 0.40,
	"evidence": 0.08, "runbooks": 0.10, "refs": 0.07, "footer": 0.05,
}

// droppableInReverseOrder is the section removal order applied when the
// composed pack still exceeds MaxChars after per-section truncation.
var droppableInReverseOrder = []string{"refs", "runbooks", "evidence", "pinned"}

// ContextPack is BuildContextPack's structured result (format=json).
type ContextPack struct {
	Task         map[string]any      `json:"task,omitempty"`
	Card         map[string]any      `json:"card,omitempty"`
	Pointers     map[string]any      `json:"pointers,omitempty"`
	Pinned       []map[string]any    `json:"pinned,omitempty"`
	Observations []map[string]any    `json:"observations,omitempty"`
	Evidence     []map[string]any    `json:"evidence,omitempty"`
	Runbooks     []map[string]any    `json:"runbooks,omitempty"`
	Refs         map[string][]string `json:"refs,omitempty"`
	Truncated    bool                `json:"truncated"`
	Chars        int                 `json:"chars"`
}

// BuildContextPack composes the context pack for a task (RFC §5.10). It
// returns the structured pack (used for format=json) and the equivalent
// rendered markdown (used for format=markdown); both share the same
// underlying data and section selection.
func BuildContextPack(s *store.Store, project, taskRef string, opts ContextPackOptions) (*ContextPack, string, error) {
	task, err := s.ResolveTaskRef(project, taskRef)
	if err != nil {
		return nil, "", err
	}

	sections := canonicalSections
	if len(opts.Sections) > 0 {
		requested := map[string]bool{}
		for _, sec := range opts.Sections {
			requested[sec] = true
		}
		var filtered []string
		for _, sec := range canonicalSections {
			if requested[sec] {
				filtered = append(filtered, sec)
			}
		}
		sections = filtered
	}
	want := map[string]bool{}
	for _, sec := range sections {
		want[sec] = true
	}

	pack := &ContextPack{}
	blocks := map[string]string{}
	truncatedSections := map[string]bool{}

	budget := func(sec string) int {
		b := int(float64(opts.MaxChars) * sectionBudgetPct[sec])
		if b < 40 {
			b = 40
		}
		return b
	}
	fit := func(sec, text string) string {
		b := budget(sec)
		if len([]rune(text)) <= b {
			return text
		}
		truncatedSections[sec] = true
		r := []rune(text)
		if b <= 1 {
			return "…"
		}
		return string(r[:b-1]) + "…"
	}

	card, cardErr := s.GetProjectCard(project)
	hasCard := cardErr == nil

	// ─── 1. header ──────────────────────────────────────────────────────
	if want["header"] {
		key := taskKey(task)
		var line1 strings.Builder
		fmt.Fprintf(&line1, "**Context pack — %s · %s**\n", key, task.Title)
		fmt.Fprintf(&line1, "kind: %s · state: %s", task.Kind, task.State)
		if task.JiraStatus != nil {
			fmt.Fprintf(&line1, " (Jira: %s", *task.JiraStatus)
			if task.StateSyncedAt != nil {
				fmt.Fprintf(&line1, ", sincronizado %s UTC", *task.StateSyncedAt)
			}
			line1.WriteString(")")
		}
		if task.Branch != nil {
			fmt.Fprintf(&line1, " · branch: %s", *task.Branch)
		}
		if isTaskStateStale(task.StateSyncedAt) {
			line1.WriteString(" ⚠ state_stale")
		}
		blocks["header"] = fit("header", line1.String())
		pack.Task = map[string]any{
			"key": key, "title": task.Title, "kind": task.Kind, "state": task.State,
			"jira_status": task.JiraStatus, "state_synced_at": task.StateSyncedAt, "branch": task.Branch,
			"pr_url": task.PRUrl, "state_stale": isTaskStateStale(task.StateSyncedAt),
		}
	}

	// ─── 2. card ────────────────────────────────────────────────────────
	if want["card"] && hasCard {
		var b strings.Builder
		b.WriteString("**Proyecto**\n")
		fmt.Fprintf(&b, "%s", card.Slug)
		if card.RepoURL != nil {
			fmt.Fprintf(&b, " · %s", *card.RepoURL)
		}
		fmt.Fprintf(&b, " (%s)", card.DefaultBranch)
		if card.Owner != nil {
			fmt.Fprintf(&b, " · owner %s", *card.Owner)
		}
		fmt.Fprintf(&b, " · Jira %s", card.JiraProject)
		if card.JiraComponent != nil {
			fmt.Fprintf(&b, " / %s", *card.JiraComponent)
		}
		blocks["card"] = fit("card", b.String())
		pack.Card = map[string]any{
			"slug": card.Slug, "repo_url": card.RepoURL, "default_branch": card.DefaultBranch,
			"owner": card.Owner, "jira_project": card.JiraProject, "jira_component": card.JiraComponent,
		}
	}

	// ─── 3. pointers ────────────────────────────────────────────────────
	if want["pointers"] {
		var b strings.Builder
		b.WriteString("**Punteros**\n")
		pointers := map[string]any{}
		if hasCard && card.KnowledgeHubPath != nil {
			fmt.Fprintf(&b, "- Knowledge hub: %s\n", *card.KnowledgeHubPath)
			pointers["knowledge_hub_path"] = *card.KnowledgeHubPath
		}
		if hasCard && card.GraphCommit != nil {
			fmt.Fprintf(&b, "- Code graph: %s @ %s", card.GraphPath, shortCommit(*card.GraphCommit))
			if card.GraphBuiltAt != nil {
				fmt.Fprintf(&b, " (%s UTC)", *card.GraphBuiltAt)
			}
			if opts.RepoDir != "" {
				if head := gitHeadCommit(opts.RepoDir); head != "" {
					if head == *card.GraphCommit {
						b.WriteString(" · HEAD coincide")
					} else {
						b.WriteString(" · HEAD difiere (posible desactualización)")
						pointers["graph_stale"] = true
					}
				}
			}
			b.WriteString("\n")
			pointers["graph_path"] = card.GraphPath
			pointers["graph_commit"] = *card.GraphCommit
			pointers["graph_built_at"] = card.GraphBuiltAt
		}
		key := taskKey(task)
		var line3 []string
		if task.JiraKey != nil {
			line3 = append(line3, "Jira: "+jiraBaseURL+*task.JiraKey)
			pointers["jira_url"] = jiraBaseURL + *task.JiraKey
		}
		if task.SDDChange != nil {
			line3 = append(line3, "SDD: openspec/changes/"+*task.SDDChange+"/")
			pointers["sdd_path"] = "openspec/changes/" + *task.SDDChange + "/"
		}
		if len(line3) > 0 {
			fmt.Fprintf(&b, "- %s\n", strings.Join(line3, " · "))
		}
		var topicKeys []string
		if task.JiraKey != nil {
			topicKeys = append(topicKeys, "incident/"+*task.JiraKey, "evidence/"+*task.JiraKey)
		}
		if task.SDDChange != nil {
			topicKeys = append(topicKeys, "sdd/"+*task.SDDChange+"/*")
		}
		if len(topicKeys) > 0 {
			fmt.Fprintf(&b, "- Topic keys: %s\n", strings.Join(topicKeys, " · "))
			pointers["topic_keys"] = topicKeys
		}
		_ = key
		blocks["pointers"] = fit("pointers", strings.TrimRight(b.String(), "\n"))
		pack.Pointers = pointers
	}

	// ─── 4. pinned ──────────────────────────────────────────────────────
	if want["pinned"] {
		pinned, _ := s.PinnedObservations(project, "project")
		if len(pinned) > 10 {
			pinned = pinned[:10]
		}
		var b strings.Builder
		fmt.Fprintf(&b, "**Decisiones pinneadas del proyecto (%d de %d)**\n", len(pinned), len(pinned))
		for _, o := range pinned {
			fmt.Fprintf(&b, "- [#%d %s] %s\n", o.ID, o.Type, truncateRunes(o.Content, 160))
			pack.Pinned = append(pack.Pinned, map[string]any{
				"id": o.ID, "type": o.Type, "title": o.Title, "snippet": truncateRunes(o.Content, 160),
			})
		}
		blocks["pinned"] = fit("pinned", strings.TrimRight(b.String(), "\n"))
	}

	// ─── 5. observations ────────────────────────────────────────────────
	var includedObsSyncIDs []string
	if want["observations"] {
		limit := opts.ObservationsLimit
		if limit <= 0 {
			limit = 8
		}
		obsChars := opts.ObservationChars
		if obsChars <= 0 {
			obsChars = 600
		}
		linked, _ := s.TaskObservationsForTask(task.ID)
		total := len(linked)
		if len(linked) > limit {
			linked = linked[:limit]
		}
		if len(linked) < limit {
			seen := map[int64]bool{}
			for _, d := range linked {
				seen[d.Observation.ID] = true
			}
			var backfillPrefixes []string
			if task.JiraKey != nil {
				backfillPrefixes = append(backfillPrefixes, "incident/"+*task.JiraKey, "evidence/"+*task.JiraKey)
			}
			if task.SDDChange != nil {
				backfillPrefixes = append(backfillPrefixes, "sdd/"+*task.SDDChange+"/")
			}
			for _, prefix := range backfillPrefixes {
				if len(linked) >= limit {
					break
				}
				extra, _ := s.ObservationsByTopicKeyPrefix(project, prefix, limit-len(linked))
				for _, o := range extra {
					if seen[o.ID] || len(linked) >= limit {
						continue
					}
					seen[o.ID] = true
					linked = append(linked, store.TaskObservationDetail{Role: "context", Observation: o})
					total++
				}
			}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "**Observaciones de la tarea (%d de %d)**\n", len(linked), total)
		for _, d := range linked {
			fmt.Fprintf(&b, "- [%s #%d %s] %s\n", d.Role, d.Observation.ID, d.Observation.Type,
				truncateRunes(d.Observation.Content, obsChars))
			pack.Observations = append(pack.Observations, map[string]any{
				"id": d.Observation.ID, "sync_id": d.Observation.SyncID, "role": d.Role,
				"type": d.Observation.Type, "title": d.Observation.Title,
				"snippet": truncateRunes(d.Observation.Content, obsChars),
			})
			includedObsSyncIDs = append(includedObsSyncIDs, d.Observation.SyncID)
		}
		blocks["observations"] = fit("observations", strings.TrimRight(b.String(), "\n"))
	}

	// ─── 6. evidence ────────────────────────────────────────────────────
	if want["evidence"] {
		items, total, _, _ := s.ListEvidence(project, store.EvidenceListFilter{TaskSyncID: task.SyncID, Limit: 10})
		var b strings.Builder
		fmt.Fprintf(&b, "**Evidencia (%d)**\n", total)
		for _, e := range items {
			attached := "no adjunta"
			if e.AttachedJira {
				attached = "adjunta a Jira"
			}
			shaShort := e.SHA256
			if len(shaShort) > 12 {
				shaShort = shaShort[:12]
			}
			fmt.Fprintf(&b, "- %s · %s · %s · %s · prueba: %s\n", e.Path, shaShort, e.Kind, attached, e.Proves)
			pack.Evidence = append(pack.Evidence, map[string]any{
				"path": e.Path, "sha256_short": shaShort, "kind": e.Kind, "attached_jira": e.AttachedJira, "proves": e.Proves,
			})
		}
		blocks["evidence"] = fit("evidence", strings.TrimRight(b.String(), "\n"))
	}

	// ─── 7. runbooks ────────────────────────────────────────────────────
	if want["runbooks"] && opts.IncludeRunbooks {
		items, _, _ := s.FindRunbooks(store.RunbookFindParams{
			Query: task.Title, Project: project, IncludeStale: true, MatchMode: "any", Limit: 3,
		})
		var b strings.Builder
		b.WriteString("**Runbooks candidatos**\n")
		for _, r := range items {
			status := r.Status
			if r.Stale {
				ageStr := ""
				if r.AgeDays != nil {
					ageStr = fmt.Sprintf(" %d días", *r.AgeDays)
				}
				status += " · STALE" + ageStr
			}
			fmt.Fprintf(&b, "- %s %s · %s/%s · %s · ejecutado %d veces\n",
				r.ID, r.Title, r.Category, strVal(r.Pattern), status, r.ExecCount)
			pack.Runbooks = append(pack.Runbooks, map[string]any{
				"id": r.ID, "title": r.Title, "category": r.Category, "pattern": r.Pattern,
				"status": r.Status, "stale": r.Stale, "exec_count": r.ExecCount,
			})
		}
		blocks["runbooks"] = fit("runbooks", strings.TrimRight(b.String(), "\n"))
	}

	// ─── 8. refs ────────────────────────────────────────────────────────
	if want["refs"] && len(includedObsSyncIDs) > 0 {
		refs, _ := s.ObservationRefsFor(includedObsSyncIDs)
		grouped := map[string][]string{}
		var kinds []string
		for _, r := range refs {
			if _, ok := grouped[r.RefKind]; !ok {
				kinds = append(kinds, r.RefKind)
			}
			grouped[r.RefKind] = append(grouped[r.RefKind], r.Ref)
		}
		sort.Strings(kinds)
		var b strings.Builder
		b.WriteString("**Referencias**\n")
		for _, kind := range kinds {
			for _, ref := range grouped[kind] {
				fmt.Fprintf(&b, "- %s: %s\n", kind, ref)
			}
		}
		if len(kinds) > 0 {
			blocks["refs"] = fit("refs", strings.TrimRight(b.String(), "\n"))
			pack.Refs = grouped
		}
	}

	// ─── 9. footer ──────────────────────────────────────────────────────
	if want["footer"] {
		blocks["footer"] = fit("footer", fmt.Sprintf(
			"_Generado %s UTC · fuentes: engram (historia), vault (dominio), graphify (estructura) · conflicto: grafo > vault > engram según el tipo de hecho_",
			time.Now().UTC().Format("2006-01-02 15:04:05")))
	}

	// Assemble in canonical order, dropping sections (reverse-priority) while
	// the total still exceeds MaxChars.
	active := map[string]bool{}
	for sec := range blocks {
		active[sec] = true
	}
	assemble := func() string {
		var parts []string
		for _, sec := range canonicalSections {
			if active[sec] {
				if text, ok := blocks[sec]; ok && text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n\n")
	}
	rendered := assemble()
	for _, sec := range droppableInReverseOrder {
		if len([]rune(rendered)) <= opts.MaxChars {
			break
		}
		if active[sec] {
			active[sec] = false
			truncatedSections[sec] = true
			rendered = assemble()
		}
	}

	pack.Truncated = len(truncatedSections) > 0
	pack.Chars = len([]rune(rendered))
	return pack, rendered, nil
}

func taskKey(t store.Task) string {
	if t.JiraKey != nil {
		return *t.JiraKey
	}
	if t.SDDChange != nil {
		return *t.SDDChange
	}
	return t.SyncID
}

func isTaskStateStale(stateSyncedAt *string) bool {
	if stateSyncedAt == nil || strings.TrimSpace(*stateSyncedAt) == "" {
		return false
	}
	t, err := time.Parse("2006-01-02 15:04:05", *stateSyncedAt)
	if err != nil {
		return false
	}
	return time.Since(t) > 24*time.Hour
}

func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func strVal(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// gitHeadCommit runs `git -C repoDir rev-parse HEAD`, returning "" on any
// failure. Used only for the optional graph-staleness hint in `pointers`.
func gitHeadCommit(repoDir string) string {
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
