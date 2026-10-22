package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	versioncheck "github.com/Gentleman-Programming/engram/v3/internal/version"
)

func TestSemanticSearchValidationBeforeOpen(t *testing.T) {
	for _, args := range [][]string{{"query", "--search-mode", "bad"}, {"query", "--search-mode"}, {"query", "--search-mode", "semantic", "--match", "all"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			old := storeNew
			opened := false
			storeNew = func(store.Config) (*store.Store, error) { opened = true; return nil, nil }
			t.Cleanup(func() { storeNew = old })
			withArgs(t, append([]string{"engram", "search"}, args...)...)
			_, stderr, code := captureExitPanic(t, func() { cmdSearch(store.Config{}) })
			if code != 1 || stderr == "" || opened {
				t.Fatalf("exit=%d stderr=%q opened=%v", code, stderr, opened)
			}
		})
	}
}

// Exercise actual CLI dispatch with an owned store and a synthetic Ollama only.
func TestEmbedMainRoundTrip(t *testing.T) {
	stubCheckForUpdates(t, versioncheck.CheckResult{Status: versioncheck.StatusUpToDate})
	cfg := testConfig(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ENGRAM_DATA_DIR", cfg.DataDir)
	var mu sync.Mutex
	var inputs [][]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"name": "local:latest", "digest": strings.Repeat("a", 64)}}})
		case "/api/embed":
			var body struct {
				Input    []string `json:"input"`
				Truncate bool     `json:"truncate"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Truncate {
				t.Error("truncation enabled")
			}
			mu.Lock()
			inputs = append(inputs, body.Input)
			mu.Unlock()
			vectors := make([][]float64, len(body.Input))
			for i := range vectors {
				vectors[i] = []float64{1, 0}
			}
			json.NewEncoder(w).Encode(map[string]any{"model": "local:latest", "embeddings": vectors})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected", 404)
		}
	}))
	defer ts.Close()
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "local")
	t.Setenv("ENGRAM_EMBEDDING_URL", ts.URL)
	oldCfg, oldMigration := storeDefaultConfig, migrateOrphanedDatabase
	storeDefaultConfig = func() (store.Config, error) { return cfg, nil }
	migrateOrphanedDatabase = func(string) {} // No orphan scanning or private data in CLI tests.
	t.Cleanup(func() { storeDefaultConfig = oldCfg; migrateOrphanedDatabase = oldMigration })
	run := func(args ...string) (string, string, int) {
		withArgs(t, append([]string{"engram"}, args...)...)
		return captureExitPanic(t, main)
	}
	for _, args := range [][]string{
		{"save", "first", "alpha content", "--project", "alpha", "--scope", "project", "--type", "decision"},
		{"save", "second", "beta content", "--project", "beta", "--scope", "personal", "--type", "bugfix"},
	} {
		out, err, code := run(args...)
		if code != 0 || err != "" || !strings.Contains(out, "Memory saved") {
			t.Fatalf("save: %d %q %q", code, out, err)
		}
	}
	t.Setenv("ENGRAM_EMBEDDING_URL", "https://malformed.example/path")
	for _, match := range []string{"", "all", "any"} {
		args := []string{"search", "alpha", "--all"}
		if match != "" {
			args = append(args, "--match", match)
		}
		out, err, code := run(args...)
		if code != 0 || err != "" || !strings.Contains(out, "Found 1 memories") {
			t.Fatalf("lexical: %d %q %q", code, out, err)
		}
	}
	t.Setenv("ENGRAM_EMBEDDING_URL", ts.URL)
	mu.Lock()
	n := len(inputs)
	mu.Unlock()
	if n != 0 {
		t.Fatal("lexical/save embedded documents")
	}
	out, err, code := run("search", "unrelated", "--all", "--search-mode", "semantic")
	if code != 1 || out != "" || !strings.Contains(err, "engram embed") {
		t.Fatalf("missing: %d %q %q", code, out, err)
	}
	for _, tc := range []struct {
		args  []string
		count int
	}{
		{[]string{"embed", "--project", "alpha", "--scope", "project", "--type", "decision"}, 1},
		{[]string{"embed", "--all"}, 2},
	} {
		out, err, code = run(tc.args...)
		var result store.SemanticIndexResult
		if code != 0 || err != "" || json.Unmarshal([]byte(out), &result) != nil || result.Indexed != tc.count {
			t.Fatalf("embed: %d %q %q", code, out, err)
		}
	}
	mu.Lock()
	before := len(inputs)
	mu.Unlock()
	out, err, code = run("search", "unrelated", "--project", "alpha", "--scope", "project", "--type", "decision", "--search-mode", "semantic")
	if code != 0 || err != "" || !strings.Contains(out, "potentially related") || !strings.Contains(out, "Similarity: 1") || strings.Contains(out, "second") {
		t.Fatalf("semantic: %d %q %q", code, out, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(inputs) != before+1 || len(inputs[len(inputs)-1]) != 1 || inputs[len(inputs)-1][0] != "unrelated" {
		t.Fatalf("query generated documents: %v", inputs)
	}
	// Reopening via each command proves the derived cache was persisted.
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "engram.db")); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticLazyConfiguration(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "local")
	t.Setenv("ENGRAM_EMBEDDING_URL", "https://invalid.example")
	cfg = configureSemantic(cfg)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Search("hello", store.SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SearchContext(context.Background(), "hello", store.SearchOptions{SearchMode: "semantic"}); err == nil {
		t.Fatal("semantic must validate optional configuration")
	}
	if configureSemantic(cfg).EmbeddingProvider != cfg.EmbeddingProvider {
		t.Fatal("injected provider replaced")
	}
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "")
	if configureSemantic(store.Config{}).EmbeddingProvider != nil {
		t.Fatal("no model must leave provider nil")
	}
}

func TestEmbedValidationBeforeOpen(t *testing.T) {
	for _, args := range [][]string{{"--force"}, {"--limit", "1"}, {"--project"}, {"--scope"}, {"--type"}, {"--project", "alpha", "--all"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			old := storeNew
			opened := false
			storeNew = func(store.Config) (*store.Store, error) { opened = true; return nil, nil }
			t.Cleanup(func() { storeNew = old })
			withArgs(t, append([]string{"engram", "embed"}, args...)...)
			out, err, code := captureExitPanic(t, func() { cmdEmbed(store.Config{}) })
			if code != 1 || out != "" || err == "" || opened {
				t.Fatalf("exit=%d stdout=%q stderr=%q opened=%v", code, out, err, opened)
			}
		})
	}
}

func TestEmbedSQLiteClassification(t *testing.T) {
	if !usesLocalSQLite([]string{"embed"}) {
		t.Fatal("embed must use SQLite temp bootstrap")
	}
}
