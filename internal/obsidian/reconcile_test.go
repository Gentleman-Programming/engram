package obsidian

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestFailedNotesDoNotGenerateHubReferences(t *testing.T) {
	for _, successful := range []int{0, 1, 2} {
		t.Run(string(rune('0'+successful))+" successful notes", func(t *testing.T) {
			dir, root := newReconcileVault(t)
			observations := []store.Observation{
				makeObs(1, "s", "demo", "manual", "failed", "topic/failed", "2026-01-01T10:00:00Z"),
				makeObs(2, "s", "demo", "manual", "good", "topic/good", "2026-01-01T10:00:00Z"),
				makeObs(3, "s", "demo", "manual", "also good", "topic/also", "2026-01-01T10:00:00Z"),
			}[:successful+1]
			if err := os.MkdirAll(filepath.Join(root, "demo", "manual", "failed-1.md"), 0755); err != nil {
				t.Fatal(err)
			}
			ms := &mockStore{exportData: &store.ExportData{Observations: observations}}
			r, err := NewExporter(ms, ExportConfig{VaultPath: dir}).Export()
			if err != nil {
				t.Fatal(err)
			}
			wantHubs := successful // one session, plus a topic only for two successful notes
			if len(r.Errors) != 1 || !strings.Contains(r.Errors[0].Error(), "write") || r.Created != successful || r.Updated != 0 || r.Deleted != 0 || r.Skipped != 0 || r.HubsCreated != wantHubs {
				t.Errorf("failed note must not generate hubs: %+v", r)
			}
			state := readStateFiles(t, root)
			if len(state.Files) != successful || state.Files[1] != "" || len(state.SessionHubs)+len(state.TopicHubs) != wantHubs {
				t.Errorf("failed export tracked: %+v", state)
			}
			for _, hub := range []string{filepath.Join("_sessions", "s.md"), filepath.Join("_topics", "topic.md")} {
				content, readErr := os.ReadFile(filepath.Join(root, hub))
				wantExists := successful > 0 && (strings.HasPrefix(hub, "_sessions") || successful == 2)
				if !wantExists {
					if !os.IsNotExist(readErr) {
						t.Errorf("unexpected hub %s: %v", hub, readErr)
					}
					continue
				}
				if readErr != nil || strings.Contains(string(content), "failed-1") || !strings.Contains(string(content), "good-2") {
					t.Errorf("hub contains failed reference or lacks successful one: %s: %q %v", hub, content, readErr)
				}
			}
		})
	}
}

