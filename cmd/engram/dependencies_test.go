package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLinuxARM64DependencyGraphExcludesAtottoClipboard(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate module root: runtime.Caller failed")
	}

	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./cmd/engram")
	cmd.Dir = filepath.Join(filepath.Dir(sourceFile), "..", "..")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list linux/arm64 command dependencies: %v\n%s", err, output)
	}

	for _, dependency := range strings.Fields(string(output)) {
		if dependency == "github.com/atotto/clipboard" {
			t.Fatal("linux/arm64 command dependency graph includes github.com/atotto/clipboard")
		}
	}
}
