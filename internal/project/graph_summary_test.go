package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// writeGraphFixture copies testdata/<name> into <repoDir>/graphify-out/graph.json,
// mirroring the real layout graphify produces. Returns repoDir.
func writeGraphFixture(t *testing.T, name string) string {
	t.Helper()
	repoDir := t.TempDir()
	src := filepath.Join("testdata", name)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	outDir := filepath.Join(repoDir, "graphify-out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "graph.json"), data, 0o644); err != nil {
		t.Fatalf("write graph.json: %v", err)
	}
	return repoDir
}

func withGraphReport(t *testing.T, repoDir, name string) {
	t.Helper()
	src := filepath.Join("testdata", name)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	dst := filepath.Join(repoDir, "graphify-out", "GRAPH_REPORT.md")
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write GRAPH_REPORT.md: %v", err)
	}
}

func TestSyncGraph_NotFound(t *testing.T) {
	s := newContextPackTestStore(t)
	if _, _, err := s.UpsertProjectCard(store.UpsertProjectCardParams{Slug: "acme"}); err != nil {
		t.Fatalf("UpsertProjectCard: %v", err)
	}

	_, err := SyncGraph(s, "acme", t.TempDir(), "")
	if !errors.Is(err, store.ErrGraphNotFound) {
		t.Fatalf("want ErrGraphNotFound, got %v", err)
	}
}

func TestSyncGraph_MissingCommitWritesNothing(t *testing.T) {
	s := newContextPackTestStore(t)
	if _, _, err := s.UpsertProjectCard(store.UpsertProjectCardParams{Slug: "acme"}); err != nil {
		t.Fatalf("UpsertProjectCard: %v", err)
	}
	repoDir := writeGraphFixture(t, "graph-no-commit.json")

	_, err := SyncGraph(s, "acme", repoDir, "")
	if !errors.Is(err, store.ErrGraphMissingCommit) {
		t.Fatalf("want ErrGraphMissingCommit, got %v", err)
	}

	card, err := s.GetProjectCard("acme")
	if err != nil {
		t.Fatalf("GetProjectCard: %v", err)
	}
	if card.GraphCommit != nil {
		t.Fatalf("graph_commit must stay nil, got %v", *card.GraphCommit)
	}
	if card.GraphBuiltAt != nil {
		t.Fatalf("graph_built_at must stay nil, got %v", *card.GraphBuiltAt)
	}
	if card.GraphSummary != nil {
		t.Fatalf("graph_summary must stay nil, got %v", *card.GraphSummary)
	}
}

