package mcp

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	ceiling := os.TempDir()
	if existing := os.Getenv("GIT_CEILING_DIRECTORIES"); existing != "" {
		ceiling += string(os.PathListSeparator) + existing
	}
	if err := os.Setenv("GIT_CEILING_DIRECTORIES", ceiling); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
