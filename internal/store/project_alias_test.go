package store

import (
	"encoding/json"
	"errors"
	"fmt"
	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestAlias(t *testing.T) {
	s := newTestStore(t)
	cwd := filepath.Join(t.TempDir(), "child", "..")
	evidence := &ProjectProvenance{Source: "explicit_override", Directory: "raw/../capture", RawProject: " Old--Name "}
	if err := s.CreateSessionWithOwnershipMode("alias-history", "old-name", cwd, SessionOwnershipShared, evidence); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("old-name"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "alias-history", Project: "old-name", Type: "note", Title: "history", Content: "preserve history", OperationID: "alias-history-operation", ProjectProvenance: evidence}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "alias-history", Project: "old-name", Content: "preserve prompt", ProjectProvenance: evidence}); err != nil {
		t.Fatal(err)
	}
	before := aliasProtectedState(t, s)
	check := func() {
		t.Helper()
		if after := aliasProtectedState(t, s); before != after {
			t.Fatalf("protected public state changed\nbefore %s\nafter %s", before, after)
		}
	}
	key, err := s.SetProjectAlias(ProjectAliasKindKey, " Old--Name ", " Target--Name ")
	if err != nil {
		t.Fatal(err)
	}
	if *key != (ProjectAlias{Kind: ProjectAliasKindKey, Key: "old-name", Project: "target-name"}) {
		t.Fatalf("alias %#v", key)
	}
	check()
	if _, err := s.SetProjectAlias(ProjectAliasKindCWD, cwd, "target-name"); err != nil {
		t.Fatal(err)
	}
	check()
	got, err := s.ResolveProjectAlias("OLD--NAME", filepath.Clean(cwd))
	if err != nil || got.Conflict || got.Project != "target-name" || got.KeyAlias == nil || got.CwdAlias == nil {
		t.Fatalf("agreement %#v %v", got, err)
	}
	check()
	if _, err := s.SetProjectAlias(ProjectAliasKindCWD, cwd, "other"); err != nil {
		t.Fatal(err)
	}
	check()
	got, err = s.ResolveProjectAlias("old-name", cwd)
	if err != nil || !got.Conflict || got.Project != "" || got.KeyAlias.Project != "target-name" || got.CwdAlias.Project != "other" {
		t.Fatalf("conflict %#v %v", got, err)
	}
	check()
	aliases, err := s.ListProjectAliases()
	absoluteCWD, absErr := filepath.Abs(filepath.Clean(cwd))
	if absErr != nil {
		t.Fatal(absErr)
	}
	wantAliases := []ProjectAlias{{Kind: ProjectAliasKindCWD, Key: absoluteCWD, Project: "other"}, {Kind: ProjectAliasKindKey, Key: "old-name", Project: "target-name"}}
	if err != nil || !reflect.DeepEqual(aliases, wantAliases) {
		t.Fatalf("list %#v %v", aliases, err)
	}
	check()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := mustDefaultConfig(t)
	cfg.DataDir = s.cfg.DataDir
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	reopened, err := s.ListProjectAliases()
	if err != nil || !reflect.DeepEqual(reopened, wantAliases) {
		t.Fatalf("persisted %#v %v", reopened, err)
	}
	if after := aliasProtectedState(t, s); before != after {
		t.Fatal("reopen changed historical state")
	}
	for _, input := range []struct{ key, cwd, want string }{{"old-name", "", "target-name"}, {"", cwd, "other"}} {
		result, err := s.ResolveProjectAlias(input.key, input.cwd)
		if err != nil || result.Project != input.want || result.Conflict {
			t.Fatalf("single match %#v %v", result, err)
		}
		check()
	}
	for _, bad := range []struct {
		kind        ProjectAliasKind
		key, target string
	}{
		{"bad", "old-name", "target"}, {ProjectAliasKindKey, "bad/path", "target"}, {ProjectAliasKindKey, "old-name", "bad/path"}, {ProjectAliasKindCWD, "", "target"}, {ProjectAliasKindKey, "", "target"}, {ProjectAliasKindKey, "old-name", ""}, {ProjectAliasKindKey, "bad\nname", "target"}, {ProjectAliasKindCWD, "bad\x00cwd", "target"},
	} {
		check()
		_, err := s.SetProjectAlias(bad.kind, bad.key, bad.target)
		if bad.kind == "bad" {
			aliasExactError(t, err, `invalid project alias kind: "bad"`)
		} else if bad.kind == ProjectAliasKindCWD {
			aliasExactError(t, err, fmt.Sprintf("invalid project alias cwd: %q", bad.key))
		} else if !errors.Is(err, projectpkg.ErrInvalidProjectName) {
			t.Fatalf("invalid name error %v", err)
		}
		check()
		after, err := s.ListProjectAliases()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(aliases, after) {
			t.Fatal("invalid input mutated aliases")
		}
	}
	_, err = s.RemoveProjectAlias("bad", "old-name")
	aliasExactError(t, err, `invalid project alias kind: "bad"`)
	check()
	_, err = s.RemoveProjectAlias(ProjectAliasKindKey, "bad/path")
	if !errors.Is(err, projectpkg.ErrInvalidProjectName) {
		t.Fatalf("invalid removal error %v", err)
	}
	check()
	current, err := s.ListProjectAliases()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current, aliases) {
		t.Fatal("invalid removal mutated aliases")
	}
	for _, a := range aliases {
		ok, err := s.RemoveProjectAlias(a.Kind, a.Key)
		if err != nil || !ok {
			t.Fatalf("remove %v %v", ok, err)
		}
		check()
		remaining, err := s.ListProjectAliases()
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range remaining {
			if entry.Kind == a.Kind && entry.Key == a.Key {
				t.Fatalf("removed alias persisted %#v", entry)
			}
		}
		ok, err = s.RemoveProjectAlias(a.Kind, a.Key)
		if err != nil || ok {
			t.Fatalf("missing remove %v %v", ok, err)
		}
		check()
	}
	for _, key := range []string{"unknown", " Unknown--Raw ", ""} {
		got, err := s.ResolveProjectAlias(key, "")
		if err != nil || got.Project != key || got.Conflict || got.KeyAlias != nil || got.CwdAlias != nil {
			t.Fatalf("fallback %#v %v", got, err)
		}
		check()
	}
	if after := aliasProtectedState(t, s); before != after {
		t.Fatalf("aliases changed protected state\nbefore %s\nafter %s", before, after)
	}
	empty, err := s.ListProjectAliases()
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty aliases %#v %v", empty, err)
	}
	_, err = s.ResolveProjectAlias("bad/path", cwd)
	if !errors.Is(err, projectpkg.ErrInvalidProjectName) {
		t.Fatalf("invalid resolve error %v", err)
	}
	check()
}

