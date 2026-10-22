package obsidian

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/identityfile"
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

// The rival writes a real complete hub immediately before atomic publication.
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
	exporter.publishTopicHub = func(temporary, path string) error {
		calls++
		assertScopedTopicAbsent(t, path)
		if got := readScopedTopicFile(t, temporary); len(got) == 0 {
			t.Fatal("publishing empty temporary")
		}
		if path != hubPath {
			t.Fatalf("unexpected scoped hub path: %s", path)
		}
		if err := os.WriteFile(path, rival, 0644); err != nil {
			t.Fatal(err)
		}
		return identityfile.Publish(temporary, path)
	}
	result, err := exporter.Export()
	if err != nil {
		t.Fatal(err)
	}
	assertScopedTopicTempsClean(t, root)
	if calls != 1 {
		t.Fatalf("scoped publication calls = %d, want 1", calls)
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
	var scratchPath string
	calls := 0
	exporter.createTopicTemp = func(string, string) (*os.File, error) {
		calls++
		// A closed scratch handle fails Write without creating a topic hub.
		file, err := os.CreateTemp(dir, "closed-writer-")
		if err != nil {
			t.Fatal(err)
		}
		scratchPath = file.Name()
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
	assertScopedTopicAbsent(t, scratchPath)
	assertScopedTopicTempsClean(t, root)
	if _, err := os.Stat(filepath.Join(root, "_topics", "shared.md")); !os.IsNotExist(err) {
		t.Errorf("failed write left a topic hub: %v", err)
	}
	if !reflect.DeepEqual(observations, scopedTopicObservations()) {
		t.Error("failed hub write mutated observation data")
	}
}

// Fail the actual attempt-owned writer, not an unrelated scratch handle.
// A failed attempt must not publish an empty hub that blocks retry.
func TestScopedTopicHubsWriteFailureRecovery(t *testing.T) {
	for _, competingWinner := range []bool{false, true} {
		name := "missing hub recovers"
		if competingWinner {
			name = "complete competing winner survives failure"
		}
		t.Run(name, func(t *testing.T) {
			dir, root := newReconcileVault(t)
			observations := scopedTopicObservations()
			ms := &mockStore{exportData: &store.ExportData{Observations: observations}}
			cfg := ExportConfig{VaultPath: dir, Project: "alpha"}
			hubPath := filepath.Join(root, "_topics", "shared.md")
			rival := []byte("# Complete rival hub\n\n[[alpha-one-1]]\n[[alpha-two-2]]\n[[beta-one-3]]\n[[beta-two-4]]\n")
			exporter := NewExporter(ms, cfg)
			calls := 0
			exporter.createTopicTemp = func(directory, pattern string) (*os.File, error) {
				calls++
				assertScopedTopicAbsent(t, hubPath)
				file, err := os.CreateTemp(directory, pattern)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				if competingWinner {
					// Model another writer completing the destination before this
					// attempt reports its write failure. Cleanup must leave it alone.
					winner := filepath.Join(dir, "winner.md")
					if err := os.WriteFile(winner, rival, 0644); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(winner, hubPath); err != nil {
						t.Fatal(err)
					}
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
				t.Errorf("reported error = %v, want wrapped closed-file error", result.Errors[0])
			}
			notes := make(map[string][]byte)
			for _, obs := range observations[:2] {
				path := filepath.Join(root, "alpha", "manual", Slugify(obs.Title, obs.ID)+".md")
				notes[path] = readScopedTopicFile(t, path)
			}
			assertScopedTopicTempsClean(t, root)
			wantHubs := 1
			if competingWinner {
				wantHubs = 0
				if got := readScopedTopicFile(t, hubPath); !bytes.Equal(got, rival) {
					t.Errorf("failed attempt changed competing complete hub: %s", got)
				}
			} else if info, err := os.Stat(hubPath); !os.IsNotExist(err) {
				if err != nil {
					t.Errorf("inspect failed hub: %v", err)
				} else {
					t.Errorf("failed write left final hub (%d bytes); want absent", info.Size())
				}
			}
			// Do not stop at the absence assertion: expose silent EEXIST retry
			// preservation as well as the original zero-byte final artifact.
			assertScopedTopicExport(t, ms, cfg, 0, 2, wantHubs)
			hub := readScopedTopicFile(t, hubPath)
			if competingWinner {
				if !bytes.Equal(hub, rival) {
					t.Errorf("retry changed competing complete hub: %s", hub)
				}
			} else {
				if len(hub) == 0 || strings.Count(string(hub), "[[") != 2 {
					t.Errorf("retry did not create complete two-link scoped hub: %q", hub)
				}
				for _, obs := range observations[:2] {
					if !strings.Contains(string(hub), "[["+Slugify(obs.Title, obs.ID)) {
						t.Errorf("recovered hub lacks alpha observation %d: %s", obs.ID, hub)
					}
				}
			}
			assertScopedTopicNotes(t, notes)
			if !reflect.DeepEqual(observations, scopedTopicObservations()) {
				t.Error("failed hub write or retry mutated observation data")
			}
		})
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
	exporter.createTopicTemp = func(directory, pattern string) (*os.File, error) {
		calls++
		return nil, &os.PathError{Op: "create", Path: directory, Err: os.ErrPermission}
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
	exporter.createTopicTemp = func(string, string) (*os.File, error) {
		t.Fatal("full export used scoped temporary seam")
		return nil, os.ErrPermission
	}
	exporter.publishTopicHub = func(string, string) error {
		t.Fatal("full export used scoped publication seam")
		return os.ErrPermission
	}
	result, err := exporter.Export()
	if err != nil || result.HubsCreated != 1 || len(result.Errors) != 0 {
		t.Fatalf("full Export = %+v, %v", result, err)
	}
}

func TestScopedTopicHubsPublication(t *testing.T) {
	for _, scenario := range []string{"success", "hard-link success", "permission", "permission with winner", "permission with cleanup failure"} {
		t.Run(scenario, func(t *testing.T) {
			dir, root := newReconcileVault(t)
			ms := &mockStore{exportData: &store.ExportData{Observations: scopedTopicObservations()}}
			cfg := ExportConfig{VaultPath: dir, Project: "alpha"}
			exporter := NewExporter(ms, cfg)
			hubPath := filepath.Join(root, "_topics", "shared.md")
			var writer *os.File
			exporter.createTopicTemp = func(directory, pattern string) (*os.File, error) {
				assertScopedTopicAbsent(t, hubPath)
				file, err := os.CreateTemp(directory, pattern)
				writer = file
				return file, err
			}
			calls := 0
			rival := []byte("# Complete competing winner\n\n[[alpha-one-1]]\n[[beta-one-3]]\n")
			exporter.publishTopicHub = func(temporary, destination string) error {
				calls++
				assertScopedTopicAbsent(t, destination)
				if _, err := writer.Write([]byte("must not reach file")); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("publication writer is not closed: %v", err)
				}
				content := readScopedTopicFile(t, temporary)
				if strings.Count(string(content), "[[") != 2 {
					t.Fatalf("publication content incomplete: %q", content)
				}
				info, err := os.Stat(temporary)
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0644 {
					t.Errorf("publication mode = %o, want 644", info.Mode().Perm())
				}
				if scenario == "success" {
					return identityfile.Publish(temporary, destination)
				}
				if scenario == "hard-link success" {
					// Exercise cleanup when publication retains the owned source.
					return os.Link(temporary, destination)
				}
				if scenario == "permission with winner" {
					winner := filepath.Join(dir, "complete-winner.md")
					if err := os.WriteFile(winner, rival, 0644); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(winner, destination); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "permission with cleanup failure" {
					// Replace only the test-owned temp pathname with a nonempty
					// directory to produce a real cleanup error without fake modes.
					if err := os.Rename(temporary, filepath.Join(dir, "saved-attempt")); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(temporary, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(temporary, "child"), []byte("fixture"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				return &os.LinkError{Op: "publish", Old: temporary, New: destination, Err: os.ErrPermission}
			}
			result, err := exporter.Export()
			if err != nil {
				t.Fatal(err)
			}
			wantHubs, wantErrors := 0, 1
			if scenario == "success" || scenario == "hard-link success" {
				wantHubs, wantErrors = 1, 0
			}
			if calls != 1 || result.Created != 2 || result.Skipped != 0 || result.Updated != 0 || result.Deleted != 0 || result.HubsCreated != wantHubs || len(result.Errors) != wantErrors {
				t.Fatalf("publication result = %+v, calls=%d", result, calls)
			}
			if wantErrors != 0 && !errors.Is(result.Errors[0], os.ErrPermission) {
				t.Errorf("primary publication error lost: %v", result.Errors[0])
			}
			switch scenario {
			case "permission with cleanup failure":
				if !strings.Contains(result.Errors[0].Error(), "clean topic hub temporary") {
					t.Errorf("cleanup error missing: %v", result.Errors[0])
				}
				assertScopedTopicAbsent(t, hubPath)
			case "permission with winner":
				if got := readScopedTopicFile(t, hubPath); !bytes.Equal(got, rival) {
					t.Errorf("publication failure changed competing winner: %q", got)
				}
				assertScopedTopicTempsClean(t, root)
				assertScopedTopicExport(t, ms, cfg, 0, 2, 0)
			case "permission":
				assertScopedTopicAbsent(t, hubPath)
				assertScopedTopicTempsClean(t, root)
				assertScopedTopicExport(t, ms, cfg, 0, 2, 1)
			case "success", "hard-link success":
				assertScopedTopicTempsClean(t, root)
				if got := readScopedTopicFile(t, hubPath); strings.Count(string(got), "[[") != 2 {
					t.Errorf("published hub incomplete: %q", got)
				}
			}
		})
	}
}

func assertScopedTopicAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("destination must be absent before publication: %s: %v", path, err)
	}
}

func assertScopedTopicTempsClean(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "_topics"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".topic-hub-") {
			t.Errorf("attempt-owned temporary remains: %s", entry.Name())
		}
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
	assertScopedTopicTempsClean(t, filepath.Join(cfg.VaultPath, "engram"))
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