func TestSyncGraph_SmallGraphWithoutReport(t *testing.T) {
	s := newContextPackTestStore(t)
	if _, _, err := s.UpsertProjectCard(store.UpsertProjectCardParams{Slug: "acme"}); err != nil {
		t.Fatalf("UpsertProjectCard: %v", err)
	}
	repoDir := writeGraphFixture(t, "graph-small.json")

	result, err := SyncGraph(s, "acme", repoDir, "")
	if err != nil {
		t.Fatalf("SyncGraph: %v", err)
	}
	if !result.Synced {
		t.Fatalf("expected Synced=true")
	}
	if result.GraphCommit != "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" {
		t.Fatalf("unexpected graph_commit: %s", result.GraphCommit)
	}
	if result.NodeCount != 15 || result.EdgeCount != 13 || result.CommunityCount != 4 {
		t.Fatalf("unexpected counts: nodes=%d edges=%d communities=%d", result.NodeCount, result.EdgeCount, result.CommunityCount)
	}

	card, err := s.GetProjectCard("acme")
	if err != nil {
		t.Fatalf("GetProjectCard: %v", err)
	}
	if card.GraphCommit == nil || *card.GraphCommit != result.GraphCommit {
		t.Fatalf("card.graph_commit not stamped correctly: %+v", card.GraphCommit)
	}
	if card.GraphSummary == nil {
		t.Fatalf("card.graph_summary was not written")
	}

	var summary GraphSummary
	if err := json.Unmarshal([]byte(*card.GraphSummary), &summary); err != nil {
		t.Fatalf("graph_summary is not valid JSON: %v", err)
	}

	if summary.NodeCount != 15 || summary.EdgeCount != 13 || summary.CommunityCount != 4 {
		t.Fatalf("unexpected summary counts: %+v", summary)
	}
	if summary.ExtractedPct != 92 || summary.InferredPct != 8 {
		t.Fatalf("unexpected extraction pct: extracted=%d inferred=%d", summary.ExtractedPct, summary.InferredPct)
	}
	if summary.ReportPath != "" {
		t.Fatalf("report_path should be empty when no report exists, got %q", summary.ReportPath)
	}

	wantGodNodes := []GodNode{
		{Label: "svc.Handler", Edges: 6, File: "internal/api/handler.go"},
		{Label: "db.Conn", Edges: 4, File: "internal/db/conn.go"},
		{Label: "worker.Job", Edges: 3, File: "internal/worker/job.go"},
		{Label: "worker.Queue", Edges: 3, File: "internal/worker/queue.go"},
		{Label: "db.Query", Edges: 1, File: "internal/db/query.go"},
		{Label: "db.Migration", Edges: 1, File: "internal/db/migration.go"},
		{Label: "db.Pool", Edges: 1, File: "internal/db/pool.go"},
		{Label: "svc.Router", Edges: 1, File: "internal/api/router.go"},
		{Label: "svc.Middleware", Edges: 1, File: "internal/api/middleware.go"},
		{Label: "svc.Config", Edges: 1, File: "internal/api/config.go"},
	}
	if len(summary.GodNodes) != len(wantGodNodes) {
		t.Fatalf("god_nodes length: got %d want %d (%+v)", len(summary.GodNodes), len(wantGodNodes), summary.GodNodes)
	}
	for i, want := range wantGodNodes {
		if summary.GodNodes[i] != want {
			t.Fatalf("god_nodes[%d]: got %+v want %+v", i, summary.GodNodes[i], want)
		}
	}

	wantCommunities := []GraphCommunity{
		{ID: 0, Label: "Community 0", Size: 5, TopFiles: []string{"internal/api/config.go", "internal/api/handler.go", "internal/api/logger.go"}},
		{ID: 2, Label: "Community 2", Size: 5, TopFiles: []string{"internal/worker/job.go", "internal/worker/metrics.go", "internal/worker/queue.go"}},
		{ID: 1, Label: "Community 1", Size: 4, TopFiles: []string{"internal/db/conn.go", "internal/db/migration.go", "internal/db/pool.go"}},
		{ID: 3, Label: "Community 3", Size: 1, TopFiles: []string{"docs/README.md"}},
	}
	if len(summary.Communities) != len(wantCommunities) {
		t.Fatalf("communities length: got %d want %d (%+v)", len(summary.Communities), len(wantCommunities), summary.Communities)
	}
	for i, want := range wantCommunities {
		got := summary.Communities[i]
		if got.ID != want.ID || got.Label != want.Label || got.Size != want.Size || fmt.Sprint(got.TopFiles) != fmt.Sprint(want.TopFiles) {
			t.Fatalf("communities[%d]: got %+v want %+v", i, got, want)
		}
	}

	if summary.Relations["calls"] != 7 || summary.Relations["references"] != 4 || summary.Relations["contains"] != 2 {
		t.Fatalf("unexpected relations: %+v", summary.Relations)
	}
	if summary.FileTypes["code"] != 14 || summary.FileTypes["document"] != 1 {
		t.Fatalf("unexpected file_types: %+v", summary.FileTypes)
	}
}

