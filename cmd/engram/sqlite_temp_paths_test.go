package main

import (
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSQLiteTempPaths(t *testing.T) {
	for _, scenario := range []string{"shared directory", "relative data directory", "blocked directory"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("TEMP", root)
			t.Setenv("TMP", root)
			data := filepath.Join(root, "data")
			wantError := ""
			switch scenario {
			case "relative data directory":
				data = "relative-data"
				wantError = "absolute path"
			case "blocked directory":
				if err := os.MkdirAll(data, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(data, "tmp"), []byte("blocker"), 0600); err != nil {
					t.Fatal(err)
				}
				wantError = "create SQLite temporary directory"
			case "shared directory":
				dir := filepath.Join(data, "tmp", "sqlite")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "other-process"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := prepareSQLiteTemp(data)
			if runtime.GOOS != "windows" {
				if err != nil || os.Getenv("TEMP") != root || os.Getenv("TMP") != root {
					t.Fatalf("non-Windows changed environment: %v", err)
				}
				return
			}
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("got %v, want %s", err, wantError)
				}
				if os.Getenv("TEMP") != root || os.Getenv("TMP") != root {
					t.Fatal("rejected startup changed environment")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := prepareSQLiteTemp(data); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(data, "tmp", "sqlite", "other-process"))
			if err != nil || string(got) != "preserve" {
				t.Fatalf("shared file changed: %q %v", got, err)
			}
		})
	}
}

func TestSQLiteTempConfigFree(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TEMP", root)
	t.Setenv("TMP", root)
	oldArgs, oldConfig := os.Args, storeDefaultConfig
	t.Cleanup(func() { os.Args = oldArgs; storeDefaultConfig = oldConfig })
	storeDefaultConfig = func() (store.Config, error) {
		t.Fatal("config-free command resolved configuration")
		return store.Config{}, nil
	}
	os.Args = []string{"engram", "--version"}
	main()
	if os.Getenv("TEMP") != root || os.Getenv("TMP") != root {
		t.Fatal("config-free command changed environment")
	}
}
