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

func TestProjectHistoryMatchesRealWindowsMixedDirectory(t *testing.T) {
	s := newTestStore(t)
	selected := filepath.Join(t.TempDir(), "First long directory component", "Second long directory component")
	if err := os.MkdirAll(selected, 0o755); err != nil {
		t.Fatal(err)
	}
	canonical := project.DirectoryIdentity(selected)
	input, err := windows.UTF16PtrFromString(canonical)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetShortPathName(input, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || n >= uint32(len(buffer)) {
		t.Fatalf("real selected-directory short spelling: length=%d error=%v", n, err)
	}
	short := windows.UTF16ToString(buffer[:n])
	prefix := filepath.Join(filepath.Dir(short), filepath.Base(canonical))
	leaf := filepath.Join(filepath.Dir(canonical), filepath.Base(short))
	for _, mixed := range []string{prefix, leaf} {
		if strings.EqualFold(mixed, canonical) || strings.EqualFold(mixed, short) {
			t.Skip("volume exposes no distinct mixed 8.3 spelling for the selected fixture")
		}
	}
	selectedInfo, err := os.Stat(canonical)
	if err != nil {
		t.Fatal(err)
	}
	spellings := []struct{ name, directory string }{
		{"long", canonical}, {"short", short}, {"prefix", prefix}, {"leaf", leaf},
	}
	for _, spelling := range spellings {
		info, err := os.Stat(spelling.directory)
		if err != nil || !os.SameFile(selectedInfo, info) {
			t.Fatalf("%s spelling is not the selected directory: %q, %v", spelling.name, spelling.directory, err)
		}
	}
	nested := filepath.Join(canonical, "nested")
	sibling := canonical + " sibling"
	for _, directory := range []string{nested, sibling} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rows := []struct{ id, name, directory string }{
		{"long", "long-app", canonical}, {"short", "short-app", short},
		{"prefix", "prefix-app", prefix}, {"leaf", "leaf-app", leaf},
		{"duplicate", "short-app", canonical}, {"nested", "nested-app", nested},
		{"sibling", "sibling-app", sibling},
	}
	before := make(map[string]*Session)
	for _, row := range rows {
		if err := s.CreateSession(row.id, row.name, row.directory); err != nil {
			t.Fatal(err)
		}
		before[row.id], err = s.GetSession(row.id)
		if err != nil || before[row.id].Directory != row.directory {
			t.Fatalf("session spelling before history query: %s, %v", row.id, err)
		}
	}
	statsBefore, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"leaf-app", "long-app", "prefix-app", "short-app"}
	for _, spelling := range spellings {
		t.Run(spelling.name, func(t *testing.T) {
			got, err := s.ProjectHistory(spelling.directory)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("mixed directory history = %v, %v; want %v", got, err, want)
			}
		})
	}
	for _, row := range rows {
		after, err := s.GetSession(row.id)
		if err != nil || !reflect.DeepEqual(before[row.id], after) {
			t.Errorf("history query changed session %q: before=%+v after=%+v error=%v", row.id, before[row.id], after, err)
		}
	}
	statsAfter, err := s.Stats()
	if err != nil || !reflect.DeepEqual(statsBefore, statsAfter) {
		t.Errorf("history query changed counters: before=%+v after=%+v error=%v", statsBefore, statsAfter, err)
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
