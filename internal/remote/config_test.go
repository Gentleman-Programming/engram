package remote

import (
	"errors"
	"testing"
	"time"
)

func TestValidate_Valid(t *testing.T) {
	cfg := CloudConfig{
		ServerURL:    "https://cloud.example.com",
		APIKey:       "tok-abc",
		Mode:         "cloud-only",
		Project:      "myapp",
		PushDebounce: 10 * time.Second,
		PullInterval: 120 * time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidate_LocalSyncMode(t *testing.T) {
	cfg := CloudConfig{
		ServerURL:    "https://cloud.example.com",
		APIKey:       "tok-abc",
		Mode:         "local-sync",
		Project:      "myapp",
		PushDebounce: 10 * time.Second,
		PullInterval: 120 * time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidate_EmptyServerURL(t *testing.T) {
	cfg := CloudConfig{APIKey: "tok", Mode: "cloud-only", PushDebounce: 10 * time.Second, PullInterval: 120 * time.Second}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty ServerURL")
	}
}

func TestValidate_EmptyAPIKey(t *testing.T) {
	cfg := CloudConfig{ServerURL: "https://x.com", Mode: "cloud-only", PushDebounce: 10 * time.Second, PullInterval: 120 * time.Second}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty APIKey")
	}
}

func TestValidate_InvalidMode(t *testing.T) {
	cfg := CloudConfig{
		ServerURL:    "https://x.com",
		APIKey:       "tok",
		Mode:         "auto",
		PushDebounce: 10 * time.Second,
		PullInterval: 120 * time.Second,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if !containsStr(err.Error(), "invalid mode") {
		t.Fatalf("expected error to contain 'invalid mode', got %q", err.Error())
	}
}

func TestValidate_PushDebounceTooLow(t *testing.T) {
	cfg := CloudConfig{
		ServerURL:    "https://x.com",
		APIKey:       "tok",
		Mode:         "local-sync",
		PushDebounce: 500 * time.Millisecond,
		PullInterval: 120 * time.Second,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for PushDebounce < 1s")
	}
}

func TestValidate_PullIntervalTooLow(t *testing.T) {
	cfg := CloudConfig{
		ServerURL:    "https://x.com",
		APIKey:       "tok",
		Mode:         "local-sync",
		PushDebounce: 10 * time.Second,
		PullInterval: 5 * time.Second,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for PullInterval < 10s")
	}
}

// T05 tests — LoadFromStore / SaveToStore

func TestSaveAndLoadConfig(t *testing.T) {
	store := newTestConfigStore(t)

	cfg := CloudConfig{
		ServerURL:    "https://cloud.example.com",
		APIKey:       "tok-abc123",
		Mode:         "local-sync",
		Project:      "engram",
		PushDebounce: 15 * time.Second,
		PullInterval: 60 * time.Second,
	}

	if err := SaveToStore(store, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadFromStore(store)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.ServerURL != cfg.ServerURL {
		t.Fatalf("ServerURL: expected %q, got %q", cfg.ServerURL, loaded.ServerURL)
	}
	if loaded.APIKey != cfg.APIKey {
		t.Fatalf("APIKey: expected %q, got %q", cfg.APIKey, loaded.APIKey)
	}
	if loaded.Mode != cfg.Mode {
		t.Fatalf("Mode: expected %q, got %q", cfg.Mode, loaded.Mode)
	}
	if loaded.Project != cfg.Project {
		t.Fatalf("Project: expected %q, got %q", cfg.Project, loaded.Project)
	}
	if loaded.PushDebounce != cfg.PushDebounce {
		t.Fatalf("PushDebounce: expected %v, got %v", cfg.PushDebounce, loaded.PushDebounce)
	}
	if loaded.PullInterval != cfg.PullInterval {
		t.Fatalf("PullInterval: expected %v, got %v", cfg.PullInterval, loaded.PullInterval)
	}
}

func TestSaveOverwriteConfig(t *testing.T) {
	store := newTestConfigStore(t)

	cfg1 := CloudConfig{ServerURL: "https://old.com", APIKey: "old-key", Mode: "cloud-only", PushDebounce: 10 * time.Second, PullInterval: 120 * time.Second}
	SaveToStore(store, cfg1)

	cfg2 := CloudConfig{ServerURL: "https://new.com", APIKey: "new-key", Mode: "local-sync", PushDebounce: 20 * time.Second, PullInterval: 60 * time.Second}
	SaveToStore(store, cfg2)

	loaded, _ := LoadFromStore(store)
	if loaded.APIKey != "new-key" {
		t.Fatalf("expected new-key, got %q", loaded.APIKey)
	}
	if loaded.ServerURL != "https://new.com" {
		t.Fatalf("expected https://new.com, got %q", loaded.ServerURL)
	}
}

func TestLoadFromStore_Empty(t *testing.T) {
	store := newTestConfigStore(t)

	_, err := LoadFromStore(store)
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("expected ErrConfigNotFound, got %v", err)
	}
}

// T06 tests — env var overrides

func TestLoadFromStore_EnvOverrides(t *testing.T) {
	store := newTestConfigStore(t)

	cfg := CloudConfig{ServerURL: "https://stored.com", APIKey: "stored-key", Mode: "cloud-only", Project: "proj", PushDebounce: 10 * time.Second, PullInterval: 120 * time.Second}
	SaveToStore(store, cfg)

	t.Setenv("ENGRAM_CLOUD_URL", "https://env.example.com")
	t.Setenv("ENGRAM_CLOUD_KEY", "env-key")
	t.Setenv("ENGRAM_CLOUD_MODE", "local-sync")

	loaded, err := LoadFromStore(store)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ServerURL != "https://env.example.com" {
		t.Fatalf("expected env URL, got %q", loaded.ServerURL)
	}
	if loaded.APIKey != "env-key" {
		t.Fatalf("expected env key, got %q", loaded.APIKey)
	}
	if loaded.Mode != "local-sync" {
		t.Fatalf("expected local-sync, got %q", loaded.Mode)
	}
}

func TestLoadFromStore_InvalidEnvMode(t *testing.T) {
	store := newTestConfigStore(t)

	cfg := CloudConfig{ServerURL: "https://x.com", APIKey: "tok", Mode: "cloud-only", PushDebounce: 10 * time.Second, PullInterval: 120 * time.Second}
	SaveToStore(store, cfg)

	t.Setenv("ENGRAM_CLOUD_MODE", "invalid-mode")

	_, err := LoadFromStore(store)
	if err == nil {
		t.Fatal("expected validation error for invalid env mode")
	}
}

// helpers

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && searchStr(s, sub)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
