package cloudconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPathJoinsDataDir verifies that Path() produces <dataDir>/cloud.json
// using the host's path separator, regardless of trailing slashes in the
// data dir argument.
func TestPathJoinsDataDir(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		dataDir string
		want   string
	}{
		{"plain dir", "/foo", filepath.Join("/foo", "cloud.json")},
		{"trailing slash", "/foo/", filepath.Join("/foo", "cloud.json")},
		{"relative dir", "data", filepath.Join("data", "cloud.json")},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Path(tc.dataDir)
			if got != tc.want {
				t.Fatalf("Path(%q) = %q, want %q", tc.dataDir, got, tc.want)
			}
			if !strings.HasSuffix(got, "cloud.json") {
				t.Fatalf("Path(%q) = %q, want suffix cloud.json", tc.dataDir, got)
			}
		})
	}
}

// TestLoadReturnsZeroValueOnNotExist verifies that when cloud.json does not
// exist, Load returns a non-nil zero-value Config and a nil error.
// This matches the existing TUI semantics (zero-value > nil pointer) so
// the migration from cmd/engram does not change call-site behavior.
func TestLoadReturnsZeroValueOnNotExist(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(%q) on missing file returned err=%v, want nil", dir, err)
	}
	if cfg == nil {
		t.Fatalf("Load(%q) on missing file returned nil cfg, want non-nil zero-value", dir)
	}
	if cfg.ServerURL != "" || cfg.Token != "" {
		t.Fatalf("Load(%q) on missing file returned non-zero cfg=%+v, want zero-value", dir, cfg)
	}
}

// TestLoadReturnsErrorOnMalformedJSON verifies that when cloud.json exists
// but contains invalid JSON, Load returns a non-nil error and does not
// silently produce a zero-value.
func TestLoadReturnsErrorOnMalformedJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup MkdirAll: %v", err)
	}
	if err := os.WriteFile(Path(dir), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("setup WriteFile: %v", err)
	}

	cfg, err := Load(dir)
	if err == nil {
		t.Fatalf("Load(%q) on malformed JSON returned err=nil, want non-nil", dir)
	}
	if cfg != nil {
		t.Fatalf("Load(%q) on malformed JSON returned non-nil cfg=%+v, want nil", dir, cfg)
	}
}

// TestSaveCreatesDirAndFile verifies that Save creates the data directory
// with mode 0o755 and the cloud.json file with mode 0o644. The file mode is
// verified via os.Stat to prove the on-disk permissions match the spec.
func TestSaveCreatesDirAndFile(t *testing.T) {
	t.Parallel()

	// Use a nested subdir that does not yet exist; Save should create it.
	parent := t.TempDir()
	dir := filepath.Join(parent, "nested", "data")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("setup: dir %q unexpectedly exists", dir)
	}

	cfg := &Config{ServerURL: "https://cloud.example.com", Token: "secret-token"}
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save(%q, %+v) returned err=%v, want nil", dir, cfg, err)
	}

	// Directory must exist with mode 0o755.
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%q) after Save: %v", dir, err)
	}
	if !dirInfo.IsDir() {
		t.Fatalf("Stat(%q) after Save: not a directory", dir)
	}
	if got := dirInfo.Mode().Perm(); got != 0o755 {
		t.Fatalf("dir %q mode = %o, want 0o755", dir, got)
	}

	// File must exist with mode 0o644.
	fileInfo, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatalf("Stat(%q) after Save: %v", Path(dir), err)
	}
	if fileInfo.IsDir() {
		t.Fatalf("Stat(%q) after Save: unexpectedly a directory", Path(dir))
	}
	if got := fileInfo.Mode().Perm(); got != 0o644 {
		t.Fatalf("file %q mode = %o, want 0o644", Path(dir), got)
	}
}

// TestLoadAcceptsEmptyConfig verifies that when cloud.json exists but
// is the empty object `{}`, Load returns a non-nil zero-value Config
// and a nil error. This is a valid file on disk and must not be
// treated as malformed.
func TestLoadAcceptsEmptyConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup MkdirAll: %v", err)
	}
	if err := os.WriteFile(Path(dir), []byte("{}"), 0o644); err != nil {
		t.Fatalf("setup WriteFile: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(%q) on empty {} returned err=%v, want nil", dir, err)
	}
	if cfg == nil {
		t.Fatalf("Load(%q) on empty {} returned nil cfg, want non-nil", dir)
	}
	if cfg.ServerURL != "" || cfg.Token != "" {
		t.Fatalf("Load(%q) on empty {} returned non-zero cfg=%+v, want zero-value", dir, cfg)
	}
}

// TestSaveThenLoadRoundTrip verifies that a Config saved with Save can
// be recovered byte-equivalent with Load. This guards against silent
// field renames, missing JSON tags, or non-deterministic encoding.
func TestSaveThenLoadRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	original := &Config{
		ServerURL: "https://cloud.example.com",
		Token:     "tok-abc-123",
	}

	if err := Save(dir, original); err != nil {
		t.Fatalf("Save(%q, %+v) returned err=%v, want nil", dir, original, err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(%q) returned err=%v, want nil", dir, err)
	}
	if got == nil {
		t.Fatalf("Load(%q) returned nil cfg, want non-nil", dir)
	}
	if got.ServerURL != original.ServerURL {
		t.Fatalf("Load: ServerURL = %q, want %q", got.ServerURL, original.ServerURL)
	}
	if got.Token != original.Token {
		t.Fatalf("Load: Token = %q, want %q", got.Token, original.Token)
	}
}

// TestSaveOverwriteKeepsConfiguredMode verifies that calling Save on a
// directory that already has a cloud.json (with whatever mode) still
// produces a file with the spec's mode 0o644. Save is the source of
// truth for the file mode; an older file with the wrong mode is
// corrected on the next save.
func TestSaveOverwriteKeepsConfiguredMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup MkdirAll: %v", err)
	}
	// Pre-existing file with a non-spec mode (simulate an old artifact).
	if err := os.WriteFile(Path(dir), []byte(`{"server_url":"x","token":"y"}`), 0o600); err != nil {
		t.Fatalf("setup WriteFile: %v", err)
	}

	cfg := &Config{ServerURL: "https://cloud.example.com", Token: "new-token"}
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save(%q, %+v) returned err=%v, want nil", dir, cfg, err)
	}

	info, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatalf("Stat(%q) after overwrite: %v", Path(dir), err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("file %q mode after overwrite = %o, want 0o644", Path(dir), got)
	}

	// And the round-trip reflects the new content.
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(%q) after overwrite: %v", dir, err)
	}
	if got.Token != "new-token" {
		t.Fatalf("Load: Token = %q, want %q", got.Token, "new-token")
	}
}
