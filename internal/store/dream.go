package store

import (
	"fmt"
	"sort"
	"strings"
)

// DreamGroup is one connected component of the relation graph among live
// observations of a single project and scope. IDs are sorted ascending.
type DreamGroup struct {
	Scope string
	IDs   []int64
}

// DreamGroups clusters the live, unpinned observations of project into
// connected components over existing relations (judged related, compatible,
// supersedes or conflicts_with, plus still-pending candidates). Edges only
// join observations whose project and scope match, so a group never crosses
// either. The result is deterministic: groups are ordered by their smallest ID.
//
// Pinned observations are left out because a consolidation survivor is never
// pinned, so merging one would silently drop the pin.
func (s *Store) DreamGroups(project string) ([]DreamGroup, error) {
	project, _ = NormalizeProject(project)
	if project == "" {
		return nil, ErrProjectRequired
	}
	// ponytail: full scan of memory_relations on every run, joined to
	// observations afterwards; if that gets slow, filter observations by
	// project first (CTE of live project sync_ids) and join relations to it.
	rows, err := s.db.Query(`
		SELECT src.id, tgt.id, src.scope
		FROM memory_relations r
		JOIN observations src ON src.sync_id = r.source_id AND src.deleted_at IS NULL AND src.pinned = 0
		JOIN observations tgt ON tgt.sync_id = r.target_id AND tgt.deleted_at IS NULL AND tgt.pinned = 0
		WHERE r.superseded_at IS NULL
		  AND r.judgment_status IN ('pending', 'judged')
		  AND r.relation IN ('pending', 'related', 'compatible', 'supersedes', 'conflicts_with')
		  AND LOWER(src.project) = ? AND LOWER(tgt.project) = ?
		  AND src.scope = tgt.scope
		  AND src.id != tgt.id
		ORDER BY src.id, tgt.id`, project, project)
	if err != nil {
		return nil, fmt.Errorf("DreamGroups: query: %w", err)
	}
	defer rows.Close()

	parent := map[int64]int64{}
	scope := map[int64]string{}
	var find func(int64) int64
	find = func(x int64) int64 {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	for rows.Next() {
		var a, b int64
		var sc string
		if err := rows.Scan(&a, &b, &sc); err != nil {
			return nil, fmt.Errorf("DreamGroups: scan: %w", err)
		}
		for _, id := range []int64{a, b} {
			if _, ok := parent[id]; !ok {
				parent[id] = id
				scope[id] = sc
			}
		}
		ra, rb := find(a), find(b)
		if ra > rb {
			ra, rb = rb, ra
		}
		parent[rb] = ra // the smallest ID is always the root
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("DreamGroups: rows: %w", err)
	}

	byRoot := map[int64][]int64{}
	for id := range parent {
		root := find(id)
		byRoot[root] = append(byRoot[root], id)
	}
	groups := make([]DreamGroup, 0, len(byRoot))
	for root, ids := range byRoot {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		groups = append(groups, DreamGroup{Scope: scope[root], IDs: ids})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].IDs[0] < groups[j].IDs[0] })
	return groups, nil
}

// DreamFocusGroups returns the live, unpinned observations of project whose
// title, content or topic_key contains focus (case-insensitive for ASCII;
// SQLite LOWER/LIKE without ICU), grouped by scope. No relation is required
// between them: the runner judges which belong together. Scopes with fewer
// than two matches are dropped. focus is bound as a parameter with LIKE
// wildcards escaped, so quotes, %, _ and \ match literally.
//
// ponytail: plain LIKE over the project instead of the FTS search, because
// Search caps results at MaxSearchResults and would silently drop members.
func (s *Store) DreamFocusGroups(project, focus string) ([]DreamGroup, error) {
	project, _ = NormalizeProject(project)
	if project == "" {
		return nil, ErrProjectRequired
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(focus)) + "%"
	rows, err := s.db.Query(`
		SELECT id, scope FROM observations
		WHERE deleted_at IS NULL AND pinned = 0 AND LOWER(project) = ?
		  AND (LOWER(title) LIKE ? ESCAPE '\'
		    OR LOWER(content) LIKE ? ESCAPE '\'
		    OR LOWER(ifnull(topic_key, '')) LIKE ? ESCAPE '\')
		ORDER BY scope, id`,
		project, pattern, pattern, pattern)
	if err != nil {
		return nil, fmt.Errorf("DreamFocusGroups: query: %w", err)
	}
	defer rows.Close()
	var groups []DreamGroup
	for rows.Next() {
		var id int64
		var scope string
		if err := rows.Scan(&id, &scope); err != nil {
			return nil, fmt.Errorf("DreamFocusGroups: scan: %w", err)
		}
		if n := len(groups); n == 0 || groups[n-1].Scope != scope {
			groups = append(groups, DreamGroup{Scope: scope})
		}
		groups[len(groups)-1].IDs = append(groups[len(groups)-1].IDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("DreamFocusGroups: rows: %w", err)
	}
	kept := groups[:0]
	for _, g := range groups {
		if len(g.IDs) >= 2 {
			kept = append(kept, g)
		}
	}
	return kept, nil
}