func TestSyncGraph_CrossReferencesGraphReport(t *testing.T) {
	s := newContextPackTestStore(t)
	if _, _, err := s.UpsertProjectCard(store.UpsertProjectCardParams{Slug: "acme"}); err != nil {
		t.Fatalf("UpsertProjectCard: %v", err)
	}
	repoDir := writeGraphFixture(t, "graph-small.json")
	withGraphReport(t, repoDir, "GRAPH_REPORT-small.md")

	if _, err := SyncGraph(s, "acme", repoDir, ""); err != nil {
		t.Fatalf("SyncGraph: %v", err)
	}

	card, err := s.GetProjectCard("acme")
	if err != nil {
		t.Fatalf("GetProjectCard: %v", err)
	}
	var summary GraphSummary
	if err := json.Unmarshal([]byte(*card.GraphSummary), &summary); err != nil {
		t.Fatalf("graph_summary is not valid JSON: %v", err)
	}

	if summary.ReportPath != "graphify-out/GRAPH_REPORT.md" {
		t.Fatalf("unexpected report_path: %q", summary.ReportPath)
	}
	if len(summary.Source) != 2 || summary.Source[1] != "graphify-out/GRAPH_REPORT.md" {
		t.Fatalf("unexpected source: %+v", summary.Source)
	}

	// The report's own God Nodes list is trusted verbatim; the file still
	// has to be resolved from graph.json since the report never carries one.
	if len(summary.GodNodes) == 0 || summary.GodNodes[0].Label != "svc.Handler" ||
		summary.GodNodes[0].Edges != 6 || summary.GodNodes[0].File != "internal/api/handler.go" {
		t.Fatalf("expected god node #0 resolved from the report, got %+v", summary.GodNodes[0])
	}

	// Community 0 was relabeled ("API Layer") in the report; community 1
	// never appears in the report (simulating a thin omission) and must
	// fall back to the default "Community N".
	byID := map[int]GraphCommunity{}
	for _, c := range summary.Communities {
		byID[c.ID] = c
	}
	if byID[0].Label != "API Layer" {
		t.Fatalf("community 0 should take its label from the report, got %q", byID[0].Label)
	}
	if byID[1].Label != "Community 1" {
		t.Fatalf("community 1 (absent from report) should fall back to the default, got %q", byID[1].Label)
	}
	if byID[2].Label != "Community 2" {
		t.Fatalf("community 2 should keep its default-looking report label, got %q", byID[2].Label)
	}
}

// TestSyncGraph_DisambiguatesDuplicateLabelsByDegree reproduces a pattern
// verified in a real middleware-sized GRAPH_REPORT.md: two unrelated nodes
// (different files) sharing one label both rank as god nodes. A naive
// label-only match would resolve both report entries to whichever node the
// map iteration returns first; matching on label+exact edge count must pick
// the correct file for each.
func TestSyncGraph_DisambiguatesDuplicateLabelsByDegree(t *testing.T) {
	s := newContextPackTestStore(t)
	if _, _, err := s.UpsertProjectCard(store.UpsertProjectCardParams{Slug: "acme"}); err != nil {
		t.Fatalf("UpsertProjectCard: %v", err)
	}
	repoDir := writeGraphFixture(t, "graph-duplicate-labels.json")
	withGraphReport(t, repoDir, "GRAPH_REPORT-duplicate-labels.md")

	if _, err := SyncGraph(s, "acme", repoDir, ""); err != nil {
		t.Fatalf("SyncGraph: %v", err)
	}
	card, err := s.GetProjectCard("acme")
	if err != nil {
		t.Fatalf("GetProjectCard: %v", err)
	}
	var summary GraphSummary
	if err := json.Unmarshal([]byte(*card.GraphSummary), &summary); err != nil {
		t.Fatalf("graph_summary is not valid JSON: %v", err)
	}

	if len(summary.GodNodes) < 2 {
		t.Fatalf("expected at least 2 god nodes, got %+v", summary.GodNodes)
	}
	want := []GodNode{
		{Label: "Standard", Edges: 5, File: "internal/pkgA/standard.go"},
		{Label: "Standard", Edges: 3, File: "internal/pkgB/standard.go"},
	}
	for i, w := range want {
		if summary.GodNodes[i] != w {
			t.Fatalf("god_nodes[%d]: got %+v want %+v", i, summary.GodNodes[i], w)
		}
	}
}

