// Package cloudconfig owns cloud configuration IO, token resolution, URL
// validation, and local-daemon probing shared by the engram CLI and the
// Bubbletea TUI.
//
// The package is the single source of truth for cloud.json layout and
// token precedence. The TUI and CLI both consume it directly; no
// helper is re-implemented in either caller.
package cloudconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config is the on-disk shape of cloud.json. Both fields are optional;
// a zero-value Config is a valid result from Load when the file is
// absent.
type Config struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
}

// Path returns the absolute (or relative, matching the input) path to
// cloud.json inside the supplied data directory.
func Path(dataDir string) string {
	return filepath.Join(dataDir, "cloud.json")
}

// Load reads cloud.json from dataDir and returns the decoded Config.
//
// When the file does not exist, Load returns a non-nil zero-value
// Config and a nil error. This matches the existing TUI semantics
// (zero-value is more useful than a nil pointer for call sites) and
// preserves the migration from cmd/engram's loadCloudConfig.
//
// When the file exists but contains invalid JSON, Load returns a
// non-nil error and a nil Config so callers can distinguish the two
// cases.
func Load(dataDir string) (*Config, error) {
	data, err := os.ReadFile(Path(dataDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save writes cfg to cloud.json inside dataDir. The data directory
// is created with mode 0o755 if it does not exist; the file is
// written with mode 0o644.
//
// os.WriteFile only applies the permission bits on file creation, so
// a pre-existing file would keep whatever mode it already had. Save
// chmods the file after writing to guarantee the on-disk mode is
// always 0o644, regardless of prior state.
func Save(dataDir string, cfg *Config) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	path := Path(dataDir)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}

	// Normalize the file mode on every write so existing files do not
	// retain a stale, non-spec permission.
	return os.Chmod(path, 0o644)
}