func TestDesiredHubsSurviveNoteRewriteFailure(t *testing.T) {
	dir, root := newReconcileVault(t)
	observations := []store.Observation{
		makeObs(1, "s", "demo", "manual", "one", "topic/one", "2026-01-01T10:00:00Z"),
		makeObs(2, "s", "demo", "manual", "two", "topic/two", "2026-01-01T10:00:00Z"),
	}
	ms := &mockStore{exportData: &store.ExportData{Observations: observations}}
	cfg := ExportConfig{VaultPath: dir}
	if _, err := NewExporter(ms, cfg).Export(); err != nil {
		t.Fatal(err)
	}
	before := readStateFiles(t, root)
	for i := range ms.exportData.Observations {
		ms.exportData.Observations[i].Type = "blocked"
	}
	if err := os.WriteFile(filepath.Join(root, "demo", "blocked"), []byte("block directory creation"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := NewExporter(ms, cfg).Export()
	if err != nil {
		t.Fatal(err)
	}
	after := readStateFiles(t, root)
	if len(r.Errors) != 2 || r.Deleted != 0 || r.HubsCreated != 0 || len(after.SessionHubs) != 1 || len(after.TopicHubs) != 1 {
		t.Fatalf("live hubs lost or rewritten after note failure: %+v state=%+v", r, after)
	}
	for _, hubs := range []map[string]string{before.SessionHubs, before.TopicHubs} {
		for _, path := range hubs {
			if !fileExists(filepath.Join(root, path)) {
				t.Errorf("live hub retired: %s", path)
			}
		}
	}
	for id, path := range before.Files {
		if after.Files[id] != path || !fileExists(filepath.Join(root, path)) {
			t.Errorf("previous note lost: %d", id)
		}
	}
}

func TestCaseOnlyRenameOwnership(t *testing.T) {
	for _, field := range []string{"type", "project"} {
		t.Run(field, func(t *testing.T) {
			dir, root := newReconcileVault(t)
			obs := makeObs(1, "", "demo", "manual", "live", "", "2026-01-01T10:00:00Z")
			ms := &mockStore{exportData: &store.ExportData{Observations: []store.Observation{obs}}}
			cfg := ExportConfig{VaultPath: dir}
			if _, err := NewExporter(ms, cfg).Export(); err != nil {
				t.Fatal(err)
			}
			old := readStateFiles(t, root).Files[1]
			target := filepath.Join("demo", "MANUAL", "live-1.md")
			if field == "project" {
				target = filepath.Join("DEMO", "manual", "live-1.md")
				project := "DEMO"
				ms.exportData.Observations[0].Project = &project
			} else {
				ms.exportData.Observations[0].Type = "MANUAL"
			}
			if _, err := os.Stat(filepath.Join(root, target)); os.IsNotExist(err) {
				t.Skip("temp filesystem is case-sensitive; case-only aliases are distinct")
			} else if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				r, err := NewExporter(ms, cfg).Export()
				if err != nil {
					t.Fatal(err)
				}
				content, readErr := os.ReadFile(filepath.Join(root, target))
				state := readStateFiles(t, root)
				if len(r.Errors) != 0 || r.Deleted != 0 || readErr != nil || string(content) != ObservationToMarkdown(ms.exportData.Observations[0]) || state.Files[1] != target || len(state.PendingRetirements[1]) != 1 || state.PendingRetirements[1][0] != old {
					t.Fatalf("live case alias retired: result=%+v read=%v state=%+v", r, readErr, state)
				}
			}
		})
	}
}

func TestPhysicalPathOwnership(t *testing.T) {
	dir, root := newReconcileVault(t)
	obs := makeObs(1, "", "demo", "manual", "live", "", "2026-01-01T10:00:00Z")
	ms := &mockStore{exportData: &store.ExportData{Observations: []store.Observation{obs}}}
	cfg := ExportConfig{VaultPath: dir}
	if _, err := NewExporter(ms, cfg).Export(); err != nil {
		t.Fatal(err)
	}
	state := readStateFiles(t, root)
	if err := os.Link(filepath.Join(root, state.Files[1]), filepath.Join(root, "alias.md")); err != nil {
		t.Fatal(err)
	}
	state.PendingRetirements[2] = []string{"alias.md"}
	if err := WriteState(filepath.Join(root, ".engram-sync-state.json"), state); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		r, err := NewExporter(ms, cfg).Export()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Errors) != 0 || r.Deleted != 0 || !fileExists(filepath.Join(root, "alias.md")) || len(readStateFiles(t, root).PendingRetirements[2]) != 1 {
			t.Fatalf("physical alias owned by another note retired: %+v", r)
		}
	}
}

func TestScopedRenameRetriesRetirement(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "retry"}[blocked], func(t *testing.T) {
			dir, root := newReconcileVault(t)
			obs := makeObs(1, "s", "demo", "manual", "before", "", "2026-01-01T10:00:00Z")
			ms := &mockStore{exportData: &store.ExportData{Observations: []store.Observation{obs}}}
			cfg := ExportConfig{VaultPath: dir, Project: "demo"}
			if _, err := NewExporter(ms, cfg).Export(); err != nil {
				t.Fatal(err)
			}
			old := readStateFiles(t, root).Files[1]
			if blocked {
				// A nonempty directory deterministically refuses file retirement on all platforms.
				if err := os.Rename(filepath.Join(root, old), filepath.Join(root, "saved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, old), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, old, "child"), []byte("block"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			ms.exportData.Observations[0].Title = "after"
			r, err := NewExporter(ms, cfg).Export()
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join("demo", "manual", "after-1.md")
			if !fileExists(filepath.Join(root, target)) {
				t.Fatal("new note missing")
			}
			if blocked {
				state := readStateFiles(t, root)
				if len(r.Errors) != 1 || state.Files[1] != target || len(state.PendingRetirements[1]) != 1 || state.PendingRetirements[1][0] != old {
					t.Fatalf("failed retirement must remain tracked: %+v state=%+v", r, readStateFiles(t, root))
				}
				if err := os.Rename(filepath.Join(root, old), filepath.Join(root, "blocker")); err != nil {
					t.Fatal(err)
				}
				r, err = NewExporter(ms, cfg).Export()
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(r.Errors) != 0 || r.Deleted != 1 {
				t.Fatalf("retirement: %+v", r)
			}
			if fileExists(filepath.Join(root, old)) || filepath.Clean(readStateFiles(t, root).Files[1]) != target {
				t.Fatal("rename not reconciled")
			}
		})
	}
}

func TestRepeatedRenameRetirement(t *testing.T) {
	for _, deletion := range []string{"live", "soft", "hard"} {
		t.Run(deletion, func(t *testing.T) {
			dir, root := newReconcileVault(t)
			obs := makeObs(1, "", "demo", "manual", "a", "", "2026-01-01T10:00:00Z")
			ms := &mockStore{exportData: &store.ExportData{Observations: []store.Observation{obs}}}
			cfg := ExportConfig{VaultPath: dir}
			run := func() *ExportResult {
				t.Helper()
				r, err := NewExporter(ms, cfg).Export()
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			run()
			a := readStateFiles(t, root).Files[1]
			if err := os.Rename(filepath.Join(root, a), filepath.Join(root, "saved")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, a), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, a, "child"), []byte("block"), 0644); err != nil {
				t.Fatal(err)
			}
			for _, title := range []string{"b", "c"} {
				ms.exportData.Observations[0].Title = title
				if r := run(); len(r.Errors) != 1 {
					t.Fatalf("blocked retirement: %+v", r)
				}
			}
			b := filepath.Join("demo", "manual", "b-1.md")
			if fileExists(filepath.Join(root, b)) {
				t.Fatal("intermediate rename permanently orphaned")
			}
			if deletion == "soft" {
				stamp := "2026-01-02T10:00:00Z"
				ms.exportData.Observations[0].DeletedAt = &stamp
			}
			if deletion == "hard" {
				ms.exportData.Observations = nil
			}
			if deletion != "live" {
				run()
			}
			if err := os.Rename(filepath.Join(root, a), filepath.Join(root, "blocker")); err != nil {
				t.Fatal(err)
			}
			if r := run(); len(r.Errors) != 0 {
				t.Fatalf("recovery: %+v", r)
			}
			state := readStateFiles(t, root)
			if fileExists(filepath.Join(root, a)) || fileExists(filepath.Join(root, b)) {
				t.Fatal("retired note remains")
			}
			c := filepath.Join("demo", "manual", "c-1.md")
			if deletion == "live" {
				if state.Files[1] != c {
					t.Fatalf("latest path: %+v", state)
				}
			} else if state.Files[1] != "" || fileExists(filepath.Join(root, c)) {
				t.Fatal("deleted latest note remains")
			}
		})
	}
}

