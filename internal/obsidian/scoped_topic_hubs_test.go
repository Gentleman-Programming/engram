package obsidian

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Shared topic paths are global even when Export selects only one project.
func TestScopedTopicHubsPreserveExisting(t *testing.T) {
	for _, alphaCount := range []int{2, 1} {
		name := "shared prefix"
		if alphaCount == 1 {
			name = "singleton scoped prefix"
		}
		t.Run(name, func(t *testing.T) {
			dir, root := newReconcileVault(t)
			observations := scopedTopicObservations()
			if alphaCount == 1 {
				observations = append(observations[:1], observations[2:]...)
			}
			ms := &mockStore{exportData: &store.ExportData{Observations: observations}}
			full := ExportConfig{VaultPath: dir}
			assertScopedTopicExport(t, ms, full, len(observations), 0, 1)

			hubPath := filepath.Join(root, "_topics", "shared.md")
			originalHub := readScopedTopicFile(t, hubPath)
			notes := make(map[string][]byte)
			for _, obs := range observations {
				path := filepath.Join(root, *obs.Project, "manual", Slugify(obs.Title, obs.ID)+".md")
				notes[path] = readScopedTopicFile(t, path)
				link := "[[" + Slugify(obs.Title, obs.ID)
				if !strings.Contains(string(originalHub), link) {
					t.Fatalf("initial full hub lacks observation %d: %s", obs.ID, originalHub)
				}
			}
			if got := strings.Count(string(originalHub), "[["); got != len(observations) {
				t.Fatalf("initial full hub links = %d, want %d", got, len(observations))
			}

			assertScopedTopicExport(t, ms, ExportConfig{VaultPath: dir, Project: "alpha"}, 0, alphaCount, 0)
			if got := readScopedTopicFile(t, hubPath); !bytes.Equal(got, originalHub) {
				t.Errorf("scoped export changed existing shared hub bytes:\nbefore:\n%s\nafter:\n%s", originalHub, got)
			}
			assertScopedTopicNotes(t, notes)

			// A full export must still regenerate hubs, not preserve arbitrary bytes.
			if err := os.WriteFile(hubPath, []byte("stale generated hub\n"), 0644); err != nil {
				t.Fatal(err)
			}
			assertScopedTopicExport(t, ms, full, 0, len(observations), 1)
			if got := readScopedTopicFile(t, hubPath); !bytes.Equal(got, originalHub) {
				t.Errorf("full export did not restore the complete topic hub:\n%s", got)
			}
			assertScopedTopicNotes(t, notes)
		})
	}
}

func TestScopedTopicHubsCreateMissing(t *testing.T) {
	for _, disjoint := range []bool{false, true} {
		name := "fresh scoped export"
		if disjoint {
			name = "disjoint existing hub"
		}
		t.Run(name, func(t *testing.T) {
			dir, root := newReconcileVault(t)
			observations := scopedTopicObservations()
			for i := 2; i < len(observations); i++ {
				topic := "other/beta"
				observations[i].TopicKey = &topic
			}
			ms := &mockStore{exportData: &store.ExportData{Observations: observations}}
			var otherHub []byte
			otherPath := filepath.Join(root, "_topics", "other.md")
			if disjoint {
				assertScopedTopicExport(t, ms, ExportConfig{VaultPath: dir, Project: "beta"}, 2, 0, 1)
				otherHub = readScopedTopicFile(t, otherPath)
			}
			assertScopedTopicExport(t, ms, ExportConfig{VaultPath: dir, Project: "alpha"}, 2, 0, 1)
			hubPath := filepath.Join(root, "_topics", "shared.md")
			hub := readScopedTopicFile(t, hubPath)
			for _, obs := range observations[:2] {
				if !strings.Contains(string(hub), "[["+Slugify(obs.Title, obs.ID)) {
					t.Errorf("new scoped hub lacks alpha observation %d: %s", obs.ID, hub)
				}
			}
			if got := strings.Count(string(hub), "[["); got != 2 {
				t.Errorf("new scoped hub links = %d, want 2", got)
			}
			// Repeating a scoped export must now preserve the newly created hub.
			assertScopedTopicExport(t, ms, ExportConfig{VaultPath: dir, Project: "alpha"}, 0, 2, 0)
			if got := readScopedTopicFile(t, hubPath); !bytes.Equal(got, hub) {
				t.Error("repeated scoped export changed newly created hub")
			}
			if disjoint && !bytes.Equal(readScopedTopicFile(t, otherPath), otherHub) {
				t.Error("alpha export changed disjoint beta hub")
			}
		})
	}
}

