package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeCloudJSON(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "cloud.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write cloud.json: %v", err)
	}
}

func TestLoadCloudConfigReadsServerURLAndToken(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"server_url":"https://cloud.example.com","token":"file-token"}`)

	cc, err := loadCloudConfig(dir)
	if err != nil {
		t.Fatalf("loadCloudConfig: %v", err)
	}
	if cc.ServerURL != "https://cloud.example.com" {
		t.Fatalf("server_url = %q", cc.ServerURL)
	}
	if cc.Token != "file-token" {
		t.Fatalf("token = %q", cc.Token)
	}
}

func TestLoadCloudConfigMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()

	cc, err := loadCloudConfig(dir)
	if err != nil {
		t.Fatalf("loadCloudConfig: %v", err)
	}
	if cc.ServerURL != "" || cc.Token != "" {
		t.Fatalf("expected empty config, got %+v", cc)
	}
}

func TestTokenSourceEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"token":"file-token"}`)

	t.Setenv("ENGRAM_CLOUD_TOKEN", "env-token")
	if got := tokenSourceMessage(dir); got != TokenSourceEnv {
		t.Fatalf("source = %q, want %q", got, TokenSourceEnv)
	}
}

func TestTokenSourceFileFallback(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"token":"file-token"}`)

	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	if got := tokenSourceMessage(dir); got != TokenSourceFile {
		t.Fatalf("source = %q, want %q", got, TokenSourceFile)
	}
}

func TestTokenSourceNone(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	if got := tokenSourceMessage(dir); got != TokenSourceNone {
		t.Fatalf("source = %q, want %q", got, TokenSourceNone)
	}
}

func TestSaveCloudConfigWritesOnlyServerURLAndPreservesToken(t *testing.T) {
	dir := t.TempDir()
	writeCloudJSON(t, dir, `{"server_url":"https://old.example.com","token":"file-token"}`)

	if err := saveCloudConfig(dir, "https://new.example.com"); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "cloud.json"))
	if err != nil {
		t.Fatalf("read cloud.json: %v", err)
	}
	if !bytes.Contains(b, []byte(`"server_url": "https://new.example.com"`)) {
		t.Fatalf("server_url not updated in %s", string(b))
	}
	if !bytes.Contains(b, []byte(`"token": "file-token"`)) {
		t.Fatalf("token was not preserved in %s", string(b))
	}
}

func TestSaveCloudConfigDoesNotWriteToken(t *testing.T) {
	dir := t.TempDir()

	if err := saveCloudConfig(dir, "https://new.example.com"); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "cloud.json"))
	if err != nil {
		t.Fatalf("read cloud.json: %v", err)
	}
	if bytes.Contains(b, []byte(`"token"`)) {
		t.Fatalf("token must never be written by TUI, got %s", string(b))
	}
}

func TestValidateCloudServerURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"https ok", "https://cloud.example.com", "https://cloud.example.com", false},
		{"trims space", "  https://cloud.example.com  ", "https://cloud.example.com", false},
		{"missing scheme", "cloud.example.com", "", true},
		{"bad scheme", "ftp://cloud.example.com", "", true},
		{"missing host", "https://", "", true},
		{"query not allowed", "https://cloud.example.com?x=1", "", true},
		{"fragment not allowed", "https://cloud.example.com#x", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateCloudServerURL(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateCloudServerURL(%q) err = %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("validateCloudServerURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}


