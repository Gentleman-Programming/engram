// Package project: code-graph summary (RFC rfc-engram-projects.md §8, ADR-026
// "Indexación del code graph en engram-projects").
//
// SyncGraph reads <repo_dir>/<graph_path> (graphify's graph.json) and, when
// present, the sibling GRAPH_REPORT.md, computes the graph_summary blob
// (§8.3), and stamps graph_commit/graph_built_at/graph_summary on the
// project's card in a single write. It never copies graph.json's nodes or
// links into SQLite — graphify itself stays the query engine for structural
// facts (`graphify explain|affected|query|path`); this file only produces
// the small, cheap-to-read summary that ADR-026 decided to persist instead.
//
// This lives in internal/project rather than internal/store because
// internal/project already depends on internal/store (see contextpack.go);
// store cannot depend back on project without an import cycle, so the parsing
// and aggregation logic — the actual "T-04.06" work — belongs here, and
// internal/store only exposes the low-level StampProjectGraph write.
//
// Ground truth vs. the RFC, verified against three real graphify-out/
// directories (629, 2242 and 9967 nodes) while implementing this file:
//
//   - `relation` is an open set, not the RFC/ADR-026's closed list of 10
//     values. Observed additionally: imports_from, uses, uses_config,
//     mixes_in, references_constant, requires_env; `case_of` from the RFC's
//     list was never observed. Relations are therefore counted as a plain
//     map of whatever strings appear, never validated against an enum.
//   - GRAPH_REPORT.md's "God Nodes" section never carries a file path (only
//     `label` and `edges`) — the RFC's worked example shows a `file` next to
//     each god node, but that column only exists in graph.json's
//     source_file, resolved here after the label+edge-count match.
//   - Two distinct nodes can legitimately share a label and both rank as god
//     nodes: a real ~10k-node PHP graph ranked two vendored `PHP-Parser`
//     classes (`Php5`, `Php7`, `Standard` — one instance per vendored
//     version) as separate god nodes, each at a real but different degree.
//     A naive degree recount from `links` can also break a tie differently
//     than graphify's own report — verified on a real ~600-node graph, where
//     a plain source/target tally ranked a different node than the report
//     at one shared degree value — so when GRAPH_REPORT.md is present, its
//     God Nodes list is treated as the source of truth for label+edges, and
//     graph.json is only consulted to resolve each entry's file.
//   - GRAPH_REPORT.md's "Communities" section never lists files either (only
//     member node labels, truncated with "(+N more)") — `top_files` has no
//     textual source and is always computed from graph.json's `source_file`
//     per node.
//   - The report's "Extraction" line advertises a third "AMBIGUOUS" bucket
//     alongside EXTRACTED/INFERRED, but every graph.json observed only ever
//     used `confidence` values EXTRACTED or INFERRED — AMBIGUOUS never
//     appeared above 0%. extracted_pct/inferred_pct are computed from
//     graph.json's edge count as the denominator (not extracted+inferred),
//     so a future confidence value doesn't silently distort either percentage
//     to over 100%.
//   - graphify also writes graph.html, manifest.json, .graphify_root and
//     .graphify_labels.json, none of which the RFC's §8.2 inventory
//     mentions. .graphify_labels.json maps community id -> label and would
//     be a second source for community labels, but the RFC's algorithm names
//     GRAPH_REPORT.md's "## Communities" section as the source, so that file
//     is not read here.
package project

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// graphSummaryMaxBytes is the hard cap from RFC §8.3 step 8. It fits with
// plenty of headroom under ENGRAM_CLOUD_MAX_PUSH_BYTES (8 MiB default), which
// is the other constraint on project_card mutation payloads (§10 in the RFC).
const graphSummaryMaxBytes = 64 * 1024

