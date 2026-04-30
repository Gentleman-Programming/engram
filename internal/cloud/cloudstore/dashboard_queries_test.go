package cloudstore

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestGetContributorDetailReturnsScopedData seeds a read model with 2 contributors
// across 2 projects and asserts that GetContributorDetail("alice") returns only
// sessions/observations/prompts scoped to alice's projects. Satisfies (h).
func TestGetContributorDetailReturnsScopedData(t *testing.T) {
	aliceTime := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	bobTime := time.Date(2026, 4, 23, 11, 0, 0, 0, time.UTC)

	chunks := []dashboardChunkRow{
		// Alice: project "proj-alice"
		{
			chunkID: "chunk-alice-1", project: "proj-alice", createdBy: "alice",
			createdAt: aliceTime,
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"sess-alice-1","project":"proj-alice","started_at":"2026-04-23T08:00:00Z"}],
				"observations":[{"sync_id":"obs-alice-1","session_id":"sess-alice-1","project":"proj-alice","type":"decision","title":"Alice obs","created_at":"2026-04-23T08:10:00Z"}],
				"prompts":[{"sync_id":"prompt-alice-1","session_id":"sess-alice-1","project":"proj-alice","content":"Alice prompt","created_at":"2026-04-23T08:20:00Z"}]
			}`)),
		},
		// Bob: project "proj-bob"
		{
			chunkID: "chunk-bob-1", project: "proj-bob", createdBy: "bob",
			createdAt: bobTime,
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"sess-bob-1","project":"proj-bob","started_at":"2026-04-23T09:00:00Z"}],
				"observations":[{"sync_id":"obs-bob-1","session_id":"sess-bob-1","project":"proj-bob","type":"note","title":"Bob obs","created_at":"2026-04-23T09:10:00Z"}],
				"prompts":[{"sync_id":"prompt-bob-1","session_id":"sess-bob-1","project":"proj-bob","content":"Bob prompt","created_at":"2026-04-23T09:20:00Z"}]
			}`)),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	cs := &CloudStore{
		dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil },
	}

	contributor, sessions, observations, prompts, err := cs.GetContributorDetail("alice")
	if err != nil {
		t.Fatalf("GetContributorDetail: %v", err)
	}

	if contributor.CreatedBy != "alice" {
		t.Errorf("expected contributor.CreatedBy=%q, got %q", "alice", contributor.CreatedBy)
	}
	if len(sessions) == 0 {
		t.Error("expected at least one session for alice")
	}
	for _, s := range sessions {
		if s.Project != "proj-alice" {
			t.Errorf("expected session project=proj-alice, got %q", s.Project)
		}
	}
	if len(observations) == 0 {
		t.Error("expected at least one observation for alice")
	}
	for _, o := range observations {
		if o.Project != "proj-alice" {
			t.Errorf("expected observation project=proj-alice, got %q", o.Project)
		}
	}
	if len(prompts) == 0 {
		t.Error("expected at least one prompt for alice")
	}
	for _, p := range prompts {
		if p.Project != "proj-alice" {
			t.Errorf("expected prompt project=proj-alice, got %q", p.Project)
		}
	}

	// Assert bob's data is NOT included.
	for _, o := range observations {
		if o.Project == "proj-bob" {
			t.Errorf("expected bob's observations to be excluded, got project=%q", o.Project)
		}
	}
}

// TestListDistinctTypesScansReadModel seeds a read model with observations of types
// "bugfix", "architecture", "", "bugfix" again and asserts ListDistinctTypes returns
// ["architecture", "bugfix"] sorted, no empty. Satisfies (m).
func TestListDistinctTypesScansReadModel(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			chunkID: "chunk-types-1", project: "proj-x", createdBy: "user",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"observations":[
					{"sync_id":"o-1","session_id":"s-1","project":"proj-x","type":"bugfix","title":"Bug","created_at":"2026-04-23T08:00:00Z"},
					{"sync_id":"o-2","session_id":"s-1","project":"proj-x","type":"architecture","title":"Arch","created_at":"2026-04-23T08:01:00Z"},
					{"sync_id":"o-3","session_id":"s-1","project":"proj-x","type":"","title":"Empty type","created_at":"2026-04-23T08:02:00Z"},
					{"sync_id":"o-4","session_id":"s-1","project":"proj-x","type":"bugfix","title":"Bug2","created_at":"2026-04-23T08:03:00Z"}
				]
			}`)),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	cs := &CloudStore{
		dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil },
	}

	types, err := cs.ListDistinctTypes()
	if err != nil {
		t.Fatalf("ListDistinctTypes: %v", err)
	}

	if len(types) != 2 {
		t.Fatalf("expected 2 distinct types, got %d: %v", len(types), types)
	}
	if types[0] != "architecture" || types[1] != "bugfix" {
		t.Errorf("expected [architecture, bugfix] sorted, got %v", types)
	}
}

// TestDashboardRowDetailFields seeds a read model with an observation containing
// content, topic_key, and tool_name and asserts the new flat-row fields are populated.
// Satisfies REQ-102.
func TestDashboardRowDetailFields(t *testing.T) {
	toolName := "mem_save"
	topicKey := "architecture/auth-model"
	chunks := []dashboardChunkRow{
		{
			chunkID:   "chunk-detail-1",
			project:   "proj-detail",
			createdBy: "alan@example.com",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"sess-detail","project":"proj-detail","started_at":"2026-04-23T08:00:00Z","directory":"/workspace","ended_at":"2026-04-23T09:00:00Z","summary":"Session summary text"}],
				"observations":[{
					"sync_id":"obs-detail",
					"session_id":"sess-detail",
					"project":"proj-detail",
					"type":"decision",
					"title":"Detail Title",
					"content":"Detail content text",
					"tool_name":"mem_save",
					"topic_key":"architecture/auth-model",
					"created_at":"2026-04-23T08:10:00Z"
				}],
				"prompts":[{
					"sync_id":"prompt-detail",
					"session_id":"sess-detail",
					"project":"proj-detail",
					"content":"Prompt detail text",
					"created_at":"2026-04-23T08:20:00Z"
				}]
			}`)),
		},
	}

	_ = toolName
	_ = topicKey

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	detail, ok := model.projectDetails["proj-detail"]
	if !ok {
		t.Fatalf("expected project detail for proj-detail")
	}

	// Assert DashboardObservationRow.Content, TopicKey, ToolName, ChunkID are populated.
	if len(detail.Observations) == 0 {
		t.Fatalf("expected at least one observation in detail")
	}
	obs := detail.Observations[0]
	if obs.Content == "" {
		t.Errorf("expected DashboardObservationRow.Content to be non-empty, got %q", obs.Content)
	}
	if obs.TopicKey == "" {
		t.Errorf("expected DashboardObservationRow.TopicKey to be non-empty, got %q", obs.TopicKey)
	}
	if obs.ToolName == "" {
		t.Errorf("expected DashboardObservationRow.ToolName to be non-empty, got %q", obs.ToolName)
	}
	if obs.ChunkID == "" {
		t.Errorf("expected DashboardObservationRow.ChunkID to be non-empty, got %q", obs.ChunkID)
	}

	// Assert DashboardSessionRow.EndedAt, Summary, Directory are populated.
	if len(detail.Sessions) == 0 {
		t.Fatalf("expected at least one session in detail")
	}
	sess := detail.Sessions[0]
	if sess.EndedAt == "" {
		t.Errorf("expected DashboardSessionRow.EndedAt to be non-empty, got %q", sess.EndedAt)
	}
	if sess.Summary == "" {
		t.Errorf("expected DashboardSessionRow.Summary to be non-empty, got %q", sess.Summary)
	}
	if sess.Directory == "" {
		t.Errorf("expected DashboardSessionRow.Directory to be non-empty, got %q", sess.Directory)
	}

	// Assert DashboardPromptRow.ChunkID is populated.
	if len(detail.Prompts) == 0 {
		t.Fatalf("expected at least one prompt in detail")
	}
	prompt := detail.Prompts[0]
	if prompt.ChunkID == "" {
		t.Errorf("expected DashboardPromptRow.ChunkID to be non-empty, got %q", prompt.ChunkID)
	}
}

