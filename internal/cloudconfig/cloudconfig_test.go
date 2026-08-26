package cloudconfig

import (
	"os"
	"runtime"
	"testing"
)

func TestSaveNormalizesExistingFileMode(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(Path(dataDir), []byte(`{"server_url":"https://old.example.test"}`), 0o600); err != nil {
		t.Fatalf("seed cloud config: %v", err)
	}
	if err := os.Chmod(Path(dataDir), 0o600); err != nil {
		t.Fatalf("restrict cloud config mode: %v", err)
	}

	if err := Save(dataDir, &Config{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	info, err := os.Stat(Path(dataDir))
	if err != nil {
		t.Fatalf("stat cloud config: %v", err)
	}
	if runtime.GOOS == "windows" {
		return // Windows does not expose POSIX permission bits through os.FileMode.
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("cloud config mode = %o, want 644", got)
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