// GraphSummary is the JSON blob persisted at project_cards.graph_summary.
// Field order mirrors the RFC's worked example (§8.3) verbatim so a
// byte-level diff against that example only shows real differences in
// data, not in shape.
type GraphSummary struct {
	Source         []string         `json:"source"`
	BuiltAtCommit  string           `json:"built_at_commit"`
	NodeCount      int              `json:"node_count"`
	EdgeCount      int              `json:"edge_count"`
	CommunityCount int              `json:"community_count"`
	ExtractedPct   int              `json:"extracted_pct"`
	InferredPct    int              `json:"inferred_pct"`
	GodNodes       []GodNode        `json:"god_nodes"`
	Communities    []GraphCommunity `json:"communities"`
	FileTypes      map[string]int   `json:"file_types"`
	Relations      map[string]int   `json:"relations"`
	ReportPath     string           `json:"report_path,omitempty"`
}

// GodNode is one entry of graph_summary.god_nodes: a node ranked by degree
// (RFC §8.3 step 3). Entries are never deduplicated by Label — see the
// package doc comment on why two god nodes can legitimately share one.
type GodNode struct {
	Label string `json:"label"`
	Edges int    `json:"edges"`
	File  string `json:"file"`
}

// GraphCommunity is one entry of graph_summary.communities: one of the 10
// largest communities by node count (RFC §8.3 step 4).
type GraphCommunity struct {
	ID       int      `json:"id"`
	Label    string   `json:"label"`
	Size     int      `json:"size"`
	TopFiles []string `json:"top_files"`
}

// SyncGraph implements RFC §8.3's 8-step algorithm. It is the only caller of
// store.StampProjectGraph: on any error it returns before touching the
// database, so a project_cards row never ends up with a graph_summary that
// doesn't match its graph_commit.
func SyncGraph(s *store.Store, slug, repoDir, graphPath string) (store.GraphSyncResult, error) {
	var result store.GraphSyncResult

	if strings.TrimSpace(graphPath) == "" {
		graphPath = "graphify-out/graph.json"
	}
	fullPath := graphPath
	if !filepath.IsAbs(fullPath) {
		fullPath = filepath.Join(repoDir, graphPath)
	}
	if _, err := os.Stat(fullPath); errors.Is(err, os.ErrNotExist) {
		return result, store.ErrGraphNotFound
	} else if err != nil {
		return result, fmt.Errorf("engram-projects: stat graph.json: %w", err)
	}

	stats, err := decodeGraphJSON(fullPath)
	if err != nil {
		return result, fmt.Errorf("engram-projects: parse graph.json: %w", err)
	}
	// Step 2 (RFC §8.3): a missing/malformed built_at_commit means nothing is
	// written at all (D-02). Checked after the full decode rather than
	// up front: built_at_commit sits at the end of graphify's real output
	// (verified: it comes after "nodes" and "links" in every graph.json
	// inspected), so failing fast would need a second, cheaper pre-scan that
	// isn't worth the extra code for what is the uncommon path in practice.
	if !isValidCommit(stats.BuiltAtCommit) {
		return result, store.ErrGraphMissingCommit
	}

	reportRel := filepath.Join(filepath.Dir(graphPath), "GRAPH_REPORT.md")
	reportFull := reportRel
	if !filepath.IsAbs(reportFull) {
		reportFull = filepath.Join(repoDir, reportRel)
	}
	var reportGodNodes []reportGodNode
	var communityLabels map[int]string
	reportUsed := false
	if _, statErr := os.Stat(reportFull); statErr == nil {
		reportGodNodes, communityLabels, err = parseGraphReport(reportFull)
		if err == nil {
			reportUsed = true
		}
		// A malformed report degrades to graph.json-only data instead of
		// failing the sync: GRAPH_REPORT.md is a cross-check, never the
		// source of truth for node/edge/community counts (RFC §8.3 steps 3-4).
	}

	godNodes := topGodNodes(stats, 10)
	if reportUsed {
		godNodes = mergeGodNodesWithReport(stats, reportGodNodes)
	}
	communities := topCommunities(stats, 10, communityLabels)
	extractedPct, inferredPct := extractionPct(stats)

	summary := &GraphSummary{
		Source:         []string{graphPath},
		BuiltAtCommit:  stats.BuiltAtCommit,
		NodeCount:      stats.NodeCount,
		EdgeCount:      stats.EdgeCount,
		CommunityCount: len(stats.CommunitySize),
		ExtractedPct:   extractedPct,
		InferredPct:    inferredPct,
		GodNodes:       godNodes,
		Communities:    communities,
		FileTypes:      stats.FileTypes,
		Relations:      stats.Relations,
	}
	if reportUsed {
		summary.Source = append(summary.Source, reportRel)
		summary.ReportPath = reportRel
	}

	data, err := marshalWithBudget(summary, graphSummaryMaxBytes)
	if err != nil {
		return result, fmt.Errorf("engram-projects: marshal graph_summary: %w", err)
	}
	summaryJSON := string(data)

	builtAt := time.Now().UTC().Format("2006-01-02 15:04:05")
	if fi, statErr := os.Stat(fullPath); statErr == nil {
		builtAt = fi.ModTime().UTC().Format("2006-01-02 15:04:05")
	}
	headCommit := gitHeadCommitTimeout(repoDir)

	if err := s.StampProjectGraph(slug, stats.BuiltAtCommit, builtAt, &summaryJSON); err != nil {
		return result, err
	}

	result.Synced = true
	result.GraphCommit = stats.BuiltAtCommit
	result.HeadCommit = headCommit
	result.Stale = headCommit != "" && headCommit != stats.BuiltAtCommit
	result.NodeCount = stats.NodeCount
	result.EdgeCount = stats.EdgeCount
	result.CommunityCount = len(stats.CommunitySize)
	return result, nil
}

