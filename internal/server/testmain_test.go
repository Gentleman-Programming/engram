package server

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const currentProjectGitEchoDirectory = "ENGRAM_TEST_CURRENT_PROJECT_GIT_ECHO_DIRECTORY"

func TestMain(m *testing.M) {
	if name := filepath.Base(os.Args[0]); name == "git" || name == "git.exe" {
		if address := os.Getenv(currentProjectGitFixture); address != "" {
			released := waitForCurrentProjectGitRelease(address)
			if released && os.Getenv(currentProjectGitEchoDirectory) == "1" && len(os.Args) == 5 &&
				os.Args[1] == "-C" && os.Args[3] == "rev-parse" && os.Args[4] == "--show-toplevel" {
				fmt.Println(os.Args[2])
				os.Exit(0)
			}
			os.Exit(1)
		}
	}
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
