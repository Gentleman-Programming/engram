package testenv

import (
	"os"
	"testing"
)

func TestPrependTempDirGitCeiling(t *testing.T) {
	original, originallySet := os.LookupEnv(gitCeilingDirectories)

	t.Run("sets and unsets the ceiling when it was absent", func(t *testing.T) {
		t.Setenv(gitCeilingDirectories, "")
		if err := os.Unsetenv(gitCeilingDirectories); err != nil {
			t.Fatal(err)
		}

		restore := PrependTempDirGitCeiling()
		if got := os.Getenv(gitCeilingDirectories); got != os.TempDir() {
			t.Fatalf("GIT_CEILING_DIRECTORIES = %q, want %q", got, os.TempDir())
		}
		restore()

		if _, ok := os.LookupEnv(gitCeilingDirectories); ok {
			t.Fatal("GIT_CEILING_DIRECTORIES remained set after restore")
		}
	})

	if got, isSet := os.LookupEnv(gitCeilingDirectories); isSet != originallySet || got != original {
		t.Fatalf("GIT_CEILING_DIRECTORIES after subtest = (%q, %t), want (%q, %t)", got, isSet, original, originallySet)
	}

	t.Run("prepends and restores an existing ceiling", func(t *testing.T) {
		previous := "existing-ceiling"
		t.Setenv(gitCeilingDirectories, previous)

		restore := PrependTempDirGitCeiling()
		want := os.TempDir() + string(os.PathListSeparator) + previous
		if got := os.Getenv(gitCeilingDirectories); got != want {
			t.Fatalf("GIT_CEILING_DIRECTORIES = %q, want %q", got, want)
		}
		restore()

		if got := os.Getenv(gitCeilingDirectories); got != previous {
			t.Fatalf("GIT_CEILING_DIRECTORIES after restore = %q, want %q", got, previous)
		}
	})
}