// TestCloudstoreSystemHealthAggregates asserts that SystemHealth() counts match
// what was seeded in the read model. Satisfies REQ-105.
// This test uses the loadDashboardReadModel override so it does NOT require a real DB.
func TestCloudstoreSystemHealthAggregates(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			chunkID: "chunk-health-a", project: "proj-1", createdBy: "user-1",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"s-1","project":"proj-1","started_at":"2026-04-23T08:00:00Z"}],
				"observations":[
					{"sync_id":"o-1","session_id":"s-1","project":"proj-1","type":"decision","title":"O1","created_at":"2026-04-23T08:10:00Z"},
					{"sync_id":"o-2","session_id":"s-1","project":"proj-1","type":"note","title":"O2","created_at":"2026-04-23T08:11:00Z"}
				],
				"prompts":[{"sync_id":"p-1","session_id":"s-1","project":"proj-1","content":"P1","created_at":"2026-04-23T08:20:00Z"}]
			}`)),
		},
		{
			chunkID: "chunk-health-b", project: "proj-2", createdBy: "user-2",
			createdAt: time.Date(2026, 4, 23, 11, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[
					{"id":"s-2","project":"proj-2","started_at":"2026-04-23T09:00:00Z"},
					{"id":"s-3","project":"proj-2","started_at":"2026-04-23T09:30:00Z"}
				],
				"observations":[
					{"sync_id":"o-3","session_id":"s-2","project":"proj-2","type":"decision","title":"O3","created_at":"2026-04-23T09:10:00Z"},
					{"sync_id":"o-4","session_id":"s-2","project":"proj-2","type":"note","title":"O4","created_at":"2026-04-23T09:11:00Z"},
					{"sync_id":"o-5","session_id":"s-3","project":"proj-2","type":"bugfix","title":"O5","created_at":"2026-04-23T09:40:00Z"},
					{"sync_id":"o-6","session_id":"s-3","project":"proj-2","type":"bugfix","title":"O6","created_at":"2026-04-23T09:41:00Z"},
					{"sync_id":"o-7","session_id":"s-3","project":"proj-2","type":"bugfix","title":"O7","created_at":"2026-04-23T09:42:00Z"},
					{"sync_id":"o-8","session_id":"s-3","project":"proj-2","type":"bugfix","title":"O8","created_at":"2026-04-23T09:43:00Z"},
					{"sync_id":"o-9","session_id":"s-3","project":"proj-2","type":"bugfix","title":"O9","created_at":"2026-04-23T09:44:00Z"},
					{"sync_id":"o-10","session_id":"s-3","project":"proj-2","type":"bugfix","title":"O10","created_at":"2026-04-23T09:45:00Z"}
				],
				"prompts":[
					{"sync_id":"p-2","session_id":"s-2","project":"proj-2","content":"P2","created_at":"2026-04-23T09:20:00Z"},
					{"sync_id":"p-3","session_id":"s-2","project":"proj-2","content":"P3","created_at":"2026-04-23T09:21:00Z"},
					{"sync_id":"p-4","session_id":"s-3","project":"proj-2","content":"P4","created_at":"2026-04-23T09:50:00Z"},
					{"sync_id":"p-5","session_id":"s-3","project":"proj-2","content":"P5","created_at":"2026-04-23T09:51:00Z"}
				]
			}`)),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	// Inject model into a CloudStore via the override hook.
	cs := &CloudStore{
		dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil },
	}

	health, err := cs.SystemHealth()
	if err != nil {
		t.Fatalf("SystemHealth: %v", err)
	}

	if health.Projects != 2 {
		t.Errorf("expected Projects=2, got %d", health.Projects)
	}
	if health.Contributors != 2 {
		t.Errorf("expected Contributors=2, got %d", health.Contributors)
	}
	if health.Sessions != 3 {
		t.Errorf("expected Sessions=3, got %d", health.Sessions)
	}
	if health.Observations != 10 {
		t.Errorf("expected Observations=10, got %d", health.Observations)
	}
	if health.Prompts != 5 {
		t.Errorf("expected Prompts=5, got %d", health.Prompts)
	}
	// DBConnected should be false (no real DB).
	if health.DBConnected {
		t.Errorf("expected DBConnected=false for nil db, got true")
	}
}

// ─── Post-verify Hotfix: Regression Tests (Bugs 1, 2, 3) ────────────────────

// ─── hotfixChunk builds a dashboardChunkRow with both observations[] and mutations[]
// for the same sync_id, reproducing the production scenario where filterByPendingMutations
// serializes both arrays into the same chunk payload stored in cloud_chunks.
// The mutation payload is a JSON-encoded string (as produced by synthesizeMutationsFromChunk).
func hotfixChunk(t *testing.T, chunkID, project, sessionID, syncID, content, topicKey, toolName string) dashboardChunkRow {
	t.Helper()
	import_ := func(s any) string {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("hotfixChunk marshal: %v", err)
		}
		return string(b)
	}
	// mutation payload is the JSON string that chunkcodec.DecodeSyncMutationPayload expects.
	mutPayload := import_(map[string]any{
		"sync_id":     syncID,
		"session_id":  sessionID,
		"type":        "decision",
		"title":       "Hotfix Title",
		"content":     content,
		"topic_key":   topicKey,
		"tool_name":   toolName,
		"created_at":  "2026-04-23T08:10:00Z",
		"deleted":     false,
		"hard_delete": false,
	})
	chunkJSON, err := json.Marshal(map[string]any{
		"sessions": []map[string]any{
			{"id": sessionID, "project": project, "started_at": "2026-04-23T08:00:00Z"},
		},
		"observations": []map[string]any{
			{
				"sync_id":    syncID,
				"session_id": sessionID,
				"project":    project,
				"type":       "decision",
				"title":      "Hotfix Title",
				"content":    content,
				"tool_name":  toolName,
				"topic_key":  topicKey,
				"created_at": "2026-04-23T08:10:00Z",
			},
		},
		"mutations": []map[string]any{
			{
				"entity":     "observation",
				"entity_key": syncID,
				"op":         "upsert",
				"payload":    mutPayload,
			},
		},
	})
	if err != nil {
		t.Fatalf("hotfixChunk marshal chunk: %v", err)
	}
	return dashboardChunkRow{
		chunkID:   chunkID,
		project:   project,
		createdBy: "user",
		createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
		parsed:    parseMustChunk(t, chunkJSON),
	}
}