// ─── graph.json streaming decode ─────────────────────────────────────────────

type nodeInfo struct {
	Label string
	File  string
}

// graphStats accumulates only the aggregates SyncGraph needs, never the
// nodes/links slices themselves. nodeMeta is the one exception: resolving a
// god node's label/file requires knowing which node a given id names, and
// that pairing only exists on the node record, not on the link that reports
// its degree. Keeping id->{label,file} (two short strings per node) is a
// small fraction of what graph.json itself carries per node (also
// source_location, norm_label, community, metadata) — RFC §8.3 step 2's
// "sin cargar nodes completos en memoria" is read here as "never hold the
// full node records", not "hold nothing at all".
type graphStats struct {
	BuiltAtCommit  string
	NodeCount      int
	EdgeCount      int
	Degree         map[string]int
	NodeMeta       map[string]nodeInfo
	CommunitySize  map[int]int
	CommunityFiles map[int]map[string]int
	FileTypes      map[string]int
	Relations      map[string]int
	Extracted      int
	Inferred       int
}

func decodeGraphJSON(path string) (*graphStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	stats := &graphStats{
		Degree:         map[string]int{},
		NodeMeta:       map[string]nodeInfo{},
		CommunitySize:  map[int]int{},
		CommunityFiles: map[int]map[string]int{},
		FileTypes:      map[string]int{},
		Relations:      map[string]int{},
	}

	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		switch key {
		case "built_at_commit":
			if err := dec.Decode(&stats.BuiltAtCommit); err != nil {
				return nil, fmt.Errorf("built_at_commit: %w", err)
			}
		case "nodes":
			if err := decodeNodesArray(dec, stats); err != nil {
				return nil, fmt.Errorf("nodes: %w", err)
			}
		case "links":
			if err := decodeLinksArray(dec, stats); err != nil {
				return nil, fmt.Errorf("links: %w", err)
			}
		default:
			// directed, multigraph, graph, hyperedges: small in every
			// graph.json observed (hyperedges is always []); ADR-026 notes
			// degree computation ignores hyperedges since they are unused
			// in every graph inspected so far.
			var discard any
			if err := dec.Decode(&discard); err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}
	return stats, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || d != want {
		return fmt.Errorf("expected %q, got %v", want, tok)
	}
	return nil
}

