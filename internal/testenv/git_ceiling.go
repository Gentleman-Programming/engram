// Package testenv provides shared test-process environment setup.
package testenv

import "os"

const gitCeilingDirectories = "GIT_CEILING_DIRECTORIES"

// PrependTempDirGitCeiling prevents Git from discovering repositories above the
// OS temporary directory. It returns a function that restores the prior value.
func PrependTempDirGitCeiling() func() {
	previous, wasSet := os.LookupEnv(gitCeilingDirectories)
	ceiling := os.TempDir()
	if previous != "" {
		ceiling += string(os.PathListSeparator) + previous
	}
	if err := os.Setenv(gitCeilingDirectories, ceiling); err != nil {
		panic(err)
	}

	return func() {
		if wasSet {
			if err := os.Setenv(gitCeilingDirectories, previous); err != nil {
				panic(err)
			}
			return
		}
		if err := os.Unsetenv(gitCeilingDirectories); err != nil {
			panic(err)
		}
	}
}