func TestPendingScopeAndContainment(t *testing.T) {
	dir, root := newReconcileVault(t)
	obs := makeObs(1, "", "demo", "manual", "live", "", "2026-01-01T10:00:00Z")
	ms := &mockStore{exportData: &store.ExportData{Observations: []store.Observation{obs}}}
	outside := filepath.Join(dir, "outside.md")
	if err := os.WriteFile(outside, []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}
	state := SyncState{PendingRetirements: map[int64][]string{1: {"../outside.md"}, 2: {"foreign.md"}}}
	if err := os.WriteFile(filepath.Join(root, "foreign.md"), []byte("foreign"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteState(filepath.Join(root, ".engram-sync-state.json"), state); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		r, err := NewExporter(ms, ExportConfig{VaultPath: dir, Project: "demo"}).Export()
		if err != nil {
			t.Fatal(err)
		}
		state = readStateFiles(t, root)
		if len(r.Errors) != 1 || r.Deleted != 0 || len(state.PendingRetirements[1]) != 1 || len(state.PendingRetirements[2]) != 1 || !fileExists(filepath.Join(root, "foreign.md")) {
			t.Fatalf("pending safety: %+v state=%+v", r, state)
		}
		if content, err := os.ReadFile(outside); err != nil || string(content) != "safe" {
			t.Fatalf("outside changed: %q %v", content, err)
		}
	}
}

func TestDesiredTopicHubSurvivesWriteFailure(t *testing.T) {
	dir, root := newReconcileVault(t)
	one := makeObs(1, "", "demo", "manual", "one", "topic/one", "2026-01-01T10:00:00Z")
	two := makeObs(2, "", "demo", "manual", "two", "topic/two", "2026-01-01T10:00:00Z")
	ms := &mockStore{exportData: &store.ExportData{Observations: []store.Observation{one, two}}}
	cfg := ExportConfig{VaultPath: dir}
	if _, err := NewExporter(ms, cfg).Export(); err != nil {
		t.Fatal(err)
	}
	state := readStateFiles(t, root)
	if len(state.TopicHubs) != 1 {
		t.Fatalf("topic fixture: %+v", state)
	}
	for prefix, hub := range state.TopicHubs {
		if err := os.Rename(filepath.Join(root, hub), filepath.Join(root, "saved-topic")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, hub), 0755); err != nil {
			t.Fatal(err)
		}
		r, err := NewExporter(ms, cfg).Export()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Errors) != 1 || r.Deleted != 0 || readStateFiles(t, root).TopicHubs[prefix] != hub {
			t.Fatalf("live topic pruned: %+v", r)
		}
		if err := os.Rename(filepath.Join(root, hub), filepath.Join(root, "topic-blocker")); err != nil {
			t.Fatal(err)
		}
		r, err = NewExporter(ms, cfg).Export()
		if err != nil || len(r.Errors) != 0 || r.HubsCreated != 1 {
			t.Fatalf("topic recovery: %+v %v", r, err)
		}
	}
}

