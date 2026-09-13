package main

import (
	"os"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/cloudconfig"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

type cloudConfig = cloudconfig.Config

func loadCloudConfig(cfg store.Config) (*cloudConfig, error) {
	return cloudconfig.Load(cfg.DataDir)
}

func saveCloudConfig(cfg store.Config, config *cloudConfig) error {
	return cloudconfig.Save(cfg.DataDir, config)
}

func TestCmdCloudConfigClearPersistsEmptyValuesAndReportsOverrides(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://file-cloud.example.test", Token: "file-token"}); err != nil {
		t.Fatalf("seed cloud config: %v", err)
	}
	t.Setenv(cloudconfig.EnvCloudServer, "https://env-cloud.example.test")
	t.Setenv(cloudconfig.EnvCloudToken, "env-token")

	withArgs(t, "engram", "cloud", "config", "--clear")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud config clear = stdout %q stderr %q panic %v", stdout, stderr, recovered)
	}
	for _, want := range []string{
		"Persisted cloud server URL and token cleared",
		"ENGRAM_CLOUD_SERVER remains active",
		"ENGRAM_CLOUD_TOKEN remains active",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("cloud config clear missing %q: %q", want, stdout)
		}
	}

	persisted, err := loadCloudConfig(cfg)
	if err != nil {
		t.Fatalf("load cleared cloud config: %v", err)
	}
	if persisted.ServerURL != "" || persisted.Token != "" {
		t.Fatalf("persisted config = %+v, want empty values", persisted)
	}
}

func TestCmdCloudConfigClearReplacesMalformedPersistedConfig(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	if err := os.WriteFile(cloudconfig.Path(cfg.DataDir), []byte("{malformed-json"), 0o600); err != nil {
		t.Fatalf("write malformed cloud config: %v", err)
	}

	withArgs(t, "engram", "cloud", "config", "--clear")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud config clear = stdout %q stderr %q panic %v", stdout, stderr, recovered)
	}
	if !strings.Contains(stdout, "Persisted cloud server URL and token cleared") {
		t.Fatalf("cloud config clear output = %q", stdout)
	}

	persisted, err := loadCloudConfig(cfg)
	if err != nil {
		t.Fatalf("load cleared cloud config: %v", err)
	}
	if persisted.ServerURL != "" || persisted.Token != "" {
		t.Fatalf("persisted config = %+v, want empty values", persisted)
	}
}