// TestGraphSummary_RealGraphDirsStayUnder64KiB is an opt-in smoke test
// against real graphify-out/ directories on disk, controlled by
// ENGRAM_TEST_GRAPH_DIRS (a comma-separated list of repo roots, each
// expected to hold graphify-out/graph.json). It is skipped whenever that
// variable is unset — which is always true in CI and on a fresh checkout —
// on purpose: a real graph.json can run into the tens of MiB, so this
// package's committed fixtures (testdata/) stay small and synthetic instead
// of pointing at whatever the developer happens to have checked out
// locally.
func TestGraphSummary_RealGraphDirsStayUnder64KiB(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("ENGRAM_TEST_GRAPH_DIRS"))
	if raw == "" {
		t.Skip("set ENGRAM_TEST_GRAPH_DIRS (comma-separated repo roots) to run this smoke test")
	}
	for _, repoDir := range strings.Split(raw, ",") {
		repoDir = strings.TrimSpace(repoDir)
		if repoDir == "" {
			continue
		}
		t.Run(filepath.Base(repoDir), func(t *testing.T) {
			s := newContextPackTestStore(t)
			slug := "local-" + strings.ToLower(filepath.Base(repoDir))
			if _, _, err := s.UpsertProjectCard(store.UpsertProjectCardParams{Slug: slug}); err != nil {
				t.Fatalf("UpsertProjectCard: %v", err)
			}
			result, err := SyncGraph(s, slug, repoDir, "")
			if err != nil {
				t.Fatalf("SyncGraph: %v", err)
			}
			card, err := s.GetProjectCard(slug)
			if err != nil {
				t.Fatalf("GetProjectCard: %v", err)
			}
			if card.GraphSummary == nil {
				t.Fatalf("graph_summary was not written")
			}
			size := len(*card.GraphSummary)
			if size > graphSummaryMaxBytes {
				t.Fatalf("graph_summary is %d bytes, over the %d cap", size, graphSummaryMaxBytes)
			}
			t.Logf("%s: nodes=%d edges=%d communities=%d graph_summary=%d bytes",
				slug, result.NodeCount, result.EdgeCount, result.CommunityCount, size)
		})
	}
}

func TestMarshalWithBudget_TruncatesCommunitiesAndRelationsBeforeGodNodes(t *testing.T) {
	const communityCount = 50
	summary := &GraphSummary{
		Source:         []string{"graphify-out/graph.json"},
		BuiltAtCommit:  "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		NodeCount:      1000,
		EdgeCount:      50000,
		CommunityCount: 200,
		ExtractedPct:   90,
		InferredPct:    10,
		FileTypes:      map[string]int{"code": 900, "document": 100},
		Relations:      map[string]int{},
		Communities:    make([]GraphCommunity, communityCount),
		GodNodes:       make([]GodNode, 10),
	}
	// A very long, distinctive label/file per god node so god_nodes alone
	// would already be sizable, and a long top_files/relations payload so
	// the budget must actually cut something. marshalWithBudget only trims
	// what it's handed — feeding it more than 10 communities here (unlike
	// the real topCommunities cap) is what forces the size over budget in
	// the first place.
	for i := 0; i < 10; i++ {
		summary.GodNodes[i] = GodNode{
			Label: fmt.Sprintf("SomeVeryLongSymbolNameThatTakesSpace_%d", i),
			Edges: 100 - i,
			File:  fmt.Sprintf("internal/some/very/long/package/path/file_%d.go", i),
		}
	}
	for i := 0; i < communityCount; i++ {
		files := make([]string, 40)
		for j := range files {
			files[j] = fmt.Sprintf("internal/community_%d/some/very/long/nested/package/path/file_%d.go", i, j)
		}
		summary.Communities[i] = GraphCommunity{
			ID:       i,
			Label:    fmt.Sprintf("Community %d", i),
			Size:     500 - i,
			TopFiles: files,
		}
	}
	for i := 0; i < 2000; i++ {
		summary.Relations[fmt.Sprintf("synthetic_relation_kind_number_%d", i)] = 2000 - i
	}

	unbounded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal (sanity): %v", err)
	}
	if len(unbounded) <= graphSummaryMaxBytes {
		t.Fatalf("fixture is not actually oversized (%d bytes) — strengthen it", len(unbounded))
	}
	godNodesBefore := len(summary.GodNodes)

	data, err := marshalWithBudget(summary, graphSummaryMaxBytes)
	if err != nil {
		t.Fatalf("marshalWithBudget: %v", err)
	}
	if len(data) > graphSummaryMaxBytes {
		t.Fatalf("marshalWithBudget produced %d bytes, over the %d cap", len(data), graphSummaryMaxBytes)
	}

	var out GraphSummary
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("truncated output is not valid JSON: %v", err)
	}
	if len(out.Relations) >= 300 {
		t.Fatalf("relations should have been shed, still has %d entries", len(out.Relations))
	}
	if len(out.GodNodes) != godNodesBefore {
		t.Fatalf("god_nodes should be preserved while communities/relations can still be cut: got %d want %d", len(out.GodNodes), godNodesBefore)
	}
}

