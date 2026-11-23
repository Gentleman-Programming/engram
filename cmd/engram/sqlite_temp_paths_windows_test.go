package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteTempDirectoryStaysPinnedUntilLastRelease(t *testing.T) {
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