// TestObservationContentExtractedFromChunkPayload (Bug 1 regression)
// Seeds a chunk with BOTH an observations[] entry AND a matching mutation[] entry
// for the same sync_id. Before the fix, the mutation path overwrites Content with "".
// After the fix, Content must equal the value from the observations[] entry.
func TestObservationContentExtractedFromChunkPayload(t *testing.T) {
	chunks := []dashboardChunkRow{
		hotfixChunk(t, "chunk-hotfix-1", "proj-hotfix", "sess-hotfix", "obs-hotfix",
			"Expected content here", "arch/hotfix", "mem_save"),
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	detail, ok := model.projectDetails["proj-hotfix"]
	if !ok {
		t.Fatalf("expected project detail for proj-hotfix")
	}
	if len(detail.Observations) == 0 {
		t.Fatalf("expected at least one observation in detail")
	}
	obs := detail.Observations[0]
	// Bug 1: Content must NOT be blank when a mutation follows the observations entry.
	if obs.Content == "" {
		t.Errorf("Bug 1: DashboardObservationRow.Content is empty — mutation path clobbered the observations path value")
	}
	if obs.Content != "Expected content here" {
		t.Errorf("Bug 1: expected Content=%q, got %q", "Expected content here", obs.Content)
	}
}

// TestObservationChunkIDAndSessionIDExtracted (Bug 2 regression)
// When a chunk has both observations[] and mutations[], the ChunkID must be preserved
// (mutations don't carry a chunk ID). Before the fix, ChunkID is "" after mutation upsert,
// making the href /dashboard/observations/{proj}/{sid}// invalid.
func TestObservationChunkIDAndSessionIDExtracted(t *testing.T) {
	chunks := []dashboardChunkRow{
		hotfixChunk(t, "chunk-href-1", "proj-href", "sess-href", "obs-href",
			"Some content", "", ""),
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	detail, ok := model.projectDetails["proj-href"]
	if !ok {
		t.Fatalf("expected project detail for proj-href")
	}
	if len(detail.Observations) == 0 {
		t.Fatalf("expected at least one observation in detail")
	}
	obs := detail.Observations[0]
	// Bug 2: ChunkID must be non-empty so the href isn't malformed.
	if obs.ChunkID == "" {
		t.Errorf("Bug 2: DashboardObservationRow.ChunkID is empty — mutation path wiped chunk-href-1")
	}
	if obs.ChunkID != "chunk-href-1" {
		t.Errorf("Bug 2: expected ChunkID=%q, got %q", "chunk-href-1", obs.ChunkID)
	}
	// SessionID must also be non-empty.
	if obs.SessionID == "" {
		t.Errorf("Bug 2: DashboardObservationRow.SessionID is empty")
	}
	if obs.SessionID != "sess-href" {
		t.Errorf("Bug 2: expected SessionID=%q, got %q", "sess-href", obs.SessionID)
	}
}

// TestGetSessionDetailReturnsItsObservations (Bug 3 regression)
// Seeds a session + observations (via observations[] + mutations[]), then calls
// GetSessionDetail and asserts the observations slice is non-empty.
// Before the fix, observations' SessionID is wiped by the mutation path, so the
// filter (o.SessionID == sessionID) matches nothing → empty observations slice.
func TestGetSessionDetailReturnsItsObservations(t *testing.T) {
	chunks := []dashboardChunkRow{
		hotfixChunk(t, "chunk-sess-1", "proj-sess", "sess-detail-a", "obs-sess",
			"Session obs content", "", ""),
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	cs := &CloudStore{
		dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil },
	}

	sess, obs, _, err := cs.GetSessionDetail("proj-sess", "sess-detail-a")
	if err != nil {
		t.Fatalf("GetSessionDetail: %v", err)
	}
	if sess.SessionID != "sess-detail-a" {
		t.Errorf("expected SessionID=%q, got %q", "sess-detail-a", sess.SessionID)
	}
	// Bug 3: observations must be non-empty for the session trace page.
	if len(obs) == 0 {
		t.Errorf("Bug 3: GetSessionDetail returned 0 observations — mutation wiped SessionID causing session-filter mismatch")
	}
	if len(obs) > 0 && obs[0].Content == "" {
		t.Errorf("Bug 1+3: observation Content is empty even though source data had content")
	}
}

// TestDashboardPaginationSortStability asserts that calling ListRecentSessionsPaginated
// twice with the same parameters returns the same result. Satisfies Design Decision 1.
func TestDashboardPaginationSortStability(t *testing.T) {
	// Seed 50 sessions across 2 projects.
	chunks := make([]dashboardChunkRow, 0, 50)
	for i := 0; i < 25; i++ {
		ts := time.Date(2026, 4, 1, i, 0, 0, 0, time.UTC).Format(time.RFC3339)
		sessID := "sess-a-" + ts
		chunks = append(chunks, dashboardChunkRow{
			chunkID: "chunk-a-" + ts, project: "proj-a", createdBy: "user-a",
			createdAt: time.Date(2026, 4, 1, i, 0, 0, 0, time.UTC),
			parsed:    parseMustChunk(t, []byte(`{"sessions":[{"id":"`+sessID+`","project":"proj-a","started_at":"`+ts+`"}]}`)),
		})
	}
	for i := 0; i < 25; i++ {
		ts := time.Date(2026, 4, 2, i, 0, 0, 0, time.UTC).Format(time.RFC3339)
		sessID := "sess-b-" + ts
		chunks = append(chunks, dashboardChunkRow{
			chunkID: "chunk-b-" + ts, project: "proj-b", createdBy: "user-b",
			createdAt: time.Date(2026, 4, 2, i, 0, 0, 0, time.UTC),
			parsed:    parseMustChunk(t, []byte(`{"sessions":[{"id":"`+sessID+`","project":"proj-b","started_at":"`+ts+`"}]}`)),
		})
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	cs := &CloudStore{
		dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil },
	}

	// Page 3 of size 5 (offset 10, 5 sessions).
	page1, total1, err := cs.ListRecentSessionsPaginated("", "", 5, 10)
	if err != nil {
		t.Fatalf("ListRecentSessionsPaginated call 1: %v", err)
	}
	page2, total2, err := cs.ListRecentSessionsPaginated("", "", 5, 10)
	if err != nil {
		t.Fatalf("ListRecentSessionsPaginated call 2: %v", err)
	}

	if total1 != total2 {
		t.Errorf("expected stable total %d == %d", total1, total2)
	}
	if len(page1) != len(page2) {
		t.Fatalf("expected stable page length %d == %d", len(page1), len(page2))
	}
	for i := range page1 {
		if page1[i].SessionID != page2[i].SessionID {
			t.Errorf("page[%d] instability: %q != %q", i, page1[i].SessionID, page2[i].SessionID)
		}
	}
}

// ─── Judgment Day Hotfix RED Tests ───────────────────────────────────────────

// TestObservationDetailDistinguishesMultipleObsPerChunk (C1 RED)
// Seeds two observations in the SAME chunk + session but with different SyncIDs.
// Asserts each resolves to its own row when looked up by SyncID.
func TestObservationDetailDistinguishesMultipleObsPerChunk(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			chunkID: "shared-chunk", project: "proj-c1", createdBy: "user",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"s-c1","project":"proj-c1","started_at":"2026-04-23T08:00:00Z"}],
				"observations":[
					{"sync_id":"obs-c1-alpha","session_id":"s-c1","project":"proj-c1","type":"decision","title":"Alpha","content":"Alpha content","created_at":"2026-04-23T08:10:00Z"},
					{"sync_id":"obs-c1-beta","session_id":"s-c1","project":"proj-c1","type":"bugfix","title":"Beta","content":"Beta content","created_at":"2026-04-23T08:11:00Z"}
				]
			}`)),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}
	cs := &CloudStore{
		dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil },
	}

	// Look up alpha by syncID.
	obsAlpha, _, _, err := cs.GetObservationDetail("proj-c1", "s-c1", "obs-c1-alpha")
	if err != nil {
		t.Fatalf("GetObservationDetail(alpha): %v", err)
	}
	if obsAlpha.Title != "Alpha" {
		t.Errorf("C1: expected alpha Title=%q, got %q", "Alpha", obsAlpha.Title)
	}
	if obsAlpha.SyncID != "obs-c1-alpha" {
		t.Errorf("C1: expected alpha SyncID=%q, got %q", "obs-c1-alpha", obsAlpha.SyncID)
	}

	// Look up beta by syncID — must resolve to a different observation.
	obsBeta, _, _, err := cs.GetObservationDetail("proj-c1", "s-c1", "obs-c1-beta")
	if err != nil {
		t.Fatalf("GetObservationDetail(beta): %v", err)
	}
	if obsBeta.Title != "Beta" {
		t.Errorf("C1: expected beta Title=%q, got %q", "Beta", obsBeta.Title)
	}
	if obsBeta.SyncID != "obs-c1-beta" {
		t.Errorf("C1: expected beta SyncID=%q, got %q", "obs-c1-beta", obsBeta.SyncID)
	}
}

// TestPromptDetailDistinguishesMultiplePromptsPerChunk (C1 RED for prompts)
// Same shape as above but for prompts.
func TestPromptDetailDistinguishesMultiplePromptsPerChunk(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			chunkID: "shared-chunk-p", project: "proj-c1p", createdBy: "user",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"s-c1p","project":"proj-c1p","started_at":"2026-04-23T08:00:00Z"}],
				"prompts":[
					{"sync_id":"prompt-c1-alpha","session_id":"s-c1p","project":"proj-c1p","content":"Alpha prompt","created_at":"2026-04-23T08:10:00Z"},
					{"sync_id":"prompt-c1-beta","session_id":"s-c1p","project":"proj-c1p","content":"Beta prompt","created_at":"2026-04-23T08:11:00Z"}
				]
			}`)),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}
	cs := &CloudStore{
		dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil },
	}

	// Look up alpha.
	promptAlpha, _, _, err := cs.GetPromptDetail("proj-c1p", "s-c1p", "prompt-c1-alpha")
	if err != nil {
		t.Fatalf("GetPromptDetail(alpha): %v", err)
	}
	if promptAlpha.Content != "Alpha prompt" {
		t.Errorf("C1: expected alpha Content=%q, got %q", "Alpha prompt", promptAlpha.Content)
	}
	if promptAlpha.SyncID != "prompt-c1-alpha" {
		t.Errorf("C1: expected alpha SyncID=%q, got %q", "prompt-c1-alpha", promptAlpha.SyncID)
	}

	// Look up beta.
	promptBeta, _, _, err := cs.GetPromptDetail("proj-c1p", "s-c1p", "prompt-c1-beta")
	if err != nil {
		t.Fatalf("GetPromptDetail(beta): %v", err)
	}
	if promptBeta.Content != "Beta prompt" {
		t.Errorf("C1: expected beta Content=%q, got %q", "Beta prompt", promptBeta.Content)
	}
	if promptBeta.SyncID != "prompt-c1-beta" {
		t.Errorf("C1: expected beta SyncID=%q, got %q", "prompt-c1-beta", promptBeta.SyncID)
	}
}

