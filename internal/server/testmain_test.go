package server

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if err := os.Setenv("GIT_CEILING_DIRECTORIES", composeGitCeiling(os.Getenv("GIT_CEILING_DIRECTORIES"))); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func composeGitCeiling(existing string) string {
	if existing == "" {
		return os.TempDir()
	}
	return os.TempDir() + string(os.PathListSeparator) + existing
}

func TestComposeGitCeiling(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		want     string
	}{
		{
			name: "empty inherited ceiling",
			want: os.TempDir(),
		},
		{
			name:     "non-empty inherited ceiling",
			existing: "C:\\existing",
			want:     os.TempDir() + string(os.PathListSeparator) + "C:\\existing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := composeGitCeiling(tt.existing); got != tt.want {
				t.Errorf("composeGitCeiling(%q) = %q, want %q", tt.existing, got, tt.want)
			}
		})
	}
}
