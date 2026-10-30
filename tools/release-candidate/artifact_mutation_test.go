package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// A real Git delegate changes an original only after admission, while Pi's
// source is materialized. This avoids timing races and production test hooks.
func artifactMutationGit(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "git.go")
	program := `package main
import (
 "os"
 "os/exec"
)
func main() {
 cmd := exec.Command(os.Getenv("ARTIFACT_TEST_REAL_GIT"), os.Args[1:]...)
 cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
 if err := cmd.Run(); err != nil {
  if exit, ok := err.(*exec.ExitError); ok { os.Exit(exit.ExitCode()) }
  os.Exit(70)
 }
 for _, arg := range os.Args[1:] {
  if arg == "--batch" {
   marker := os.Getenv("ARTIFACT_TEST_MUTATED")
   if _, err := os.Stat(marker); !os.IsNotExist(err) { os.Exit(71) }
   if os.WriteFile(os.Getenv("ARTIFACT_TEST_MUTATE"), []byte("tampered"), 0600) != nil { os.Exit(72) }
   if os.WriteFile(marker, []byte("mutated"), 0600) != nil { os.Exit(73) }
  }
 }
}
`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", filepath.Join(root, name), source).CombinedOutput(); err != nil {
		t.Fatalf("build mutation delegate: %v: %s", err, out)
	}
	return root, filepath.Join(root, "mutated")
}