// The rival writes a real complete hub immediately before the scoped open.
// Bytes and public export results, rather than requested flags, are the oracle.
func TestScopedTopicHubsCompetingCreate(t *testing.T) {
	dir, root := newReconcileVault(t)
	observations := scopedTopicObservations()
	ms := &mockStore{exportData: &store.ExportData{Observations: observations}}
	cfg := ExportConfig{VaultPath: dir, Project: "alpha"}
	hubPath := filepath.Join(root, "_topics", "shared.md")
	rival := []byte("# Complete rival hub\n\n[[alpha-one-1]]\n[[alpha-two-2]]\n[[beta-one-3]]\n[[beta-two-4]]\n")
	exporter := NewExporter(ms, cfg)
	calls := 0
	exporter.openTopicHub = func(path string, flags int, perm os.FileMode) (*os.File, error) {
		calls++
		if path != hubPath {
			t.Fatalf("unexpected scoped hub path: %s", path)
		}
		if err := os.WriteFile(path, rival, 0644); err != nil {
			t.Fatal(err)
		}
		return os.OpenFile(path, flags, perm)
	}
	result, err := exporter.Export()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("scoped open calls = %d, want 1", calls)
	}
	if got := readScopedTopicFile(t, hubPath); !bytes.Equal(got, rival) {
		t.Errorf("competing complete hub was clobbered:\n%s", got)
	}
	if result.Created != 2 || result.Skipped != 0 || result.Updated != 0 || result.Deleted != 0 || result.HubsCreated != 0 || len(result.Errors) != 0 {
		t.Errorf("competing create result = %+v; want two notes, zero hubs/errors", result)
	}
	notes := make(map[string][]byte)
	for _, obs := range observations[:2] {
		path := filepath.Join(root, "alpha", "manual", Slugify(obs.Title, obs.ID)+".md")
		notes[path] = readScopedTopicFile(t, path)
	}
	assertScopedTopicNotes(t, notes)

	// A later full export with only one live observation retires a tracked hub.
	// This observes persisted tracking through Export, not private state reads.
	ms.exportData = &store.ExportData{Observations: observations[:1]}
	retired, err := NewExporter(ms, ExportConfig{VaultPath: dir}).Export()
	if err != nil || len(retired.Errors) != 0 {
		t.Fatalf("retirement Export = %+v, %v", retired, err)
	}
	if _, err := os.Stat(hubPath); !os.IsNotExist(err) {
		t.Errorf("tracked rival hub was not retired: %v", err)
	}
	remainingPath := filepath.Join(root, "alpha", "manual", Slugify(observations[0].Title, observations[0].ID)+".md")
	assertScopedTopicNotes(t, map[string][]byte{remainingPath: notes[remainingPath]})
}

func TestScopedTopicHubsWriteFailure(t *testing.T) {
	dir, root := newReconcileVault(t)
	observations := scopedTopicObservations()
	ms := &mockStore{exportData: &store.ExportData{Observations: observations}}
	exporter := NewExporter(ms, ExportConfig{VaultPath: dir, Project: "alpha"})
	calls := 0
	exporter.openTopicHub = func(string, int, os.FileMode) (*os.File, error) {
		calls++
		// A closed scratch handle fails Write without creating a topic hub.
		file, err := os.CreateTemp(dir, "closed-writer-")
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		return file, nil
	}
	result, err := exporter.Export()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Created != 2 || result.Updated != 0 || result.Deleted != 0 || result.Skipped != 0 || result.HubsCreated != 0 || len(result.Errors) != 1 {
		t.Fatalf("failed scoped write result = %+v, calls=%d", result, calls)
	}
	if !errors.Is(result.Errors[0], os.ErrClosed) {
		t.Errorf("reported error = %v, want closed-file error", result.Errors[0])
	}
	if _, err := os.Stat(filepath.Join(root, "_topics", "shared.md")); !os.IsNotExist(err) {
		t.Errorf("failed write left a topic hub: %v", err)
	}
	if !reflect.DeepEqual(observations, scopedTopicObservations()) {
		t.Error("failed hub write mutated observation data")
	}
}

