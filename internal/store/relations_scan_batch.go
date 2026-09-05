package store

import (
	"fmt"
	"sort"
	"strings"
)

// scanCandidateLimit is the per-source candidate budget used by ScanProject.
const scanCandidateLimit = 10

// scanChunkSize bounds IN (...) parameter lists in batched lookups so the
// batched path stays well under SQLite's variable limit.
const scanChunkSize = 500

// scanSourceRow carries the page fields the batched candidate ranking needs.
type scanSourceRow struct {
	id      int64
	syncID  string
	scope   string
	project string
	title   string
}

// candidateTerms splits a title into the quoted FTS5 phrases used for
// candidate detection (ANY-term overlap recall). It preserves duplicates and
// order so sanitizeFTSCandidates keeps its exact legacy output.
func candidateTerms(title string) []string {
	words := strings.Fields(title)
	if len(words) == 0 {
		return nil
	}
	quoted := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.Trim(w, `"`)
		if w == "" {
			continue
		}
		var escaped strings.Builder
		for i := 0; i < len(w); i++ {
			if w[i] == '"' {
				// FTS5 escapes a literal quote by doubling it. Preserve an
				// already escaped pair so callers do not get double escaped.
				escaped.WriteString(`""`)
				if i+1 < len(w) && w[i+1] == '"' {
					i++
				}
				continue
			}
			escaped.WriteByte(w[i])
		}
		quoted = append(quoted, `"`+escaped.String()+`"`)
	}
	return quoted
}

// scanCandidateMeta holds the candidate-side fields needed for per-source
// filtering (project, scope) alongside the returned Candidate payload.
type scanCandidateMeta struct {
	cand    Candidate
	scope   string
	project string
}

