package remote

import (
	"fmt"
	"os"
	"time"
)

// CloudConfig holds the configuration for cloud connectivity.
type CloudConfig struct {
	ServerURL    string        `json:"server_url"`
	APIKey       string        `json:"api_key"`
	Mode         string        `json:"mode"`          // "cloud-only" | "local-sync"
	Project      string        `json:"project"`
	PushDebounce time.Duration `json:"push_debounce"` // default 10s
	PullInterval time.Duration `json:"pull_interval"` // default 120s
}

// Validate checks that all required fields are present and values are within range.
func (c CloudConfig) Validate() error {
	if c.ServerURL == "" {
		return fmt.Errorf("server_url is required")
	}
	if c.APIKey == "" {
		return fmt.Errorf("api_key is required")
	}
	if c.Mode != "cloud-only" && c.Mode != "local-sync" {
		return fmt.Errorf("invalid mode %q: must be \"cloud-only\" or \"local-sync\"", c.Mode)
	}
	if c.PushDebounce < time.Second {
		return fmt.Errorf("push_debounce must be >= 1s, got %v", c.PushDebounce)
	}
	if c.PullInterval < 10*time.Second {
		return fmt.Errorf("pull_interval must be >= 10s, got %v", c.PullInterval)
	}
	return nil
}

// ConfigStore is the interface for reading/writing cloud config from the local store.
// Implemented by *store.Store.
type ConfigStore interface {
	GetCloudConfig(key string) string
	SetCloudConfig(key, value string) error
}

// SaveToStore persists the config to the local store's sync_cloud_config table.
func SaveToStore(s ConfigStore, cfg CloudConfig) error {
	pairs := map[string]string{
		"server_url":    cfg.ServerURL,
		"api_key":       cfg.APIKey,
		"mode":          cfg.Mode,
		"project":       cfg.Project,
		"push_debounce": cfg.PushDebounce.String(),
		"pull_interval": cfg.PullInterval.String(),
	}
	for k, v := range pairs {
		if err := s.SetCloudConfig(k, v); err != nil {
			return fmt.Errorf("save config %s: %w", k, err)
		}
	}
	return nil
}

// LoadFromStore reads the config from the local store, applies env var overrides,
// and validates the result. Returns ErrConfigNotFound if no config exists.
func LoadFromStore(s ConfigStore) (CloudConfig, error) {
	serverURL := s.GetCloudConfig("server_url")
	if serverURL == "" {
		return CloudConfig{}, ErrConfigNotFound
	}

	cfg := CloudConfig{
		ServerURL: serverURL,
		APIKey:    s.GetCloudConfig("api_key"),
		Mode:      s.GetCloudConfig("mode"),
		Project:   s.GetCloudConfig("project"),
	}

	if d := s.GetCloudConfig("push_debounce"); d != "" {
		if parsed, err := time.ParseDuration(d); err == nil {
			cfg.PushDebounce = parsed
		}
	}
	if cfg.PushDebounce == 0 {
		cfg.PushDebounce = 10 * time.Second
	}

	if d := s.GetCloudConfig("pull_interval"); d != "" {
		if parsed, err := time.ParseDuration(d); err == nil {
			cfg.PullInterval = parsed
		}
	}
	if cfg.PullInterval == 0 {
		cfg.PullInterval = 120 * time.Second
	}

	// Env var overrides
	if v := os.Getenv("ENGRAM_CLOUD_URL"); v != "" {
		cfg.ServerURL = v
	}
	if v := os.Getenv("ENGRAM_CLOUD_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("ENGRAM_CLOUD_MODE"); v != "" {
		cfg.Mode = v
	}

	if err := cfg.Validate(); err != nil {
		return CloudConfig{}, err
	}

	return cfg, nil
}