func TestScopedTopicHubsOpenFailure(t *testing.T) {
	dir, root := newReconcileVault(t)
	observations := scopedTopicObservations()
	before := scopedTopicObservations()
	ms := &mockStore{exportData: &store.ExportData{Observations: observations}}
	cfg := ExportConfig{VaultPath: dir, Project: "alpha"}
	exporter := NewExporter(ms, cfg)
	calls := 0
	exporter.openTopicHub = func(path string, flags int, perm os.FileMode) (*os.File, error) {
		calls++
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
	}
	result, err := exporter.Export()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Created != 2 || result.Updated != 0 || result.Deleted != 0 || result.HubsCreated != 0 || len(result.Errors) != 1 {
		t.Fatalf("failed scoped open result = %+v, calls=%d", result, calls)
	}
	if !errors.Is(result.Errors[0], os.ErrPermission) {
		t.Errorf("reported error = %v, want permission error", result.Errors[0])
	}
	if _, err := os.Stat(filepath.Join(root, "_topics", "shared.md")); !os.IsNotExist(err) {
		t.Errorf("failed creation left a hub: %v", err)
	}
	if !reflect.DeepEqual(observations, before) {
		t.Error("failed hub creation mutated observation data")
	}
	notes := make(map[string][]byte)
	for _, obs := range observations[:2] {
		path := filepath.Join(root, "alpha", "manual", Slugify(obs.Title, obs.ID)+".md")
		notes[path] = readScopedTopicFile(t, path)
	}
	assertScopedTopicExport(t, ms, cfg, 0, 2, 1)
	assertScopedTopicNotes(t, notes)
}

func TestScopedTopicHubsFullExportBypassesScopedOpen(t *testing.T) {
	dir, _ := newReconcileVault(t)
	ms := &mockStore{exportData: &store.ExportData{Observations: scopedTopicObservations()}}
	exporter := NewExporter(ms, ExportConfig{VaultPath: dir})
	exporter.openTopicHub = func(string, int, os.FileMode) (*os.File, error) {
		t.Fatal("full export used scoped open seam")
		return nil, os.ErrPermission
	}
	result, err := exporter.Export()
	if err != nil || result.HubsCreated != 1 || len(result.Errors) != 0 {
		t.Fatalf("full Export = %+v, %v", result, err)
	}
}

func scopedTopicObservations() []store.Observation {
	return []store.Observation{
		makeObs(1, "", "alpha", "manual", "alpha one", "shared/one", "2026-01-01T10:00:00Z"),
		makeObs(2, "", "alpha", "manual", "alpha two", "shared/two", "2026-01-01T10:00:00Z"),
		makeObs(3, "", "beta", "manual", "beta one", "shared/three", "2026-01-01T10:00:00Z"),
		makeObs(4, "", "beta", "manual", "beta two", "shared/four", "2026-01-01T10:00:00Z"),
	}
}

func assertScopedTopicExport(t *testing.T, ms *mockStore, cfg ExportConfig, created, skipped, hubs int) {
	t.Helper()
	result, err := NewExporter(ms, cfg).Export()
	if err != nil {
		t.Fatalf("Export(%q): %v", cfg.Project, err)
	}
	if result.Created != created || result.Updated != 0 || result.Deleted != 0 || result.Skipped != skipped || result.HubsCreated != hubs || len(result.Errors) != 0 {
		t.Errorf("Export(%q) = %+v; want created=%d updated=0 deleted=0 skipped=%d hubs=%d errors=0", cfg.Project, result, created, skipped, hubs)
	}
}

func readScopedTopicFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func assertScopedTopicNotes(t *testing.T, notes map[string][]byte) {
	t.Helper()
	for path, want := range notes {
		if got := readScopedTopicFile(t, path); !bytes.Equal(got, want) {
			t.Errorf("export changed original observation note %s", path)
		}
	}
}
