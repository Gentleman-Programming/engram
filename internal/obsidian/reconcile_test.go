package obsidian

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

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
