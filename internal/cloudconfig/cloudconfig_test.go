package cloudconfig

import (
	"os"
	"runtime"
	"testing"
)

func TestSaveKeepsCloudConfigPrivate(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(Path(dataDir), []byte(`{"server_url":"https://old.example.test"}`), 0o644); err != nil {
		t.Fatalf("seed cloud config: %v", err)
	}
	if err := os.Chmod(Path(dataDir), 0o644); err != nil {
		t.Fatalf("make cloud config permissive: %v", err)
	}

	if err := Save(dataDir, &Config{ServerURL: "https://cloud.example.test", Token: "secret-token"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	info, err := os.Stat(Path(dataDir))
	if err != nil {
		t.Fatalf("stat cloud config: %v", err)
	}
	if runtime.GOOS == "windows" {
		return // Windows does not expose POSIX permission bits through os.FileMode.
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("cloud config mode = %o, want 600", got)
	}
}

func TestValidateServerURLPreservesTrailingSlash(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "root slash", input: "https://cloud.example.test/", want: "https://cloud.example.test/"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ValidateServerURL(test.input)
			if err != nil {
				t.Fatalf("validate server URL: %v", err)
			}
			if got != test.want {
				t.Fatalf("validated URL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestEffectiveTokenTrimsSurroundingWhitespaceWithoutRewritingFile(t *testing.T) {
	t.Run("environment token", func(t *testing.T) {
		t.Setenv(EnvCloudToken, " \n environment-token \t")

		token, source := EffectiveToken(t.TempDir())
		if token != "environment-token" {
			t.Fatal("expected effective environment token to be trimmed")
		}
		if source != SourceEnv {
			t.Fatalf("token source = %v, want environment", source)
		}
	})

	t.Run("file token", func(t *testing.T) {
		dataDir := t.TempDir()
		raw := []byte(`{"token":" \n file-token \t"}`)
		if err := os.WriteFile(Path(dataDir), raw, 0o600); err != nil {
			t.Fatalf("write cloud config: %v", err)
		}

		token, source := EffectiveToken(dataDir)
		if token != "file-token" {
			t.Fatal("expected effective file token to be trimmed")
		}
		if source != SourceFile {
			t.Fatalf("token source = %v, want file", source)
		}
		after, err := os.ReadFile(Path(dataDir))
		if err != nil {
			t.Fatalf("read cloud config after token resolution: %v", err)
		}
		if string(after) != string(raw) {
			t.Fatal("effective token resolution must not rewrite cloud config")
		}
	})
}