func decodeNodesArray(dec *json.Decoder, stats *graphStats) error {
	if err := expectDelim(dec, '['); err != nil {
		return err
	}
	for dec.More() {
		var n struct {
			ID         string `json:"id"`
			Label      string `json:"label"`
			FileType   string `json:"file_type"`
			SourceFile string `json:"source_file"`
			Community  *int   `json:"community"`
		}
		if err := dec.Decode(&n); err != nil {
			return err
		}
		stats.NodeCount++
		file := sanitizeRelFile(n.SourceFile)
		stats.NodeMeta[n.ID] = nodeInfo{Label: n.Label, File: file}
		if n.FileType != "" {
			stats.FileTypes[n.FileType]++
		}
		if n.Community != nil {
			c := *n.Community
			stats.CommunitySize[c]++
			if file != "" {
				if stats.CommunityFiles[c] == nil {
					stats.CommunityFiles[c] = map[string]int{}
				}
				stats.CommunityFiles[c][file]++
			}
		}
	}
	return expectDelim(dec, ']')
}

func decodeLinksArray(dec *json.Decoder, stats *graphStats) error {
	if err := expectDelim(dec, '['); err != nil {
		return err
	}
	for dec.More() {
		var l struct {
			Source     string `json:"source"`
			Target     string `json:"target"`
			Relation   string `json:"relation"`
			Confidence string `json:"confidence"`
		}
		if err := dec.Decode(&l); err != nil {
			return err
		}
		stats.EdgeCount++
		if l.Source != "" {
			stats.Degree[l.Source]++
		}
		if l.Target != "" {
			stats.Degree[l.Target]++
		}
		if l.Relation != "" {
			stats.Relations[l.Relation]++
		}
		switch l.Confidence {
		case "EXTRACTED":
			stats.Extracted++
		case "INFERRED":
			stats.Inferred++
		}
		// Any other confidence value (e.g. a future AMBIGUOUS) is left out of
		// both counters on purpose: extractionPct divides by EdgeCount, not
		// Extracted+Inferred, so an unrecognized value never inflates either
		// percentage past what the edges actually support.
	}
	return expectDelim(dec, ']')
}

var hexCommitRe = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func isValidCommit(commit string) bool {
	return hexCommitRe.MatchString(commit)
}

// sanitizeRelFile blanks out a source_file graphify recorded as an absolute
// path. RFC §5.10 requires every path inside graph_summary to be
// repo-relative ("el sync del grafo rechaza rutas absolutas en el resumen");
// every graph.json inspected while building this already used relative
// paths, so this is a defensive guard, not a normalization step seen to fire
// in practice.
func sanitizeRelFile(f string) string {
	if f == "" || filepath.IsAbs(f) {
		return ""
	}
	return f
}

// ─── God nodes and communities ───────────────────────────────────────────────

func topGodNodes(stats *graphStats, n int) []GodNode {
	type cand struct {
		id     string
		degree int
	}
	cands := make([]cand, 0, len(stats.Degree))
	for id, d := range stats.Degree {
		cands = append(cands, cand{id, d})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].degree != cands[j].degree {
			return cands[i].degree > cands[j].degree
		}
		return cands[i].id < cands[j].id // stable tie-break for reproducible output
	})
	if len(cands) > n {
		cands = cands[:n]
	}
	out := make([]GodNode, 0, len(cands))
	for _, c := range cands {
		meta := stats.NodeMeta[c.id]
		out = append(out, GodNode{Label: meta.Label, Edges: c.degree, File: meta.File})
	}
	return out
}

