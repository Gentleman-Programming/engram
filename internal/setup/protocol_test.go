package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadProtocolModeMissingFileDefaultsToFull(t *testing.T) {
	dataDir := t.TempDir()

	got := ReadProtocolMode(dataDir, "claude-code")

	if got != ProtocolModeFull {
		t.Fatalf("ReadProtocolMode() = %q, want %q", got, ProtocolModeFull)
	}
}

func TestReadProtocolModeMissingSlugKeyDefaultsToFull(t *testing.T) {
	dataDir := t.TempDir()
	writeRawProtocolModeFile(t, dataDir, `{"opencode":"slim"}`)

	got := ReadProtocolMode(dataDir, "claude-code")

	if got != ProtocolModeFull {
		t.Fatalf("ReadProtocolMode() = %q, want %q", got, ProtocolModeFull)
	}
}

func TestReadProtocolModeCorruptedJSONDefaultsToFull(t *testing.T) {
	dataDir := t.TempDir()
	writeRawProtocolModeFile(t, dataDir, `{not-valid-json`)

	got := ReadProtocolMode(dataDir, "claude-code")

	if got != ProtocolModeFull {
		t.Fatalf("ReadProtocolMode() = %q, want %q", got, ProtocolModeFull)
	}
}

func TestWriteProtocolModeRoundTrip(t *testing.T) {
	dataDir := t.TempDir()

	if err := WriteProtocolMode(dataDir, "claude-code", "slim"); err != nil {
		t.Fatalf("WriteProtocolMode() error = %v", err)
	}

	got := ReadProtocolMode(dataDir, "claude-code")
	if got != ProtocolModeSlim {
		t.Fatalf("ReadProtocolMode() after write = %q, want %q", got, ProtocolModeSlim)
	}
}

func TestWriteProtocolModeUnknownValueNormalizesToFull(t *testing.T) {
	dataDir := t.TempDir()

	if err := WriteProtocolMode(dataDir, "claude-code", "bogus"); err != nil {
		t.Fatalf("WriteProtocolMode() error = %v", err)
	}

	got := ReadProtocolMode(dataDir, "claude-code")
	if got != ProtocolModeFull {
		t.Fatalf("ReadProtocolMode() after unknown-value write = %q, want %q", got, ProtocolModeFull)
	}
}

// TestWriteProtocolModePreservesOtherSlugs proves the write path is an
// upsert, not a full-file overwrite — a second slug's mode must survive
// writing a different slug's mode into the same file.
func TestWriteProtocolModePreservesOtherSlugs(t *testing.T) {
	dataDir := t.TempDir()

	if err := WriteProtocolMode(dataDir, "opencode", "slim"); err != nil {
		t.Fatalf("WriteProtocolMode(opencode) error = %v", err)
	}
	if err := WriteProtocolMode(dataDir, "claude-code", "full"); err != nil {
		t.Fatalf("WriteProtocolMode(claude-code) error = %v", err)
	}

	if got := ReadProtocolMode(dataDir, "opencode"); got != ProtocolModeSlim {
		t.Fatalf("ReadProtocolMode(opencode) = %q, want %q", got, ProtocolModeSlim)
	}
	if got := ReadProtocolMode(dataDir, "claude-code"); got != ProtocolModeFull {
		t.Fatalf("ReadProtocolMode(claude-code) = %q, want %q", got, ProtocolModeFull)
	}
}

// TestWriteProtocolModeCorruptedFileReturnsErrorAndPreservesFile guards
// JD-013(b): a corrupted (unparseable) mode file must not be silently
// treated as "start fresh" — that would overwrite whatever previously-valid
// slug entries the file might still contain with a single-slug file,
// compounding the data loss. WriteProtocolMode must return an error instead
// and leave the on-disk file untouched.
func TestWriteProtocolModeCorruptedFileReturnsErrorAndPreservesFile(t *testing.T) {
	dataDir := t.TempDir()
	corrupted := `{not-valid-json`
	writeRawProtocolModeFile(t, dataDir, corrupted)

	err := WriteProtocolMode(dataDir, "claude-code", "slim")
	if err == nil {
		t.Fatal("WriteProtocolMode() on a corrupted file should return an error, not silently overwrite it")
	}

	raw, readErr := os.ReadFile(filepath.Join(dataDir, protocolModeFileName))
	if readErr != nil {
		t.Fatalf("read back file: %v", readErr)
	}
	if string(raw) != corrupted {
		t.Fatalf("corrupted file was modified, want untouched: got %q", string(raw))
	}
}

// TestWriteProtocolModeAtomicShapeNeverOpensFinalPathForWrite guards
// JD-013(a): the write path must go through a temp file in the same
// directory followed by os.Rename, and must never open the final
// protocol-mode.json path directly for writing (which would risk a torn
// read by a concurrent reader).
func TestWriteProtocolModeAtomicShapeNeverOpensFinalPathForWrite(t *testing.T) {
	dataDir := t.TempDir()
	finalPath := filepath.Join(dataDir, protocolModeFileName)

	var createdPaths []string
	origCreateTemp := osCreateTempFn
	osCreateTempFn = func(dir, pattern string) (*os.File, error) {
		f, err := origCreateTemp(dir, pattern)
		if err == nil {
			createdPaths = append(createdPaths, f.Name())
		}
		return f, err
	}
	t.Cleanup(func() { osCreateTempFn = origCreateTemp })

	var renamedFrom, renamedTo string
	origRename := osRenameFn
	osRenameFn = func(oldpath, newpath string) error {
		renamedFrom = oldpath
		renamedTo = newpath
		return origRename(oldpath, newpath)
	}
	t.Cleanup(func() { osRenameFn = origRename })

	if err := WriteProtocolMode(dataDir, "claude-code", "slim"); err != nil {
		t.Fatalf("WriteProtocolMode() error = %v", err)
	}

	for _, p := range createdPaths {
		if p == finalPath {
			t.Fatalf("write helper opened the final path %q directly for writing", finalPath)
		}
	}
	if renamedTo != finalPath {
		t.Fatalf("rename target = %q, want %q", renamedTo, finalPath)
	}
	if renamedFrom == finalPath {
		t.Fatal("rename source must not be the final path")
	}
}

func writeRawProtocolModeFile(t *testing.T, dataDir, content string) {
	t.Helper()
	path := filepath.Join(dataDir, protocolModeFileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write raw protocol-mode file: %v", err)
	}
}
