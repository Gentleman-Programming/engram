package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUnicodeProjectResolution(t *testing.T) {
	for _, name := range []string{"café", "cafe\u0301", " CAFÉ--API__ ", " CAFE\u0301--API__ "} {
		want := "café"
		if name[0] == ' ' {
			want = "café-api_"
		}
		for _, process := range []bool{false, true} {
			opts := ResolutionOptions{Mode: ResolutionCurrent, Explicit: name}
			if process {
				opts.Explicit = ""
				opts.ProcessOverride = name
			}
			got, err := Resolve(opts)
			if err != nil || got.Project != want {
				t.Fatalf("Resolve(%q, process=%t) = %+v, %v; want %q", name, process, got, err, want)
			}
		}
	}
}

func TestUnicodeProjectCaseComposition(t *testing.T) {
	for _, name := range []string{"\u0130", "I\u0307"} {
		got, err := Resolve(ResolutionOptions{Mode: ResolutionCurrent, Explicit: name})
		if err != nil || got.Project != "i" {
			t.Fatalf("Resolve(%q) = %+v, %v; want i", name, got, err)
		}
	}
}

func TestUnicodeLegacyRepositoryBinding(t *testing.T) {
	t.Setenv("ENGRAM_PROJECT", "")
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	bindingPath := filepath.Join(dir, ".git", "engram-project-identity.json")
	legacy := []byte("{\"version\":1,\"id\":\"0123456789abcdef0123456789abcdef\",\"project\":\"cafe\u0301\"}\n")
	if err := os.WriteFile(bindingPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	got := DetectProjectFull(dir)
	if got.Error != nil || got.Project != "café" {
		t.Fatalf("legacy detection = %+v, want NFC café", got)
	}
	after, err := os.ReadFile(bindingPath)
	if err != nil || string(after) != string(legacy) {
		t.Fatalf("legacy binding rewritten: %q, %v", after, err)
	}
}

func TestUnicodeProjectDetection(t *testing.T) {
	t.Setenv("ENGRAM_PROJECT", "")
	dir := filepath.Join(t.TempDir(), "cafe\u0301")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	got := DetectProjectFull(dir)
	if got.Error != nil || got.Project != "café" {
		t.Fatalf("detected %+v, want NFC café", got)
	}
}