// mergeGodNodesWithReport replaces a naive links-based degree ranking with
// GRAPH_REPORT.md's own God Nodes list, verbatim, resolving each entry's file
// from graph.json by matching label+exact edge count. See the package doc
// comment: graphify's own ranking can break a tie differently than a plain
// source/target tally, and duplicate labels across distinct nodes are real,
// so entries are matched one at a time and each matched node id is
// consumed at most once.
func mergeGodNodesWithReport(stats *graphStats, report []reportGodNode) []GodNode {
	if len(report) == 0 {
		return nil
	}
	used := map[string]bool{}
	merged := make([]GodNode, 0, len(report))
	for _, rg := range report {
		bestID := ""
		for id, meta := range stats.NodeMeta {
			if used[id] || meta.Label != rg.Label {
				continue
			}
			if stats.Degree[id] != rg.Edges {
				continue
			}
			if bestID == "" || id < bestID {
				bestID = id
			}
		}
		if bestID == "" {
			// The report mentions a node graph.json doesn't have a matching
			// degree for (stale report, or a node pruned since); keep the
			// report's own numbers rather than dropping the entry.
			merged = append(merged, GodNode{Label: rg.Label, Edges: rg.Edges})
			continue
		}
		used[bestID] = true
		merged = append(merged, GodNode{Label: rg.Label, Edges: rg.Edges, File: stats.NodeMeta[bestID].File})
	}
	return merged
}

func topCommunities(stats *graphStats, n int, reportLabels map[int]string) []GraphCommunity {
	type cand struct {
		id   int
		size int
	}
	cands := make([]cand, 0, len(stats.CommunitySize))
	for id, size := range stats.CommunitySize {
		cands = append(cands, cand{id, size})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].size != cands[j].size {
			return cands[i].size > cands[j].size
		}
		return cands[i].id < cands[j].id
	})
	if len(cands) > n {
		cands = cands[:n]
	}
	out := make([]GraphCommunity, 0, len(cands))
	for _, c := range cands {
		label := reportLabels[c.id]
		if label == "" {
			// Either GRAPH_REPORT.md doesn't exist, this community was
			// "thin" and omitted from it, or it was never renamed with
			// `graphify label` — all three collapse to the same default the
			// report itself would print (RFC §8.3 step 4).
			label = fmt.Sprintf("Community %d", c.id)
		}
		out = append(out, GraphCommunity{
			ID:       c.id,
			Label:    label,
			Size:     c.size,
			TopFiles: topFiles(stats.CommunityFiles[c.id], 3),
		})
	}
	return out
}

func topFiles(counts map[string]int, n int) []string {
	type cand struct {
		file  string
		count int
	}
	cands := make([]cand, 0, len(counts))
	for f, c := range counts {
		cands = append(cands, cand{f, c})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].count != cands[j].count {
			return cands[i].count > cands[j].count
		}
		return cands[i].file < cands[j].file
	})
	if len(cands) > n {
		cands = cands[:n]
	}
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.file)
	}
	return out
}

func extractionPct(stats *graphStats) (extractedPct, inferredPct int) {
	if stats.EdgeCount == 0 {
		return 0, 0
	}
	extractedPct = int(math.Round(100 * float64(stats.Extracted) / float64(stats.EdgeCount)))
	inferredPct = int(math.Round(100 * float64(stats.Inferred) / float64(stats.EdgeCount)))
	return
}

// ─── GRAPH_REPORT.md parsing ─────────────────────────────────────────────────

type reportGodNode struct {
	Label string
	Edges int
}

var (
	godNodeLineRe     = regexp.MustCompile("^\\d+\\.\\s+`(.+?)`\\s*-\\s*(\\d+)\\s+edges\\s*$")
	communityHeaderRe = regexp.MustCompile(`^### Community (\d+) - "(.*)"$`)
)

