package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
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
			release, err := prepareSQLiteTemp(data)
			if release != nil {
				defer release()
			}
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
			secondRelease, err := prepareSQLiteTemp(data)
			if err != nil {
				t.Fatal(err)
			}
			defer secondRelease()
			got, err := os.ReadFile(filepath.Join(data, "tmp", "sqlite", "other-process"))
			if err != nil || string(got) != "preserve" {
				t.Fatalf("shared file changed: %q %v", got, err)
			}
		})
	}
}

func TestSQLiteTempDirectoryStaysPinnedUntilLastRelease(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows temporary directory semantics")
	}
	root := t.TempDir()
	t.Setenv("TEMP", root)
	t.Setenv("TMP", root)
	data := filepath.Join(root, "data")
	dir := filepath.Join(data, "tmp", "sqlite")
	first, err := prepareSQLiteTemp(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareSQLiteTemp(data)
	first()
	if err != nil {
		t.Fatal(err)
	}
	markers, err := filepath.Glob(filepath.Join(dir, ".engram-temp-*"))
	if err != nil || len(markers) != 1 {
		second()
		t.Fatalf("expected only the remaining command's marker, got %v (%v)", markers, err)
	}
	probe, err := os.CreateTemp(dir, "other-process-")
	if err == nil {
		err = probe.Close()
	}
	renamed := filepath.Join(data, "tmp", "renamed")
	renameErr := os.Rename(dir, renamed)
	second()
	if err != nil {
		t.Fatal(err)
	}
	if renameErr == nil {
		t.Fatal("directory could be renamed while the second process handle was retained")
	}
	if err := os.Rename(dir, renamed); err != nil {
		t.Fatalf("directory was still held after both handles were released: %v", err)
	}
	markers, err = filepath.Glob(filepath.Join(renamed, ".engram-temp-*"))
	if err != nil || len(markers) != 0 {
		t.Fatalf("temporary markers survived all command exits: %v (%v)", markers, err)
	}
}

func TestSQLiteTempLongWindowsPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows extended paths")
	}
	root := t.TempDir()
	t.Setenv("TEMP", root)
	t.Setenv("TMP", root)
	data := filepath.Join(root, strings.Repeat("x", max(1, 252-len(root)-1)))
	release, err := prepareSQLiteTemp(data)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if len(os.Getenv("TEMP")) <= 260 {
		t.Fatal("expected a temporary directory beyond MAX_PATH")
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
