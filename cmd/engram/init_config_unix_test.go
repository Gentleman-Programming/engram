//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"os"
	"testing"
)

func replaceInitConfigParentForTest(t *testing.T, configDir, externalDir string) string {
	t.Helper()
	movedDir := configDir + "-moved"
	if err := os.Rename(configDir, movedDir); err != nil {
		t.Fatalf("move .engram after stable descriptor open: %v", err)
	}
	if err := os.Symlink(externalDir, configDir); err != nil {
		t.Fatalf("replace .engram with symlink: %v", err)
	}
	return movedDir
}
