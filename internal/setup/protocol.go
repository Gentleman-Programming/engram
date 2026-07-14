package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Protocol mode values persisted per setup slug. A slug's mode controls how
// verbose the version-pinned Claude Code hook scripts render the ACTIVE
// PROTOCOL prose at session start / post-compaction — see
// openspec/changes/setup-protocol-flag/proposal.md.
const (
	ProtocolModeSlim = "slim"
	ProtocolModeFull = "full"
)

// protocolModeFileName is the mode-persistence file inside the resolved
// engram data directory (store.Config.DataDir — the SQLite home, not the
// project-scoped .engram/config.json chunks dir).
const protocolModeFileName = "protocol-mode.json"

// ReadProtocolMode returns the persisted protocol mode for slug inside
// dataDir. It defaults to ProtocolModeFull whenever the mode cannot be
// resolved with certainty: missing file, unreadable file, malformed JSON,
// or no entry for slug. This keeps every failure mode safe (never silently
// suppresses the protocol prose).
func ReadProtocolMode(dataDir, slug string) string {
	modes, err := readProtocolModeFile(dataDir)
	if err != nil {
		return ProtocolModeFull
	}

	mode, ok := modes[slug]
	if !ok {
		return ProtocolModeFull
	}

	return normalizeProtocolMode(mode)
}

// WriteProtocolMode upserts slug's mode into dataDir's mode file, preserving
// entries for other slugs. Unknown or empty mode values normalize to
// ProtocolModeFull rather than failing — persistence itself never fails
// `engram setup` (callers may still surface a warning for unknown input).
//
// A missing file starts fresh. A file that exists but fails to parse
// (corrupted) is NOT silently treated as "start fresh" — that would
// overwrite whatever other slugs' modes it might still contain with a
// single-entry file, compounding the data loss. Instead the write is
// refused and an error is returned so the caller can warn and leave the
// file for manual inspection/recovery (the read path still degrades to
// ProtocolModeFull for any unparseable file, as before).
func WriteProtocolMode(dataDir, slug, mode string) error {
	modes, err := readProtocolModeFile(dataDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("protocol mode file is corrupted, refusing to overwrite: %w", err)
		}
		modes = map[string]string{}
	}

	modes[slug] = normalizeProtocolMode(mode)

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}

	return writeProtocolModeFileAtomic(dataDir, modes)
}

// osCreateTempFn and osRenameFn are indirection seams over os.CreateTemp /
// os.Rename so tests can verify the atomic write shape (temp file in the
// same directory, then rename into place) without racing a real concurrent
// reader against the file on disk.
var (
	osCreateTempFn = os.CreateTemp
	osRenameFn     = os.Rename
)

// writeProtocolModeFileAtomic serializes modes and publishes it atomically:
// write to a temp file in dataDir, then os.Rename over the final path. This
// never opens the final path for writing directly, so a concurrent reader
// never observes a truncated or partially-written file.
func writeProtocolModeFileAtomic(dataDir string, modes map[string]string) error {
	raw, err := json.MarshalIndent(modes, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := osCreateTempFn(dataDir, protocolModeFileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // best-effort cleanup; no-op after a successful rename

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return osRenameFn(tmpPath, protocolModeFilePath(dataDir))
}

func protocolModeFilePath(dataDir string) string {
	return filepath.Join(dataDir, protocolModeFileName)
}

// readProtocolModeFile reads and parses the mode file. A missing file is
// reported as an error so callers can distinguish "start fresh" (write path)
// from "default to full" (read path) without duplicating os.IsNotExist checks.
func readProtocolModeFile(dataDir string) (map[string]string, error) {
	raw, err := os.ReadFile(protocolModeFilePath(dataDir))
	if err != nil {
		return nil, err
	}

	var modes map[string]string
	if err := json.Unmarshal(raw, &modes); err != nil {
		return nil, err
	}
	if modes == nil {
		modes = map[string]string{}
	}
	return modes, nil
}

// normalizeProtocolMode returns ProtocolModeSlim only for an exact
// case-insensitive match; every other value (unknown, empty, malformed)
// normalizes to ProtocolModeFull.
func normalizeProtocolMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), ProtocolModeSlim) {
		return ProtocolModeSlim
	}
	return ProtocolModeFull
}
