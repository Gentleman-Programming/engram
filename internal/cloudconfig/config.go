// Package cloudconfig owns cloud.json persistence and runtime configuration
// primitives shared by the CLI and terminal UI.
package cloudconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config is the on-disk cloud.json shape.
type Config struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
}

// Path returns the cloud configuration path under dataDir.
func Path(dataDir string) string {
	return filepath.Join(dataDir, "cloud.json")
}

// Load reads cloud.json. A missing file is a valid zero-value configuration.
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

// Save persists cfg, creating dataDir when necessary.
func Save(dataDir string, cfg *Config) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(Path(dataDir), data, 0o644); err != nil {
		return err
	}
	return os.Chmod(Path(dataDir), 0o644)
}
