package main

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func TestCmdHistory(t *testing.T) {
	cfg := testConfig(t)
	id, versionID := createHistoryObservation(t, cfg)
	page := readHistoryPage(t, cfg, id, "")

	t.Run("renders bounded human history with baseline and incomplete markers", func(t *testing.T) {
		withArgs(t, "engram", "history", strconv.FormatInt(id, 10), "--limit", "1")

		stdout, stderr := captureOutput(t, func() { cmdHistory(cfg) })
		if stderr != "" {
			t.Fatalf("stderr = %q", stderr)
		}
		for _, want := range []string{
			"Observation #" + strconv.FormatInt(id, 10) + " history",
			"Baseline snapshot",
			"History may be incomplete",
			"More history is available. Continue with: engram history " + strconv.FormatInt(id, 10) + " --cursor ",
		} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout missing %q:\n%s", want, stdout)
			}
		}
		if strings.Contains(stdout, "version_id") {
			t.Fatalf("human output exposed version ID:\n%s", stdout)
		}
	})

	t.Run("returns a page without version IDs in JSON", func(t *testing.T) {
		if page.ObservationID != id || len(page.Versions) != 1 || !page.HasMore || page.NextCursor == "" {
			t.Fatalf("page = %#v", page)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
		if err != nil {
			t.Fatalf("decode cursor transport: %v", err)
		}
		if strings.Contains(string(decoded), versionID) {
			t.Fatalf("decoded cursor exposed version ID: %q", decoded)
		}
	})

	t.Run("rejects modifications and continues without overlap", func(t *testing.T) {
		stubExitWithPanic(t)
		tampered := page.NextCursor + "A"
		withArgs(t, "engram", "history", strconv.FormatInt(id, 10), "--cursor", tampered, "--json")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdHistory(cfg) })
		if recovered != exitCode(1) || !strings.Contains(stderr, "invalid history cursor") {
			t.Fatalf("tampered cursor result = recovered %v, stderr %q", recovered, stderr)
		}

		next := readHistoryPage(t, cfg, id, page.NextCursor)
		if len(next.Versions) != 1 || next.Versions[0].Content == page.Versions[0].Content {
			t.Fatalf("pages overlap: first=%#v next=%#v", page.Versions, next.Versions)
		}
	})
}

func TestCmdHistoryRejectsInvalidArguments(t *testing.T) {
	stubExitWithPanic(t)
	withArgs(t, "engram", "history", "1", "--limit", "21")

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdHistory(testConfig(t)) })
	if recovered != exitCode(1) {
		t.Fatalf("recovered = %v, want exit code 1", recovered)
	}
	if !strings.Contains(stderr, "--limit must be between 1 and 20") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func readHistoryPage(t *testing.T, cfg store.Config, id int64, cursor string) historyPage {
	t.Helper()
	args := []string{"engram", "history", strconv.FormatInt(id, 10), "--limit", "1", "--json"}
	if cursor != "" {
		args = append(args, "--cursor", cursor)
	}
	withArgs(t, args...)
	stdout, stderr := captureOutput(t, func() { cmdHistory(cfg) })
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stdout, `"version_id"`) {
		t.Fatalf("JSON exposed version ID: %s", stdout)
	}
	var page historyPage
	if err := json.Unmarshal([]byte(stdout), &page); err != nil {
		t.Fatalf("unmarshal history JSON: %v\n%s", err, stdout)
	}
	return page
}

func createHistoryObservation(t *testing.T, cfg store.Config) (int64, string) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.CreateSession("history-session", "engram", t.TempDir()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "history-session",
		Type:      "note",
		Title:     "history title",
		Content:   "original content",
		Project:   "engram",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	for _, content := range []string{"first update", "second update", "third update"} {
		content := content
		if _, err := s.UpdateObservation(id, store.UpdateObservationParams{Content: &content}); err != nil {
			t.Fatalf("update observation: %v", err)
		}
	}
	observation, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	versions, err := s.ObservationVersions(observation.SyncID, 20)
	if err != nil {
		t.Fatalf("observation versions: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE observation_versions SET is_baseline = 1, history_complete = 0 WHERE version_id = ?`, versions[0].VersionID); err != nil {
		t.Fatalf("mark incomplete baseline: %v", err)
	}
	return id, versions[0].VersionID
}