// scanCandidateBatch computes conflict candidates for a whole scan page with
// bounded page-level FTS batching (#955).
//
// Contract (issue #955 reopen): candidate recall (ANY-term overlap, answered
// by FTS5 itself), project/scope/deleted/self/judged-relation filters,
// per-source limits, and counters stay identical to the legacy per-source
// FindCandidates query. The internal score and ordering are replaced by a
// deterministic conflict-specific score: the number of distinct source title
// terms that FTS-matched the candidate, ordered score descending with
// candidate id ascending as the tie-break.
//
// Batching: every distinct quoted term across the page is issued exactly once
// as a single-phrase FTS query (deterministic maximum query size, cf. #718),
// so the number of FTS index scans per page is the page's distinct-term
// vocabulary instead of one multi-term scan per inspected observation.
//
// Any error aborts the batch so the caller can fall back to the legacy
// per-source path, preserving source-level failure behavior.
func (s *Store) scanCandidateBatch(sources []scanSourceRow, candidateLimit int) (map[int64][]Candidate, error) {
	result := make(map[int64][]Candidate, len(sources))
	if len(sources) == 0 || candidateLimit <= 0 {
		return result, nil
	}

	// ── Phase 1: distinct page vocabulary → one bounded FTS query per term ──
	termRowids := make(map[string][]int64)
	for _, src := range sources {
		for _, term := range candidateTerms(src.title) {
			if _, seen := termRowids[term]; seen {
				continue
			}
			termRowids[term] = nil // reserve before querying so failures retry cleanly
			rows, err := s.db.Query(
				`SELECT rowid FROM observations_fts WHERE observations_fts MATCH ?`, term,
			)
			if err != nil {
				return nil, fmt.Errorf("scanCandidateBatch: term %s: %w", term, err)
			}
			var ids []int64
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return nil, fmt.Errorf("scanCandidateBatch: scan term %s: %w", term, err)
				}
				ids = append(ids, id)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scanCandidateBatch: term %s rows: %w", term, err)
			}
			if err := rows.Close(); err != nil {
				return nil, fmt.Errorf("scanCandidateBatch: close term %s: %w", term, err)
			}
			termRowids[term] = ids
		}
	}

	// ── Phase 2: candidate metadata for every matched rowid (one pass) ──────
	rowidUnion := make([]int64, 0, len(termRowids))
	for _, ids := range termRowids {
		rowidUnion = append(rowidUnion, ids...)
	}
	sort.Slice(rowidUnion, func(i, j int) bool { return rowidUnion[i] < rowidUnion[j] })
	rowidUnion = dedupSortedInt64(rowidUnion)

	metaByID := make(map[int64]scanCandidateMeta, len(rowidUnion))
	for _, chunk := range chunkInt64(rowidUnion, scanChunkSize) {
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, id := range chunk {
			placeholders[i] = "?"
			args[i] = id
		}
		rows, err := s.db.Query(fmt.Sprintf(`
			SELECT o.id, ifnull(o.sync_id,''), o.title, o.type, o.topic_key,
			       o.scope, ifnull(o.project,'')
			FROM observations o
			WHERE o.deleted_at IS NULL AND o.id IN (%s)
		`, joinStrings(placeholders, ",")), args...)
		if err != nil {
			return nil, fmt.Errorf("scanCandidateBatch: candidate metadata: %w", err)
		}
		for rows.Next() {
			var meta scanCandidateMeta
			if err := rows.Scan(
				&meta.cand.ID, &meta.cand.SyncID, &meta.cand.Title, &meta.cand.Type,
				&meta.cand.TopicKey, &meta.scope, &meta.project,
			); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scanCandidateBatch: scan metadata: %w", err)
			}
			metaByID[meta.cand.ID] = meta
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanCandidateBatch: metadata rows: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("scanCandidateBatch: close metadata: %w", err)
		}
	}

	// ── Phase 3: judged-relation exclusions for the page's sources ──────────
	pageSyncIDs := make([]string, 0, len(sources))
	for _, src := range sources {
		pageSyncIDs = append(pageSyncIDs, src.syncID)
	}
	pageSyncIDs = dedupStrings(pageSyncIDs)

	judgedPairs := make(map[string]struct{})
	for _, chunk := range chunkStrings(pageSyncIDs, scanChunkSize) {
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)*2)
		for i, id := range chunk {
			placeholders[i] = "?"
			args = append(args, id)
		}
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := s.db.Query(fmt.Sprintf(`
			SELECT ifnull(source_id,''), ifnull(target_id,'')
			FROM memory_relations
			WHERE judgment_status = 'judged'
			  AND (source_id IN (%s) OR target_id IN (%s))
		`, joinStrings(placeholders, ","), joinStrings(placeholders, ",")), args...)
		if err != nil {
			return nil, fmt.Errorf("scanCandidateBatch: judged relations: %w", err)
		}
		for rows.Next() {
			var a, b string
			if err := rows.Scan(&a, &b); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scanCandidateBatch: scan judged: %w", err)
			}
			judgedPairs[judgedPairKey(a, b)] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanCandidateBatch: judged rows: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("scanCandidateBatch: close judged: %w", err)
		}
	}

	// ── Phase 4: deterministic per-source scoring, ordering, and limit ──────
	for _, src := range sources {
		scored := make(map[int64]int)
		for _, term := range dedupStrings(candidateTerms(src.title)) {
			for _, id := range termRowids[term] {
				meta, ok := metaByID[id]
				if !ok {
					continue // row is missing or soft-deleted
				}
				if id == src.id {
					continue // self
				}
				if meta.project != src.project {
					continue // project filter
				}
				if meta.scope != src.scope {
					continue // scope filter
				}
				if _, judged := judgedPairs[judgedPairKey(src.syncID, meta.cand.SyncID)]; judged {
					continue // existing judged relation in either direction
				}
				scored[id]++
			}
		}
		if len(scored) == 0 {
			result[src.id] = nil
			continue
		}
		order := make([]int64, 0, len(scored))
		for id := range scored {
			order = append(order, id)
		}
		sort.Slice(order, func(i, j int) bool {
			if scored[order[i]] != scored[order[j]] {
				return scored[order[i]] > scored[order[j]] // score descending
			}
			return order[i] < order[j] // deterministic tie-break: id ascending
		})
		if len(order) > candidateLimit {
			order = order[:candidateLimit]
		}
		candidates := make([]Candidate, 0, len(order))
		for _, id := range order {
			meta := metaByID[id]
			meta.cand.Score = float64(scored[id])
			candidates = append(candidates, meta.cand)
		}
		result[src.id] = candidates
	}

	return result, nil
}

// judgedPairKey canonicalizes an unordered relation pair.
func judgedPairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

func dedupSortedInt64(ids []int64) []int64 {
	if len(ids) == 0 {
		return ids
	}
	out := ids[:1]
	for _, id := range ids[1:] {
		if id != out[len(out)-1] {
			out = append(out, id)
		}
	}
	return out
}

func dedupStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func chunkInt64(items []int64, size int) [][]int64 {
	var chunks [][]int64
	for len(items) > size {
		chunks = append(chunks, items[:size])
		items = items[size:]
	}
	if len(items) > 0 {
		chunks = append(chunks, items)
	}
	return chunks
}

func chunkStrings(items []string, size int) [][]string {
	var chunks [][]string
	for len(items) > size {
		chunks = append(chunks, items[:size])
		items = items[size:]
	}
	if len(items) > 0 {
		chunks = append(chunks, items)
	}
	return chunks
}