func TestSyncGraph_SyntheticFiftyThousandEdgesStaysUnder64KiB(t *testing.T) {
	repoDir := t.TempDir()
	outDir := filepath.Join(repoDir, "graphify-out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeSyntheticGraph(t, filepath.Join(outDir, "graph.json"), 5000, 50000, 500)

	s := newContextPackTestStore(t)
	if _, _, err := s.UpsertProjectCard(store.UpsertProjectCardParams{Slug: "acme"}); err != nil {
		t.Fatalf("UpsertProjectCard: %v", err)
	}

	result, err := SyncGraph(s, "acme", repoDir, "")
	if err != nil {
		t.Fatalf("SyncGraph: %v", err)
	}
	if result.EdgeCount != 50000 {
		t.Fatalf("unexpected edge_count: %d", result.EdgeCount)
	}

	card, err := s.GetProjectCard("acme")
	if err != nil {
		t.Fatalf("GetProjectCard: %v", err)
	}
	if card.GraphSummary == nil {
		t.Fatalf("graph_summary was not written")
	}
	if size := len(*card.GraphSummary); size > graphSummaryMaxBytes {
		t.Fatalf("graph_summary is %d bytes, over the %d cap", size, graphSummaryMaxBytes)
	}
	var summary GraphSummary
	if err := json.Unmarshal([]byte(*card.GraphSummary), &summary); err != nil {
		t.Fatalf("graph_summary is not valid JSON: %v", err)
	}
}

// writeSyntheticGraph streams a synthetic graph.json with nodeCount nodes,
// edgeCount edges spread deterministically across them, and communityCount
// communities, directly to path — never held in memory as one JSON value.
func writeSyntheticGraph(t *testing.T, path string, nodeCount, edgeCount, communityCount int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create synthetic graph.json: %v", err)
	}
	defer f.Close()

	relations := []string{"calls", "contains", "defines", "imports", "inherits", "method", "references", "uses"}

	fmt.Fprint(f, `{"directed":false,"multigraph":false,"graph":{},"nodes":[`)
	for i := 0; i < nodeCount; i++ {
		if i > 0 {
			fmt.Fprint(f, ",")
		}
		fmt.Fprintf(f, `{"id":"n%d","label":"Sym%d","file_type":"code","source_file":"pkg%d/file%d.go","community":%d}`,
			i, i, i%50, i, i%communityCount)
	}
	fmt.Fprint(f, `],"links":[`)
	for i := 0; i < edgeCount; i++ {
		if i > 0 {
			fmt.Fprint(f, ",")
		}
		src := i % nodeCount
		dst := (i*7 + 3) % nodeCount
		conf := "EXTRACTED"
		if i%10 == 0 {
			conf = "INFERRED"
		}
		fmt.Fprintf(f, `{"source":"n%d","target":"n%d","relation":%q,"confidence":%q}`,
			src, dst, relations[i%len(relations)], conf)
	}
	fmt.Fprint(f, `],"hyperedges":[],"built_at_commit":"0123456789012345678901234567890123456789"}`)
}