// TestPromptMutationPreservesChunkID (C2 RED)
// Seeds a prompt via chunks[], then applies a mutation for the same SyncID.
// Asserts ChunkID is NOT wiped by the mutation upsert.
func TestPromptMutationPreservesChunkID(t *testing.T) {
	import_ := func(t *testing.T, s any) string {
		t.Helper()
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}
	mutPayload := import_(t, map[string]any{
		"sync_id":    "prompt-mut-1",
		"session_id": "sess-mut",
		"content":    "Updated prompt content",
		"created_at": "2026-04-23T08:10:00Z",
	})
	chunkJSON, err := json.Marshal(map[string]any{
		"sessions": []map[string]any{
			{"id": "sess-mut", "project": "proj-mut", "started_at": "2026-04-23T08:00:00Z"},
		},
		"prompts": []map[string]any{
			{
				"sync_id":    "prompt-mut-1",
				"session_id": "sess-mut",
				"project":    "proj-mut",
				"content":    "Original prompt content",
				"created_at": "2026-04-23T08:10:00Z",
			},
		},
		"mutations": []map[string]any{
			{
				"entity":     "prompt",
				"entity_key": "prompt-mut-1",
				"op":         "upsert",
				"payload":    mutPayload,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}

	chunks := []dashboardChunkRow{
		{
			chunkID: "chunk-mut-1", project: "proj-mut", createdBy: "user",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed:    parseMustChunk(t, chunkJSON),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	detail, ok := model.projectDetails["proj-mut"]
	if !ok {
		t.Fatalf("expected project detail for proj-mut")
	}
	if len(detail.Prompts) == 0 {
		t.Fatalf("expected at least one prompt in detail")
	}
	prompt := detail.Prompts[0]
	// C2: ChunkID must survive the mutation upsert.
	if prompt.ChunkID == "" {
		t.Errorf("C2: DashboardPromptRow.ChunkID is empty after mutation — prompt URL will be malformed")
	}
	if prompt.ChunkID != "chunk-mut-1" {
		t.Errorf("C2: expected ChunkID=%q, got %q", "chunk-mut-1", prompt.ChunkID)
	}
}

// TestSessionMutationPreservesCloseFields (C3 RED)
// Seeds a session with EndedAt+Summary+Directory via chunks[],
// then applies a mutation for the same sessionID with only StartedAt.
// Asserts EndedAt/Summary/Directory are NOT wiped.
func TestSessionMutationPreservesCloseFields(t *testing.T) {
	import_ := func(t *testing.T, s any) string {
		t.Helper()
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}
	mutPayload := import_(t, map[string]any{
		"id":         "sess-close",
		"project":    "proj-close",
		"started_at": "2026-04-23T08:00:00Z",
	})
	chunkJSON, err := json.Marshal(map[string]any{
		"sessions": []map[string]any{
			{
				"id":         "sess-close",
				"project":    "proj-close",
				"started_at": "2026-04-23T08:00:00Z",
				"ended_at":   "2026-04-23T09:00:00Z",
				"summary":    "Session summary text",
				"directory":  "/workspace/proj",
			},
		},
		"mutations": []map[string]any{
			{
				"entity":     "session",
				"entity_key": "sess-close",
				"op":         "upsert",
				"payload":    mutPayload,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}

	chunks := []dashboardChunkRow{
		{
			chunkID: "chunk-close-1", project: "proj-close", createdBy: "user",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed:    parseMustChunk(t, chunkJSON),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	detail, ok := model.projectDetails["proj-close"]
	if !ok {
		t.Fatalf("expected project detail for proj-close")
	}
	if len(detail.Sessions) == 0 {
		t.Fatalf("expected at least one session in detail")
	}
	sess := detail.Sessions[0]
	// C3: EndedAt/Summary/Directory must survive the mutation upsert.
	if sess.EndedAt == "" {
		t.Errorf("C3: DashboardSessionRow.EndedAt wiped by mutation (was: 2026-04-23T09:00:00Z)")
	}
	if sess.Summary == "" {
		t.Errorf("C3: DashboardSessionRow.Summary wiped by mutation (was: Session summary text)")
	}
	if sess.Directory == "" {
		t.Errorf("C3: DashboardSessionRow.Directory wiped by mutation (was: /workspace/proj)")
	}
}

// ─── Judgment Day Round 2 Hotfix RED Tests ───────────────────────────────────

// TestSessionMutationPreservesStartedAt (R2-5)
// Seeds a session with StartedAt via chunks[], then applies a mutation that omits started_at.
// Asserts StartedAt is preserved (not wiped).
func TestSessionMutationPreservesStartedAt(t *testing.T) {
	import_ := func(t *testing.T, s any) string {
		t.Helper()
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}
	// Mutation omits started_at entirely.
	mutPayload := import_(t, map[string]any{
		"id":        "sess-r25",
		"project":   "proj-r25",
		"ended_at":  "2026-04-23T09:00:00Z",
		"summary":   "Updated summary",
		"directory": "/workspace/r25",
	})
	chunkJSON, err := json.Marshal(map[string]any{
		"sessions": []map[string]any{
			{
				"id":         "sess-r25",
				"project":    "proj-r25",
				"started_at": "2026-04-23T08:00:00Z",
				"ended_at":   "2026-04-23T08:30:00Z",
			},
		},
		"mutations": []map[string]any{
			{
				"entity":     "session",
				"entity_key": "sess-r25",
				"op":         "upsert",
				"payload":    mutPayload,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}

	chunks := []dashboardChunkRow{
		{
			chunkID: "chunk-r25", project: "proj-r25", createdBy: "user",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed:    parseMustChunk(t, chunkJSON),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	detail, ok := model.projectDetails["proj-r25"]
	if !ok {
		t.Fatalf("expected project detail for proj-r25")
	}
	if len(detail.Sessions) == 0 {
		t.Fatalf("expected at least one session in detail")
	}
	sess := detail.Sessions[0]
	// R2-5: StartedAt must survive when mutation omits it.
	if sess.StartedAt == "" {
		t.Errorf("R2-5: DashboardSessionRow.StartedAt wiped by mutation that omitted started_at (was: 2026-04-23T08:00:00Z)")
	}
	if sess.StartedAt != "2026-04-23T08:00:00Z" {
		t.Errorf("R2-5: expected StartedAt=%q, got %q", "2026-04-23T08:00:00Z", sess.StartedAt)
	}
}

// TestObservationMutationPreservesAllScalarFields (R2-6)
// Seeds an observation via observations[] with SessionID/Type/Title/CreatedAt populated,
// then applies a mutation that carries only Content — asserts all other fields survive.
func TestObservationMutationPreservesAllScalarFields(t *testing.T) {
	import_ := func(t *testing.T, s any) string {
		t.Helper()
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}
	// Mutation only updates Content. All other fields are zero/empty.
	mutPayload := import_(t, map[string]any{
		"sync_id": "obs-r26",
		"content": "Updated content only",
	})
	chunkJSON, err := json.Marshal(map[string]any{
		"sessions": []map[string]any{
			{"id": "sess-r26", "project": "proj-r26", "started_at": "2026-04-23T08:00:00Z"},
		},
		"observations": []map[string]any{
			{
				"sync_id":    "obs-r26",
				"session_id": "sess-r26",
				"project":    "proj-r26",
				"type":       "decision",
				"title":      "Original Title",
				"content":    "Original content",
				"created_at": "2026-04-23T08:10:00Z",
			},
		},
		"mutations": []map[string]any{
			{
				"entity":     "observation",
				"entity_key": "obs-r26",
				"op":         "upsert",
				"payload":    mutPayload,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}

	chunks := []dashboardChunkRow{
		{
			chunkID: "chunk-r26", project: "proj-r26", createdBy: "user",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed:    parseMustChunk(t, chunkJSON),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	detail, ok := model.projectDetails["proj-r26"]
	if !ok {
		t.Fatalf("expected project detail for proj-r26")
	}
	if len(detail.Observations) == 0 {
		t.Fatalf("expected at least one observation in detail")
	}
	obs := detail.Observations[0]

	// R2-6: All scalar fields must survive when mutation omits them.
	if obs.SessionID == "" {
		t.Errorf("R2-6: DashboardObservationRow.SessionID wiped by partial mutation")
	}
	if obs.SessionID != "sess-r26" {
		t.Errorf("R2-6: expected SessionID=%q, got %q", "sess-r26", obs.SessionID)
	}
	if obs.Type == "" {
		t.Errorf("R2-6: DashboardObservationRow.Type wiped by partial mutation (was: decision)")
	}
	if obs.Type != "decision" {
		t.Errorf("R2-6: expected Type=%q, got %q", "decision", obs.Type)
	}
	if obs.Title == "" {
		t.Errorf("R2-6: DashboardObservationRow.Title wiped by partial mutation (was: Original Title)")
	}
	if obs.Title != "Original Title" {
		t.Errorf("R2-6: expected Title=%q, got %q", "Original Title", obs.Title)
	}
	if obs.CreatedAt == "" {
		t.Errorf("R2-6: DashboardObservationRow.CreatedAt wiped by partial mutation (was: 2026-04-23T08:10:00Z)")
	}
	if obs.CreatedAt != "2026-04-23T08:10:00Z" {
		t.Errorf("R2-6: expected CreatedAt=%q, got %q", "2026-04-23T08:10:00Z", obs.CreatedAt)
	}
	// Content should be updated to the mutation value.
	if obs.Content != "Updated content only" {
		t.Errorf("R2-6: expected Content=%q (from mutation), got %q", "Updated content only", obs.Content)
	}
}

// ─── Judgment Day Round 4 Hotfix RED Tests ───────────────────────────────────

// TestSessionsArrayPassPreservesAcrossChunks (R4-1 RED)
// Seeds two chunks: chunk-open carries started_at+project for a session,
// chunk-close carries ended_at+summary for the same session ID.
// Asserts the final row has ALL four fields: StartedAt, Project, EndedAt, Summary.
// Before the fix, upsertDashboardSession blindly overwrites on the second chunk.
func TestSessionsArrayPassPreservesAcrossChunks(t *testing.T) {
	openChunkJSON, err := json.Marshal(map[string]any{
		"sessions": []map[string]any{
			{
				"id":         "sess-r41",
				"project":    "proj-r41",
				"started_at": "2026-04-23T08:00:00Z",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal open chunk: %v", err)
	}
	closeChunkJSON, err := json.Marshal(map[string]any{
		"sessions": []map[string]any{
			{
				"id":       "sess-r41",
				"ended_at": "2026-04-23T09:00:00Z",
				"summary":  "Session summary from close chunk",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal close chunk: %v", err)
	}

	chunks := []dashboardChunkRow{
		{
			chunkID: "chunk-r41-open", project: "proj-r41", createdBy: "user",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed:    parseMustChunk(t, openChunkJSON),
		},
		{
			chunkID: "chunk-r41-close", project: "proj-r41", createdBy: "user",
			createdAt: time.Date(2026, 4, 23, 10, 1, 0, 0, time.UTC),
			parsed:    parseMustChunk(t, closeChunkJSON),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	detail, ok := model.projectDetails["proj-r41"]
	if !ok {
		t.Fatalf("expected project detail for proj-r41")
	}
	if len(detail.Sessions) == 0 {
		t.Fatalf("expected at least one session in detail")
	}
	sess := detail.Sessions[0]

	// R4-1: StartedAt from open chunk must survive the close-chunk sessions-array pass.
	if sess.StartedAt == "" {
		t.Errorf("R4-1: StartedAt wiped by close chunk sessions-array pass (was: 2026-04-23T08:00:00Z)")
	}
	if sess.StartedAt != "2026-04-23T08:00:00Z" {
		t.Errorf("R4-1: expected StartedAt=%q, got %q", "2026-04-23T08:00:00Z", sess.StartedAt)
	}

	// R4-1: EndedAt from close chunk must be present.
	if sess.EndedAt == "" {
		t.Errorf("R4-1: EndedAt from close chunk is missing")
	}
	if sess.EndedAt != "2026-04-23T09:00:00Z" {
		t.Errorf("R4-1: expected EndedAt=%q, got %q", "2026-04-23T09:00:00Z", sess.EndedAt)
	}

	// R4-1: Summary from close chunk must be present.
	if sess.Summary == "" {
		t.Errorf("R4-1: Summary from close chunk is missing")
	}
	if sess.Summary != "Session summary from close chunk" {
		t.Errorf("R4-1: expected Summary=%q, got %q", "Session summary from close chunk", sess.Summary)
	}

	// R4-1: Project from open chunk must survive.
	if sess.Project == "" {
		t.Errorf("R4-1: Project wiped by close chunk sessions-array pass")
	}
	if sess.Project != "proj-r41" {
		t.Errorf("R4-1: expected Project=%q, got %q", "proj-r41", sess.Project)
	}
}

// TestPromptMutationPreservesSessionIDAndCreatedAt (R3-4 RED)
// Seeds a prompt via chunks[] with a known SessionID and CreatedAt,
// then applies a partial mutation that omits session_id and created_at.
// Asserts SessionID and CreatedAt are NOT wiped by the mutation upsert.
func TestPromptMutationPreservesSessionIDAndCreatedAt(t *testing.T) {
	import_ := func(t *testing.T, s any) string {
		t.Helper()
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}
	// Mutation payload — content only, no session_id or created_at.
	mutPayload := import_(t, map[string]any{
		"sync_id": "prompt-r34-1",
		"content": "Updated prompt content via mutation",
	})
	chunkJSON, err := json.Marshal(map[string]any{
		"sessions": []map[string]any{
			{"id": "sess-r34", "project": "proj-r34", "started_at": "2026-04-23T09:00:00Z"},
		},
		"prompts": []map[string]any{
			{
				"sync_id":    "prompt-r34-1",
				"session_id": "sess-r34",
				"project":    "proj-r34",
				"content":    "Original prompt content",
				"created_at": "2026-04-23T09:10:00Z",
			},
		},
		"mutations": []map[string]any{
			{
				"entity":     "prompt",
				"entity_key": "prompt-r34-1",
				"op":         "upsert",
				"payload":    mutPayload,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}

	chunks := []dashboardChunkRow{
		{
			chunkID: "chunk-r34-1", project: "proj-r34", createdBy: "tester",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed:    parseMustChunk(t, chunkJSON),
		},
	}

	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}

	detail, ok := model.projectDetails["proj-r34"]
	if !ok {
		t.Fatalf("expected project detail for proj-r34")
	}
	if len(detail.Prompts) == 0 {
		t.Fatalf("expected at least one prompt in detail")
	}
	prompt := detail.Prompts[0]

	// R3-4: SessionID must survive a mutation that omits session_id.
	if prompt.SessionID == "" {
		t.Errorf("R3-4: DashboardPromptRow.SessionID wiped by partial mutation (was: sess-r34)")
	}
	if prompt.SessionID != "sess-r34" {
		t.Errorf("R3-4: expected SessionID=%q, got %q", "sess-r34", prompt.SessionID)
	}

	// R3-4: CreatedAt must survive a mutation that omits created_at.
	if prompt.CreatedAt == "" {
		t.Errorf("R3-4: DashboardPromptRow.CreatedAt wiped by partial mutation (was: 2026-04-23T09:10:00Z)")
	}
	if prompt.CreatedAt != "2026-04-23T09:10:00Z" {
		t.Errorf("R3-4: expected CreatedAt=%q, got %q", "2026-04-23T09:10:00Z", prompt.CreatedAt)
	}

	// Content must be updated to the mutation value.
	if prompt.Content != "Updated prompt content via mutation" {
		t.Errorf("R3-4: expected Content=%q (from mutation), got %q", "Updated prompt content via mutation", prompt.Content)
	}
}

// TestStandaloneCloudMutationObservationVisibleInPaginatedList verifies the regression
// where an observation that exists only in cloud_mutations (and not cloud_chunks)
// must still appear in dashboard observation list/search.
func TestStandaloneCloudMutationObservationVisibleInPaginatedList(t *testing.T) {
	payloadBytes, err := json.Marshal(map[string]any{
		"sync_id":    "obs-ab76f3e44c24859e",
		"session_id": "sess-standalone",
		"project":    "engram",
		"type":       "decision",
		"title":      "Test de decisión arquitectónica ignorar",
		"content":    "contenido",
		"created_at": "2026-04-26T09:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal mutation payload: %v", err)
	}

	model, err := buildDashboardReadModelFromRows(nil, []dashboardMutationRow{
		{
			seq:        726,
			project:    "engram",
			entity:     "observation",
			entityKey:  "obs-ab76f3e44c24859e",
			op:         "upsert",
			payload:    payloadBytes,
			occurredAt: time.Date(2026, 4, 26, 9, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRows: %v", err)
	}

	cs := &CloudStore{dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil }}
	rows, total, err := cs.ListRecentObservationsPaginated("engram", "decisión arquitectónica", "", 20, 0)
	if err != nil {
		t.Fatalf("ListRecentObservationsPaginated: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total=1, got %d", total)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 paginated row, got %d", len(rows))
	}
	if rows[0].SyncID != "obs-ab76f3e44c24859e" {
		t.Fatalf("expected sync_id obs-ab76f3e44c24859e, got %q", rows[0].SyncID)
	}
	if rows[0].Title != "Test de decisión arquitectónica ignorar" {
		t.Fatalf("expected standalone mutation title preserved, got %q", rows[0].Title)
	}
}

// TestStandaloneCloudMutationDeleteWinsBySeq ensures standalone delete mutations
// are applied in sequence order and can remove previously upserted entities.
func TestStandaloneCloudMutationDeleteWinsBySeq(t *testing.T) {
	upsertPayload, err := json.Marshal(map[string]any{
		"sync_id":    "obs-delete-seq",
		"session_id": "sess-delete-seq",
		"project":    "engram",
		"type":       "bugfix",
		"title":      "Will be deleted",
		"created_at": "2026-04-26T08:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal upsert payload: %v", err)
	}
	deletePayload, err := json.Marshal(map[string]any{
		"sync_id":    "obs-delete-seq",
		"session_id": "sess-delete-seq",
		"deleted":    true,
	})
	if err != nil {
		t.Fatalf("marshal delete payload: %v", err)
	}

	model, err := buildDashboardReadModelFromRows(nil, []dashboardMutationRow{
		{seq: 100, project: "engram", entity: "observation", entityKey: "obs-delete-seq", op: "upsert", payload: upsertPayload, occurredAt: time.Date(2026, 4, 26, 8, 0, 0, 0, time.UTC)},
		{seq: 101, project: "engram", entity: "observation", entityKey: "obs-delete-seq", op: "delete", payload: deletePayload, occurredAt: time.Date(2026, 4, 26, 8, 1, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRows: %v", err)
	}

	cs := &CloudStore{dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil }}
	rows, total, err := cs.ListRecentObservationsPaginated("engram", "", "", 20, 0)
	if err != nil {
		t.Fatalf("ListRecentObservationsPaginated: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("expected deleted observation to be absent, total=%d len=%d", total, len(rows))
	}
}

func TestCloudMutationsDefineCurrentObservationStateWhenChunksContainHistory(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			chunkID:   "chunk-history-1",
			project:   "engram",
			createdBy: "doctor@example.com",
			createdAt: time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"observations":[
					{"sync_id":"obs-historical-1","session_id":"sess-history","project":"engram","type":"decision","title":"historical one","created_at":"2026-04-20T09:10:00Z"},
					{"sync_id":"obs-historical-2","session_id":"sess-history","project":"engram","type":"decision","title":"historical two","created_at":"2026-04-20T09:20:00Z"},
					{"sync_id":"obs-current","session_id":"sess-current","project":"engram","type":"decision","title":"stale chunk title","created_at":"2026-04-20T09:30:00Z"}
				]
			}`)),
		},
	}
	currentPayload, err := json.Marshal(map[string]any{
		"sync_id":    "obs-current",
		"session_id": "sess-current",
		"project":    "engram",
		"type":       "decision",
		"title":      "current mutation title",
		"created_at": "2026-04-21T09:30:00Z",
	})
	if err != nil {
		t.Fatalf("marshal current mutation payload: %v", err)
	}

	model, err := buildDashboardReadModelFromRows(chunks, []dashboardMutationRow{
		{seq: 200, project: "engram", entity: "observation", entityKey: "obs-current", op: "upsert", payload: currentPayload, occurredAt: time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRows: %v", err)
	}

	detail, ok := model.projectDetails["engram"]
	if !ok {
		t.Fatal("expected engram project detail")
	}
	if detail.Stats.Observations != 1 {
		t.Fatalf("expected current observation count=1, got %d (%+v)", detail.Stats.Observations, detail.Observations)
	}
	if len(detail.Observations) != 1 {
		t.Fatalf("expected exactly one current observation row, got %d", len(detail.Observations))
	}
	if detail.Observations[0].SyncID != "obs-current" {
		t.Fatalf("expected only obs-current to remain, got %q", detail.Observations[0].SyncID)
	}
	if detail.Observations[0].Title != "current mutation title" {
		t.Fatalf("expected mutation payload to win, got %q", detail.Observations[0].Title)
	}
}

func TestCloudMutationCurrentStatePruningIsProjectScoped(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			chunkID:   "chunk-project-a",
			project:   "project-a",
			createdBy: "doctor@example.com",
			createdAt: time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"observations":[
					{"sync_id":"obs-a-history","session_id":"sess-a","project":"project-a","type":"decision","title":"historical a","created_at":"2026-04-20T09:10:00Z"},
					{"sync_id":"obs-a-current","session_id":"sess-a","project":"project-a","type":"decision","title":"current a chunk","created_at":"2026-04-20T09:20:00Z"}
				]
			}`)),
		},
		{
			chunkID:   "chunk-project-b",
			project:   "project-b",
			createdBy: "doctor@example.com",
			createdAt: time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"observations":[
					{"sync_id":"obs-b-chunk-only","session_id":"sess-b","project":"project-b","type":"decision","title":"chunk only b","created_at":"2026-04-20T10:10:00Z"}
				]
			}`)),
		},
	}
	currentPayload, err := json.Marshal(map[string]any{
		"sync_id":    "obs-a-current",
		"session_id": "sess-a",
		"project":    "project-a",
		"type":       "decision",
		"title":      "current a mutation",
		"created_at": "2026-04-21T09:30:00Z",
	})
	if err != nil {
		t.Fatalf("marshal current mutation payload: %v", err)
	}

	model, err := buildDashboardReadModelFromRows(chunks, []dashboardMutationRow{
		{seq: 201, project: "project-a", entity: "observation", entityKey: "obs-a-current", op: "upsert", payload: currentPayload, occurredAt: time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRows: %v", err)
	}

	detailA := model.projectDetails["project-a"]
	if detailA.Stats.Observations != 1 || len(detailA.Observations) != 1 || detailA.Observations[0].SyncID != "obs-a-current" {
		t.Fatalf("expected project-a pruning to keep only current observation, got %+v", detailA.Observations)
	}
	detailB := model.projectDetails["project-b"]
	if detailB.Stats.Observations != 1 || len(detailB.Observations) != 1 || detailB.Observations[0].SyncID != "obs-b-chunk-only" {
		t.Fatalf("expected project-b chunk-only observation to survive project-a pruning, got %+v", detailB.Observations)
	}
}

func TestCloudMutationSessionPruningPreservesReferencedParentSession(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			chunkID:   "chunk-parent-session",
			project:   "engram",
			createdBy: "doctor@example.com",
			createdAt: time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[
					{"id":"sess-parent","project":"engram","started_at":"2026-04-20T09:00:00Z"},
					{"id":"sess-stale","project":"engram","started_at":"2026-04-20T08:00:00Z"}
				],
				"observations":[
					{"sync_id":"obs-current-parent","session_id":"sess-parent","project":"engram","type":"decision","title":"current child","created_at":"2026-04-20T09:10:00Z"}
				]
			}`)),
		},
	}
	sessionPayload, err := json.Marshal(map[string]any{
		"id":         "sess-current-other",
		"project":    "engram",
		"started_at": "2026-04-21T09:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal session mutation payload: %v", err)
	}
	obsPayload, err := json.Marshal(map[string]any{
		"sync_id":    "obs-current-parent",
		"session_id": "sess-parent",
		"project":    "engram",
		"type":       "decision",
		"title":      "current child mutation",
		"created_at": "2026-04-21T09:10:00Z",
	})
	if err != nil {
		t.Fatalf("marshal observation mutation payload: %v", err)
	}

	model, err := buildDashboardReadModelFromRows(chunks, []dashboardMutationRow{
		{seq: 210, project: "engram", entity: "session", entityKey: "sess-current-other", op: "upsert", payload: sessionPayload, occurredAt: time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)},
		{seq: 211, project: "engram", entity: "observation", entityKey: "obs-current-parent", op: "upsert", payload: obsPayload, occurredAt: time.Date(2026, 4, 21, 9, 10, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRows: %v", err)
	}

	detail := model.projectDetails["engram"]
	if detail.Stats.Observations != 1 || len(detail.Observations) != 1 {
		t.Fatalf("expected one retained current observation, got %+v", detail.Observations)
	}
	seenParent := false
	seenStale := false
	for _, session := range detail.Sessions {
		switch session.SessionID {
		case "sess-parent":
			seenParent = true
		case "sess-stale":
			seenStale = true
		}
	}
	if !seenParent {
		t.Fatalf("expected parent session referenced by retained observation to survive pruning, got %+v", detail.Sessions)
	}
	if seenStale {
		t.Fatalf("expected unreferenced stale session to be pruned, got %+v", detail.Sessions)
	}
}

func TestDashboardCurrentStateKeepsDuplicateEntityKeysAcrossProjects(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			chunkID:   "chunk-duplicate-project-a",
			project:   "project-a",
			createdBy: "doctor@example.com",
			createdAt: time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"shared-session","project":"project-a","started_at":"2026-04-20T09:00:00Z","summary":"project a session"}],
				"observations":[{"sync_id":"shared-observation","session_id":"shared-session","project":"project-a","type":"decision","title":"project a observation","created_at":"2026-04-20T09:10:00Z"}],
				"prompts":[{"sync_id":"shared-prompt","session_id":"shared-session","project":"project-a","content":"project a prompt","created_at":"2026-04-20T09:20:00Z"}]
			}`)),
		},
		{
			chunkID:   "chunk-duplicate-project-b",
			project:   "project-b",
			createdBy: "doctor@example.com",
			createdAt: time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"shared-session","project":"project-b","started_at":"2026-04-20T10:00:00Z","summary":"project b session"}],
				"observations":[{"sync_id":"shared-observation","session_id":"shared-session","project":"project-b","type":"decision","title":"project b observation","created_at":"2026-04-20T10:10:00Z"}],
				"prompts":[{"sync_id":"shared-prompt","session_id":"shared-session","project":"project-b","content":"project b prompt","created_at":"2026-04-20T10:20:00Z"}]
			}`)),
		},
	}
	sessionPayloadA, err := json.Marshal(map[string]any{"id": "shared-session", "project": "project-a", "started_at": "2026-04-20T09:00:00Z", "summary": "project a mutation session"})
	if err != nil {
		t.Fatalf("marshal project-a session payload: %v", err)
	}
	sessionPayloadB, err := json.Marshal(map[string]any{"id": "shared-session", "project": "project-b", "started_at": "2026-04-20T10:00:00Z", "summary": "project b mutation session"})
	if err != nil {
		t.Fatalf("marshal project-b session payload: %v", err)
	}
	observationPayloadA, err := json.Marshal(map[string]any{"sync_id": "shared-observation", "session_id": "shared-session", "project": "project-a", "type": "decision", "title": "project a mutation observation", "created_at": "2026-04-20T09:10:00Z"})
	if err != nil {
		t.Fatalf("marshal project-a observation payload: %v", err)
	}
	observationPayloadB, err := json.Marshal(map[string]any{"sync_id": "shared-observation", "session_id": "shared-session", "project": "project-b", "type": "decision", "title": "project b mutation observation", "created_at": "2026-04-20T10:10:00Z"})
	if err != nil {
		t.Fatalf("marshal project-b observation payload: %v", err)
	}
	promptPayloadA, err := json.Marshal(map[string]any{"sync_id": "shared-prompt", "session_id": "shared-session", "project": "project-a", "content": "project a mutation prompt", "created_at": "2026-04-20T09:20:00Z"})
	if err != nil {
		t.Fatalf("marshal project-a prompt payload: %v", err)
	}
	promptPayloadB, err := json.Marshal(map[string]any{"sync_id": "shared-prompt", "session_id": "shared-session", "project": "project-b", "content": "project b mutation prompt", "created_at": "2026-04-20T10:20:00Z"})
	if err != nil {
		t.Fatalf("marshal project-b prompt payload: %v", err)
	}

	model, err := buildDashboardReadModelFromRows(chunks, []dashboardMutationRow{
		{seq: 300, project: "project-a", entity: "session", entityKey: "shared-session", op: "upsert", payload: sessionPayloadA, occurredAt: time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)},
		{seq: 301, project: "project-b", entity: "session", entityKey: "shared-session", op: "upsert", payload: sessionPayloadB, occurredAt: time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)},
		{seq: 302, project: "project-a", entity: "observation", entityKey: "shared-observation", op: "upsert", payload: observationPayloadA, occurredAt: time.Date(2026, 4, 21, 9, 10, 0, 0, time.UTC)},
		{seq: 303, project: "project-b", entity: "observation", entityKey: "shared-observation", op: "upsert", payload: observationPayloadB, occurredAt: time.Date(2026, 4, 21, 10, 10, 0, 0, time.UTC)},
		{seq: 304, project: "project-a", entity: "prompt", entityKey: "shared-prompt", op: "upsert", payload: promptPayloadA, occurredAt: time.Date(2026, 4, 21, 9, 20, 0, 0, time.UTC)},
		{seq: 305, project: "project-b", entity: "prompt", entityKey: "shared-prompt", op: "upsert", payload: promptPayloadB, occurredAt: time.Date(2026, 4, 21, 10, 20, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRows: %v", err)
	}

	assertDuplicateProjectDetail := func(project, sessionSummary, observationTitle, promptContent string) {
		t.Helper()
		detail, ok := model.projectDetails[project]
		if !ok {
			t.Fatalf("expected project detail for %s", project)
		}
		if detail.Stats.Sessions != 1 || len(detail.Sessions) != 1 {
			t.Fatalf("expected one session for %s, stats=%+v rows=%+v", project, detail.Stats, detail.Sessions)
		}
		if detail.Sessions[0].SessionID != "shared-session" || detail.Sessions[0].Summary != sessionSummary {
			t.Fatalf("unexpected session for %s: %+v", project, detail.Sessions[0])
		}
		if detail.Stats.Observations != 1 || len(detail.Observations) != 1 {
			t.Fatalf("expected one observation for %s, stats=%+v rows=%+v", project, detail.Stats, detail.Observations)
		}
		if detail.Observations[0].SyncID != "shared-observation" || detail.Observations[0].Title != observationTitle {
			t.Fatalf("unexpected observation for %s: %+v", project, detail.Observations[0])
		}
		if detail.Stats.Prompts != 1 || len(detail.Prompts) != 1 {
			t.Fatalf("expected one prompt for %s, stats=%+v rows=%+v", project, detail.Stats, detail.Prompts)
		}
		if detail.Prompts[0].SyncID != "shared-prompt" || detail.Prompts[0].Content != promptContent {
			t.Fatalf("unexpected prompt for %s: %+v", project, detail.Prompts[0])
		}
	}

	assertDuplicateProjectDetail("project-a", "project a mutation session", "project a mutation observation", "project a mutation prompt")
	assertDuplicateProjectDetail("project-b", "project b mutation session", "project b mutation observation", "project b mutation prompt")
}

// ─── R5-4: Dedicated not-found error sentinels ───────────────────────────────

// TestSessionDetailNotFoundReturnsSessionNotFoundError asserts that
// GetSessionDetail returns ErrDashboardSessionNotFound (not ErrDashboardProjectNotFound)
// when the session ID does not exist within a valid project.
func TestSessionDetailNotFoundReturnsSessionNotFoundError(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			chunkID: "chunk-sess-nf-1", project: "proj-valid", createdBy: "user",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"sess-exists","project":"proj-valid","started_at":"2026-04-23T08:00:00Z"}]
			}`)),
		},
	}
	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}
	cs := &CloudStore{
		dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil },
	}
	_, _, _, err = cs.GetSessionDetail("proj-valid", "sess-missing")
	if err == nil {
		t.Fatal("expected an error for missing session, got nil")
	}
	if !errors.Is(err, ErrDashboardSessionNotFound) {
		t.Errorf("expected ErrDashboardSessionNotFound, got %v", err)
	}
	if errors.Is(err, ErrDashboardProjectNotFound) {
		t.Errorf("must NOT be ErrDashboardProjectNotFound for missing session in valid project")
	}
}

// TestObservationDetailNotFoundReturnsObservationNotFoundError asserts that
// GetObservationDetail returns ErrDashboardObservationNotFound (not ErrDashboardProjectNotFound)
// when the observation does not exist within a valid project.
func TestObservationDetailNotFoundReturnsObservationNotFoundError(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			chunkID: "chunk-obs-nf-1", project: "proj-valid", createdBy: "user",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"sess-exists","project":"proj-valid","started_at":"2026-04-23T08:00:00Z"}],
				"observations":[{"sync_id":"obs-exists","session_id":"sess-exists","project":"proj-valid","type":"note","title":"Exists","created_at":"2026-04-23T08:10:00Z"}]
			}`)),
		},
	}
	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}
	cs := &CloudStore{
		dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil },
	}
	_, _, _, err = cs.GetObservationDetail("proj-valid", "sess-exists", "obs-missing")
	if err == nil {
		t.Fatal("expected an error for missing observation, got nil")
	}
	if !errors.Is(err, ErrDashboardObservationNotFound) {
		t.Errorf("expected ErrDashboardObservationNotFound, got %v", err)
	}
	if errors.Is(err, ErrDashboardProjectNotFound) {
		t.Errorf("must NOT be ErrDashboardProjectNotFound for missing observation in valid project")
	}
}

// TestPromptDetailNotFoundReturnsPromptNotFoundError asserts that
// GetPromptDetail returns ErrDashboardPromptNotFound (not ErrDashboardProjectNotFound)
// when the prompt does not exist within a valid project.
func TestPromptDetailNotFoundReturnsPromptNotFoundError(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			chunkID: "chunk-prompt-nf-1", project: "proj-valid", createdBy: "user",
			createdAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
			parsed: parseMustChunk(t, []byte(`{
				"sessions":[{"id":"sess-exists","project":"proj-valid","started_at":"2026-04-23T08:00:00Z"}],
				"prompts":[{"sync_id":"prompt-exists","session_id":"sess-exists","project":"proj-valid","content":"Exists","created_at":"2026-04-23T08:20:00Z"}]
			}`)),
		},
	}
	model, err := buildDashboardReadModel(chunks)
	if err != nil {
		t.Fatalf("buildDashboardReadModel: %v", err)
	}
	cs := &CloudStore{
		dashboardReadModelLoad: func() (dashboardReadModel, error) { return model, nil },
	}
	_, _, _, err = cs.GetPromptDetail("proj-valid", "sess-exists", "prompt-missing")
	if err == nil {
		t.Fatal("expected an error for missing prompt, got nil")
	}
	if !errors.Is(err, ErrDashboardPromptNotFound) {
		t.Errorf("expected ErrDashboardPromptNotFound, got %v", err)
	}
	if errors.Is(err, ErrDashboardProjectNotFound) {
		t.Errorf("must NOT be ErrDashboardProjectNotFound for missing prompt in valid project")
	}
}
