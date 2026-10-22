//go:build windows

package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/project"
	"golang.org/x/sys/windows"
)

func TestProjectHistoryAliasLookupError(t *testing.T) {
	s := newTestStore(t)
	directory := t.TempDir()
	if err := s.CreateSession("preserved", "local-app", directory); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetSession("preserved")
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(directory, "nonexistent selected directory")
	got, err := s.ProjectHistory(project.DirectoryIdentity(missing))
	if got != nil || !(errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)) {
		t.Fatalf("missing selected directory must expose Windows alias error: %v, %v", got, err)
	}
	after, err := s.GetSession("preserved")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed history lookup changed session: before=%+v after=%+v error=%v", before, after, err)
	}
}

func TestProjectHistoryMatchesRealWindowsShortDirectory(t *testing.T) {
	s := newTestStore(t)
	selected := filepath.Join(t.TempDir(), "Selected directory history")
	if err := os.Mkdir(selected, 0o755); err != nil {
		t.Fatal(err)
	}
	canonical := project.DirectoryIdentity(selected)
	input, err := windows.UTF16PtrFromString(canonical)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetShortPathName(input, &buffer[0], uint32(len(buffer)))
	if err != nil {
		t.Fatalf("GetShortPathName on selected directory: %v", err)
	}
	if n == 0 || n >= uint32(len(buffer)) {
		t.Fatalf("invalid short-path length: %d", n)
	}
	short := windows.UTF16ToString(buffer[:n])
	if strings.EqualFold(filepath.Clean(short), filepath.Clean(canonical)) {
		t.Skip("volume exposes no distinct 8.3 alias for selected directory")
	}
	longInfo, err := os.Stat(canonical)
	if err != nil {
		t.Fatal(err)
	}
	shortInfo, err := os.Stat(short)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(longInfo, shortInfo) {
		t.Fatal("Windows short path does not identify the selected directory")
	}
	nested := filepath.Join(selected, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	rows := []struct{ id, name, directory string }{
		{"long", "long-app", canonical},
		{"short", "short-app", short},
		{"duplicate", "short-app", canonical},
		{"short-only", "alias-app", short},
		{"nested", "nested-app", nested},
	}
	for _, row := range rows {
		if err := s.CreateSession(row.id, row.name, row.directory); err != nil {
			t.Fatal(err)
		}
	}
	got, historyErr := s.ProjectHistory(project.DirectoryIdentity(selected))
	for _, row := range rows {
		session, err := s.GetSession(row.id)
		if err != nil {
			t.Fatal(err)
		}
		if session.Directory != row.directory {
			t.Fatalf("history changed session %q directory: got %q, want %q", row.id, session.Directory, row.directory)
		}
	}
	want := []string{"alias-app", "long-app", "short-app"}
	if historyErr != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("history for real long/8.3 directory = %v, %v; want %v", got, historyErr, want)
	}
}
