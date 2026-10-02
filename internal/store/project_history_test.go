package store

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/project"
)

func TestProjectHistoryIgnoresRelativeDirectories(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	t.Chdir(dir)
	relative := []string{".", filepath.Join("nested", "..")}
	if runtime.GOOS == "windows" {
		// Root-relative Windows paths also lack a recorded volume/base.
		relative = append(relative, strings.TrimPrefix(dir, filepath.VolumeName(dir)))
	}
	for i, path := range relative {
		if _, err := s.db.Exec(`INSERT INTO sessions(id, project, directory) VALUES (?, 'other-app', ?)`, i, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSession("absolute", "local-app", dir); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProjectHistory(project.DirectoryIdentity(dir))
	if err != nil || !reflect.DeepEqual(got, []string{"local-app"}) {
		t.Fatalf("relative history acquired current cwd: %v, %v", got, err)
	}
	if got, err := s.ProjectHistory(""); err != nil || len(got) != 0 {
		t.Fatalf("empty directory matched unusable history: %v, %v", got, err)
	}
}

func TestProjectHistoryDoesNotResolveHistoricalAliases(t *testing.T) {
	s := newTestStore(t)
	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "historical-alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("local symlink unavailable: %v", err)
	}
	if err := s.CreateSession("alias", "other-app", alias); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProjectHistory(project.DirectoryIdentity(target))
	if err != nil || len(got) != 0 {
		t.Fatalf("historical alias was dereferenced: %v, %v", got, err)
	}
}

func TestHistoricalDirectoryIdentityIsLexical(t *testing.T) {
	if got := historicalDirectoryIdentity("."); got != "" {
		t.Fatalf("relative path gained a base: %q", got)
	}
	if runtime.GOOS == "windows" {
		// This is a string-only comparison; it must never probe the share.
		got := historicalDirectoryIdentity(`\\Nonlocal.Invalid\Share\folder\..\project`)
		if got != `\\nonlocal.invalid\share\project` {
			t.Fatalf("UNC lexical identity = %q", got)
		}
	}
}

func TestProjectHistoryCanonicalImportedNames(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	// Import preserves raw session labels; exercise that legacy representation.
	for i, name := range []string{"Remote-App", " remote-app ", "remote--app", " ", "invalid/name"} {
		if _, err := s.db.Exec(`INSERT INTO sessions(id, project, directory) VALUES (?, ?, ?)`, i, name, dir); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ProjectHistory(project.DirectoryIdentity(dir))
	if err != nil || !reflect.DeepEqual(got, []string{"remote-app"}) {
		t.Fatalf("canonical evidence=%v,%v", got, err)
	}
}

func TestProjectHistoryExactDirectory(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ id, name, dir string }{
		{"a", "local-app", dir}, {"b", "explicit-app", dir}, {"c", "local-app", dir},
		{"d", "nested-app", nested}, {"e", "no-directory", ""},
	} {
		if err := s.CreateSession(tt.id, tt.name, tt.dir); err != nil {
			t.Fatal(err)
		}
	}
	variants := []string{dir, filepath.Join(dir, "nested", "..")}
	if runtime.GOOS == "windows" {
		variants = append(variants, strings.ToUpper(dir))
	}
	for _, variant := range variants {
		got, err := s.ProjectHistory(project.DirectoryIdentity(variant))
		if err != nil || !reflect.DeepEqual(got, []string{"explicit-app", "local-app"}) {
			t.Fatalf("history(%q)=%v,%v", variant, got, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProjectHistory(project.DirectoryIdentity(dir)); err == nil {
		t.Fatal("closed store must not look like empty history")
	}
}