func aliasExactError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("error %v want %q", err, want)
	}
}

func aliasProtectedState(t *testing.T, s *Store) string {
	t.Helper()
	snapshot := map[string]any{}
	record := func(name string, value any, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("snapshot %s: %v", name, err)
		}
		snapshot[name] = value
	}
	session, err := s.GetSession("alias-history")
	record("session", session, err)
	observations, err := s.SessionObservations("alias-history", 100)
	record("observations", observations, err)
	if len(observations) != 1 {
		t.Fatalf("expected seeded history, got %d", len(observations))
	}
	for _, observation := range observations {
		got, err := s.GetObservation(observation.ID)
		record("observation", got, err)
		versions, err := s.GetObservationVersions(observation.ID)
		record("versions", versions, err)
		evidence, err := s.CaptureProjectProvenance("observation", strconv.FormatInt(observation.ID, 10))
		record("observation-evidence", evidence, err)
		if evidence == nil {
			t.Fatal("missing seeded observation evidence")
		}
	}
	prompts, err := s.RecentPrompts("old-name", 100)
	record("prompts", prompts, err)
	if len(prompts) != 1 {
		t.Fatalf("expected seeded prompt, got %d", len(prompts))
	}
	evidence, err := s.CaptureProjectProvenance("prompt", strconv.FormatInt(prompts[0].ID, 10))
	record("prompt-evidence", evidence, err)
	if evidence == nil {
		t.Fatal("missing seeded prompt evidence")
	}
	evidence, err = s.CaptureProjectProvenance("session", "alias-history")
	record("session-evidence", evidence, err)
	if evidence == nil {
		t.Fatal("missing seeded session evidence")
	}
	enrolled, err := s.ListEnrolledProjects()
	record("enrolled", enrolled, err)
	for _, project := range []string{"old-name", "target-name", "other"} {
		enrolled, err := s.IsProjectEnrolled(project)
		record("enrolled-"+project, enrolled, err)
	}
	states, err := s.ListSyncStates()
	record("states", states, err)
	for _, state := range states {
		detail, err := s.GetSyncState(state.TargetKey)
		record("state-"+state.TargetKey, detail, err)
	}
	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	record("pending", pending, err)
	if len(pending) == 0 {
		t.Fatal("seeded queue is empty")
	}
	highwater, err := s.MaxPendingSyncMutationSeq(DefaultSyncTargetKey)
	record("highwater", highwater, err)
	if highwater <= 0 {
		t.Fatal("seeded highwater is empty")
	}
	stats, err := s.Stats()
	record("stats", stats, err)
	projectStats, err := s.StatsProject("old-name")
	record("project-stats", projectStats, err)
	projects, err := s.ListProjectsWithStats("")
	record("projects", projects, err)
	operationID, err := s.GetObservationSaveResult("alias-history-operation")
	record("operation", operationID, err)
	if operationID != observations[0].ID {
		t.Fatalf("operation ID %d want %d", operationID, observations[0].ID)
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
