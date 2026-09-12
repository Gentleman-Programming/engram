//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func replaceInitConfigParentForTest(t *testing.T, configDir, externalDir string) string {
	t.Helper()
	if err := os.Rename(configDir, configDir+"-moved"); err == nil {
		t.Fatal("renamed .engram while its stable handle was open")
	}
	if err := os.Symlink(externalDir, configDir); err == nil {
		t.Fatal("replaced .engram while its stable handle was open")
	}
	if _, err := os.Stat(filepath.Join(configDir, ".")); err != nil {
		t.Fatalf("stable .engram path unavailable after blocked replacement: %v", err)
	}
	return configDir
}
