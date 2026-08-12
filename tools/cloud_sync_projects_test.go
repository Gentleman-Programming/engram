package tools_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Deterministic wrapper tests with a fake `engram` (no network, no real data
// dir). Bash runs non-Windows; pwsh runs Windows; otherwise skipped.
func wrapperAbs(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("wrapper not found at %s: %v", abs, err)
	}
	return abs
}
func assertContains(t *testing.T, label, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Fatalf("%s missing %q:\n%s", label, w, out)
		}
	}
}

// fakeEngram writes a fake `engram` to dir; echoes stdout+stderr, exits 0
// (failProj exits 1). Windows: .cmd; else: bash script.
func fakeEngram(t *testing.T, dir, failProj string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		body := "@echo off\r\nset PROJ=\r\n:parse\r\nif \"%1\"==\"\" goto run\r\nif \"%1\"==\"--project\" (set PROJ=%~2& shift & shift & goto parse)\r\nshift\r\ngoto parse\r\n:run\r\necho stdout: syncing project=%PROJ%\r\necho stderr: project=%PROJ% 1>&2\r\n"
		if failProj != "" {
			body += "if \"%PROJ%\"==\"" + failProj + "\" (echo fake: forced failure for %PROJ% 1>&2 & exit 1)\r\n"
		}
		body += "exit 0\r\n"
		if err := os.WriteFile(filepath.Join(dir, "engram.cmd"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return
	}
	s := `#!/usr/bin/env bash
proj=""; while [ $# -gt 0 ]; do case "$1" in --project) proj="$2"; shift 2 ;; *) shift ;; esac; done
printf 'stdout: syncing project=%s\n' "$proj"; printf 'stderr: project=%s\n' "$proj" >&2
`
	if failProj != "" {
		s += fmt.Sprintf("if [ \"$proj\" = %q ]; then echo \"fake: forced failure for $proj\" >&2; exit 1; fi\n", failProj)
	}
	s += "exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "engram"), []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
}

type wcase struct {
	name            string
	projects        []string
	failProj        string
	wantExit        int
	wantIn, wantLog []string
}

func run(t *testing.T, interp, wrapper, fakeDir, dataDir string, args ...string) (int, string, string) {
	t.Helper()
	var cmd *exec.Cmd
	env := os.Environ()
	env = append(env, "ENGRAM_DATA_DIR="+dataDir)
	if interp == "bash" {
		cmd = exec.Command("bash", append([]string{wrapper}, args...)...)
		env = append(env, "HOME="+t.TempDir())
	} else {
		cmd = exec.Command(interp, append([]string{"-NoProfile", "-File", wrapper}, args...)...)
		env = append(env, "USERPROFILE="+t.TempDir())
	}
	for i, e := range env {
		key, value, ok := strings.Cut(e, "=")
		if ok && strings.EqualFold(key, "PATH") {
			env[i] = "PATH=" + fakeDir + string(os.PathListSeparator) + value
			break
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	exit := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s: %v; output:\n%s", interp, err, string(out))
	}
	return exit, string(out), filepath.Join(dataDir, "cloud-sync-projects.log")
}
func TestCloudSyncWrappers(t *testing.T) {
	type interp struct{ name, file, flag string }
	var interps []interp
	if runtime.GOOS != "windows" {
		if _, err := exec.LookPath("bash"); err == nil {
			interps = append(interps, interp{"bash", "cloud-sync-projects.sh", "--log"})
		}
	} else {
		if p, err := exec.LookPath("pwsh"); err == nil {
			interps = append(interps, interp{p, "cloud-sync-projects.ps1", "-LogPath"})
		}
	}
	if len(interps) == 0 {
		t.Skip("no native wrapper interpreter available")
	}
	cases := []wcase{
		{name: "SuccessWithLogOverride", projects: []string{"alpha", "beta"}, wantExit: 0, wantIn: []string{"stdout: syncing project=alpha", "stderr: project=alpha", "project SUCCESS project=alpha exit=0", "wrapper END result=success"}, wantLog: []string{"] project SUCCESS project=alpha exit=0", "stderr: project=alpha"}},
		{name: "PartialFailureContinuesAggregate1", projects: []string{"good", "mid", "tail"}, failProj: "mid", wantExit: 1, wantIn: []string{"project FAILURE project=mid exit=1", "project START project=tail", "wrapper END result=failure overall=1"}, wantLog: []string{"] project FAILURE project=mid exit=1"}},
		{name: "SpaceInProjectName", projects: []string{"my project"}, wantExit: 0, wantIn: []string{"stdout: syncing project=my project", "project SUCCESS project=my project exit=0"}, wantLog: []string{"] project SUCCESS project=my project exit=0"}},
	}
	for _, it := range interps {
		t.Run(it.file, func(t *testing.T) {
			wrapper := wrapperAbs(t, it.file)
			tmp := t.TempDir()
			fakeDir, dataDir := filepath.Join(tmp, "bin"), filepath.Join(tmp, "data")
			for _, d := range []string{fakeDir, dataDir} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					fakeEngram(t, fakeDir, tc.failProj)
					args := append([]string{it.flag, filepath.Join(dataDir, "cloud-sync-projects.log")}, tc.projects...)
					exit, out, logPath := run(t, it.name, wrapper, fakeDir, dataDir, args...)
					if exit != tc.wantExit {
						t.Fatalf("exit=%d want %d; output:\n%s", exit, tc.wantExit, out)
					}
					assertContains(t, "console", out, tc.wantIn...)
					if lb, rerr := os.ReadFile(logPath); rerr != nil {
						t.Fatalf("read log: %v", rerr)
					} else {
						assertContains(t, "log", string(lb), tc.wantLog...)
					}
				})
			}
			t.Run("MissingArgsUsage2", func(t *testing.T) {
				fakeEngram(t, fakeDir, "")
				exit, out, _ := run(t, it.name, wrapper, fakeDir, dataDir)
				if exit != 2 {
					t.Fatalf("exit=%d want 2; output:\n%s", exit, out)
				}
				if !strings.Contains(out, "at least one project is required") {
					t.Fatalf("missing usage:\n%s", out)
				}
			})
			t.Run("InvalidLogExits1", func(t *testing.T) {
				fakeEngram(t, fakeDir, "")
				exit, out, _ := run(t, it.name, wrapper, fakeDir, dataDir, it.flag, dataDir, "alpha")
				if exit != 1 {
					t.Fatalf("exit=%d want 1; output:\n%s", exit, out)
				}
				if strings.Contains(out, "stdout: syncing project=alpha") {
					t.Fatalf("engram invoked despite invalid log:\n%s", out)
				}
			})
		})
	}
}