func TestEquivalentPathOwnership(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-note", true: "other-owner"}[shared], func(t *testing.T) {
			dir, root := newReconcileVault(t)
			obs := makeObs(1, "s", "demo", "manual", "before", "", "2026-01-01T10:00:00Z")
			ms := &mockStore{exportData: &store.ExportData{Observations: []store.Observation{obs}}}
			cfg := ExportConfig{VaultPath: dir, Project: "demo"}
			if _, err := NewExporter(ms, cfg).Export(); err != nil {
				t.Fatal(err)
			}
			state := readStateFiles(t, root)
			old := state.Files[1]
			state.Files[1] = filepath.ToSlash(old)
			if shared {
				state.Files[2] = filepath.Join("demo", "manual", "before-1.md")
				ms.exportData.Observations[0].Title = "after"
			}
			state.LastExportAt = ""
			if err := WriteState(filepath.Join(root, ".engram-sync-state.json"), state); err != nil {
				t.Fatal(err)
			}
			r, err := NewExporter(ms, cfg).Export()
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Errors) != 0 || r.Deleted != 0 || !fileExists(filepath.Join(root, old)) {
				t.Fatalf("live owned path retired: %+v", r)
			}
			state = readStateFiles(t, root)
			if shared && state.Files[2] == "" {
				t.Fatal("other owner lost")
			}
			if !fileExists(filepath.Join(root, state.Files[1])) {
				t.Fatal("current note missing")
			}
		})
	}
}

func TestScopedForeignSoftDeletePreserved(t *testing.T) {
	dir, root := newReconcileVault(t)
	own := makeObs(1, "s", "demo", "manual", "own", "", "2026-01-01T10:00:00Z")
	foreign := makeObs(2, "other-s", "other", "manual", "foreign", "", "2026-01-01T10:00:00Z")
	ms := &mockStore{exportData: &store.ExportData{Observations: []store.Observation{own, foreign}}}
	if _, err := NewExporter(ms, ExportConfig{VaultPath: dir}).Export(); err != nil {
		t.Fatal(err)
	}
	old := readStateFiles(t, root).Files[2]
	before, err := os.ReadFile(filepath.Join(root, old))
	if err != nil {
		t.Fatal(err)
	}
	deleted := "2026-01-02T10:00:00Z"
	ms.exportData.Observations[1].DeletedAt = &deleted
	r, err := NewExporter(ms, ExportConfig{VaultPath: dir, Project: "demo"}).Export()
	if err != nil {
		t.Fatal(err)
	}
	after, readErr := os.ReadFile(filepath.Join(root, old))
	if len(r.Errors) != 0 || r.Deleted != 0 || readErr != nil || string(before) != string(after) || readStateFiles(t, root).Files[2] != old {
		t.Fatalf("foreign note changed: %+v read=%v", r, readErr)
	}
	r, err = NewExporter(ms, ExportConfig{VaultPath: dir}).Export()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Errors) != 0 || r.Deleted != 2 || fileExists(filepath.Join(root, old)) || readStateFiles(t, root).Files[2] != "" {
		t.Fatalf("full cleanup: %+v", r)
	}
}

func TestDesiredLiveHubSurvivesWriteFailure(t *testing.T) {
	dir, root := newReconcileVault(t)
	obs := makeObs(1, "s", "demo", "manual", "live", "", "2026-01-01T10:00:00Z")
	ms := &mockStore{exportData: &store.ExportData{Observations: []store.Observation{obs}}}
	cfg := ExportConfig{VaultPath: dir}
	if _, err := NewExporter(ms, cfg).Export(); err != nil {
		t.Fatal(err)
	}
	hub := readStateFiles(t, root).SessionHubs["s"]
	if err := os.Rename(filepath.Join(root, hub), filepath.Join(root, "saved-hub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, hub), 0755); err != nil {
		t.Fatal(err)
	}
	r, err := NewExporter(ms, cfg).Export()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Errors) != 1 || r.Deleted != 0 || readStateFiles(t, root).SessionHubs["s"] != hub {
		t.Fatalf("live hub pruned after write failure: %+v", r)
	}
	if info, err := os.Stat(filepath.Join(root, hub)); err != nil || !info.IsDir() {
		t.Fatalf("hub blocker removed: %v", err)
	}
	if err := os.Rename(filepath.Join(root, hub), filepath.Join(root, "hub-blocker")); err != nil {
		t.Fatal(err)
	}
	r, err = NewExporter(ms, cfg).Export()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, hub))
	if len(r.Errors) != 0 || r.Deleted != 0 || r.HubsCreated != 1 || err != nil || len(content) == 0 {
		t.Fatalf("hub recovery: %+v read=%v", r, err)
	}
}
