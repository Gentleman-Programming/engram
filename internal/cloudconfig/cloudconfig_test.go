package cloudconfig

import (
	"os"
	"testing"
)

func TestApplyServerOverride(t *testing.T) {
	t.Setenv(EnvCloudServer, " https://env-cloud.example.test/ ")

	config := ApplyServerOverride(&Config{ServerURL: "https://file-cloud.example.test"})
	if config.ServerURL != "https://env-cloud.example.test/" {
		t.Fatalf("server URL = %q, want environment override", config.ServerURL)
	}
}

func TestEffectiveTokenUsesEnvironmentBeforeFileWithoutRewritingFile(t *testing.T) {
	dataDir := t.TempDir()
	raw := []byte(`{"token":"file-token"}`)
	if err := os.WriteFile(Path(dataDir), raw, 0o600); err != nil {
		t.Fatalf("write cloud config: %v", err)
	}
	t.Setenv(EnvCloudToken, " environment-token ")

	token, source := EffectiveToken(dataDir)
	if token != "environment-token" || source != SourceEnv {
		t.Fatalf("effective token = %q from %v", token, source)
	}
	after, err := os.ReadFile(Path(dataDir))
	if err != nil {
		t.Fatalf("read cloud config after token resolution: %v", err)
	}
	if string(after) != string(raw) {
		t.Fatal("effective token resolution must not rewrite cloud config")
	}
}