// parseGraphReport extracts the two facts SyncGraph cross-checks against
// graph.json: the ordered "## God Nodes" list (label, edges) and a
// community-id -> label map from "## Communities". Every other section
// (Surprising Connections, Import Cycles, Knowledge Gaps, Suggested
// Questions...) is prose meant for a human reading the report directly and
// is intentionally not parsed here.
func parseGraphReport(path string) ([]reportGodNode, map[int]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var godNodes []reportGodNode
	communityLabels := map[int]string{}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	inGodNodes := false
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "## God Nodes"):
			inGodNodes = true
			continue
		case strings.HasPrefix(line, "## "):
			inGodNodes = false
		}
		if inGodNodes {
			if m := godNodeLineRe.FindStringSubmatch(line); m != nil {
				edges, convErr := strconv.Atoi(m[2])
				if convErr == nil {
					godNodes = append(godNodes, reportGodNode{Label: m[1], Edges: edges})
				}
			}
			continue
		}
		if m := communityHeaderRe.FindStringSubmatch(line); m != nil {
			if id, convErr := strconv.Atoi(m[1]); convErr == nil {
				communityLabels[id] = m[2]
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	return godNodes, communityLabels, nil
}

// ─── Size budget (RFC §8.3 step 8) ───────────────────────────────────────────

// marshalWithBudget serializes summary and, if it exceeds limit, sheds data
// in the order the RFC specifies ("se truncan communities y relations antes
// que god_nodes"): relations first, then communities' top_files and then
// communities themselves, then file_types as an extra safety net the RFC
// doesn't name explicitly, and only then god_nodes — one entry at a time, so
// the cheapest cut that gets back under budget is always taken first.
func marshalWithBudget(summary *GraphSummary, limit int) ([]byte, error) {
	for {
		data, err := json.Marshal(summary)
		if err != nil {
			return nil, err
		}
		if len(data) <= limit {
			return data, nil
		}
		switch {
		case len(summary.Relations) > 5:
			summary.Relations = topNCounts(summary.Relations, 5)
		case len(summary.Relations) > 0:
			summary.Relations = map[string]int{}
		case capCommunityTopFiles(summary.Communities, 1):
			// shrunk in place; loop re-measures
		case len(summary.Communities) > 1:
			summary.Communities = summary.Communities[:len(summary.Communities)-1]
		case len(summary.Communities) > 0:
			summary.Communities = nil
		case len(summary.FileTypes) > 3:
			summary.FileTypes = topNCounts(summary.FileTypes, 3)
		case len(summary.GodNodes) > 1:
			summary.GodNodes = summary.GodNodes[:len(summary.GodNodes)-1]
		default:
			// Nothing left to cut (a single god node's own bytes exceed the
			// budget, which would require pathologically long labels/paths);
			// return what we have rather than loop forever.
			return data, nil
		}
	}
}

func capCommunityTopFiles(cs []GraphCommunity, max int) bool {
	changed := false
	for i := range cs {
		if len(cs[i].TopFiles) > max {
			cs[i].TopFiles = cs[i].TopFiles[:max]
			changed = true
		}
	}
	return changed
}

func topNCounts(m map[string]int, n int) map[string]int {
	type cand struct {
		key   string
		count int
	}
	cands := make([]cand, 0, len(m))
	for k, c := range m {
		cands = append(cands, cand{k, c})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].count != cands[j].count {
			return cands[i].count > cands[j].count
		}
		return cands[i].key < cands[j].key
	})
	if len(cands) > n {
		cands = cands[:n]
	}
	out := make(map[string]int, len(cands))
	for _, c := range cands {
		out[c.key] = c.count
	}
	return out
}

// ─── git HEAD (staleness check, RFC §8.3 step 7) ─────────────────────────────

// gitHeadCommitTimeout runs `git -C repoDir rev-parse HEAD` with the same
// 2-second timeout pattern as detectGitRootDir (detect.go), per RFC §8.3
// step 7 ("mismo patrón que detectGitRootDir"). Distinct from the existing
// gitHeadCommit (contextpack.go), which has no timeout and only backs the
// best-effort pointer hint in mem_context_pack; a stalled git process here
// must not block mem_project_upsert.
func gitHeadCommitTimeout(repoDir string) string {
	if strings.TrimSpace(repoDir) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := newProjectCommandContext(ctx, "git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
