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

// Wrapper tests with a fake `engram`: bash on non-Windows, pwsh on Windows;
// PowerShell 5.1 is rejected in a separate Windows-only subtest.
func wrapperAbs(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(name)
	if err != nil {
		t.Fatal(err)
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

func fakeEngram(t *testing.T, dir, failProj string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		body := "@echo off\r\nset PROJ=\r\n:parse\r\nif \"%1\"==\"\" goto run\r\nif \"%1\"==\"--project\" (set PROJ=%~2& shift & shift & goto parse)\r\nshift\r\ngoto parse\r\n:run\r\necho stdout: syncing project=%PROJ%\r\necho stderr: project=%PROJ% 1>&2\r\n"
		if failProj != "" {
			body += "if \"%PROJ%\"==\"" + failProj + "\" (echo fake: forced failure for %PROJ% 1>&2 & exit 1)\r\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "engram.cmd"), []byte(body+"exit 0\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return
	}
	s := "#!/usr/bin/env bash\nproj=\"\"; while [ $# -gt 0 ]; do case \"$1\" in --project) proj=\"$2\"; shift 2 ;; *) shift ;; esac; done\nprintf 'stdout: syncing project=%s\\n' \"$proj\"; printf 'stderr: project=%s\\n' \"$proj\" >&2\n"
	if failProj != "" {
		s += fmt.Sprintf("if [ \"$proj\" = %q ]; then echo \"fake: forced failure for $proj\" >&2; exit 1; fi\n", failProj)
	}
	if err := os.WriteFile(filepath.Join(dir, "engram"), []byte(s+"exit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// run: controlled env (ENGRAM_CLOUD_SYNC_LOG removed case-insensitively for Windows, add appended, fakeDir on PATH); never returns a hard-coded log path.
func run(t *testing.T, interp, wrapper, fakeDir string, add, args []string) (int, string) {
	t.Helper()
	env := []string{}
	for _, e := range os.Environ() {
		k, v, ok := strings.Cut(e, "=")
		if !ok || strings.EqualFold(k, "ENGRAM_CLOUD_SYNC_LOG") || strings.EqualFold(k, "ENGRAM_DATA_DIR") {
			continue
		}
		if fakeDir != "" && strings.EqualFold(k, "PATH") {
			e = "PATH=" + fakeDir + string(os.PathListSeparator) + v
		}
		env = append(env, e)
	}
	env = append(env, add...)
	argv := append([]string{wrapper}, args...)
	if interp != "bash" {
		argv = append([]string{"-NoProfile", "-File", wrapper}, args...)
	}
	cmd := exec.Command(interp, argv...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), string(out)
	}
	if err != nil {
		t.Fatalf("run %s: %v; output:\n%s", interp, err, string(out))
	}
	return 0, string(out)
}

// wcase covers log-path precedence, failure aggregation, usage errors, and help in one table; envLog/explicitLog "" = unset/omitted.
type wcase struct {
	name                       string
	projects                   []string
	failProj                   string
	envLog, explicitLog        string
	wantExit                   int
	wantLogPath                string
	wantIn, wantLog, wantNotIn []string
}

func TestCloudSyncWrappers(t *testing.T) {
	type interp struct{ name, file, flag string }
	var interps []interp
	if runtime.GOOS != "windows" {
		if _, err := exec.LookPath("bash"); err == nil {
			interps = append(interps, interp{"bash", "cloud-sync-projects.sh", "--log"})
		}
	} else if p, err := exec.LookPath("pwsh"); err == nil {
		interps = append(interps, interp{p, "cloud-sync-projects.ps1", "-LogPath"})
	}
	for _, it := range interps {
		t.Run(it.file, func(t *testing.T) {
			wrapper := wrapperAbs(t, it.file)
			tmp := t.TempDir()
			fakeDir, dataDir := filepath.Join(tmp, "bin"), filepath.Join(tmp, "data")
			os.MkdirAll(fakeDir, 0o755)
			os.MkdirAll(dataDir, 0o755)
			defLog := filepath.Join(dataDir, "cloud-sync-projects.log")
			envLog, envLogUnused, explicitLog, pfLog := filepath.Join(tmp, "env.log"), filepath.Join(tmp, "env-unused.log"), filepath.Join(tmp, "explicit.log"), filepath.Join(tmp, "pf.log")
			cases := []wcase{
				{name: "DefaultLogPath", projects: []string{"alpha"}, wantExit: 0, wantLogPath: defLog, wantIn: []string{"stdout: syncing project=alpha", "stderr: project=alpha", "project SUCCESS project=alpha exit=0"}, wantLog: []string{"] project SUCCESS project=alpha exit=0", "stderr: project=alpha"}},
				{name: "EnvLogOverride", projects: []string{"beta"}, envLog: envLog, wantExit: 0, wantLogPath: envLog, wantIn: []string{"stdout: syncing project=beta", "project SUCCESS project=beta exit=0"}, wantLog: []string{"] project SUCCESS project=beta exit=0"}},
				{name: "ExplicitLogPrecedence", projects: []string{"gamma"}, envLog: envLogUnused, explicitLog: explicitLog, wantExit: 0, wantLogPath: explicitLog, wantIn: []string{"stdout: syncing project=gamma", "project SUCCESS project=gamma exit=0"}, wantLog: []string{"] project SUCCESS project=gamma exit=0"}},
				{name: "PartialFailureContinuesAggregate1", projects: []string{"good", "mid", "tail"}, failProj: "mid", envLog: pfLog, wantExit: 1, wantLogPath: pfLog, wantIn: []string{"project FAILURE project=mid exit=1", "project START project=tail", "wrapper END result=failure overall=1"}, wantLog: []string{"] project FAILURE project=mid exit=1"}},
				{name: "SpaceInProjectName", projects: []string{"my project"}, wantExit: 0, wantLogPath: defLog, wantIn: []string{"stdout: syncing project=my project", "project SUCCESS project=my project exit=0"}, wantLog: []string{"] project SUCCESS project=my project exit=0"}},
				{name: "MissingArgsUsage2", wantExit: 2, wantIn: []string{"at least one project is required"}},
				{name: "InvalidLogExits1", projects: []string{"alpha"}, explicitLog: dataDir, wantExit: 1, wantNotIn: []string{"stdout: syncing project=alpha"}},
			}
			if it.file == "cloud-sync-projects.sh" {
				cases = append(cases, wcase{name: "HelpExits0_-h", projects: []string{"-h"}, wantIn: []string{"Usage:"}}, wcase{name: "HelpExits0_--help", projects: []string{"--help"}, wantIn: []string{"Usage:"}})
			} else {
				cases = append(cases, wcase{name: "HelpExits0_-Help", projects: []string{"-Help"}, wantIn: []string{"Usage:"}}, wcase{name: "HelpExits0_--help", projects: []string{"--help"}, wantIn: []string{"Usage:"}}, wcase{name: "HelpExits0_-h", projects: []string{"-h"}, wantIn: []string{"Usage:"}})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					fakeEngram(t, fakeDir, tc.failProj)
					add := []string{"ENGRAM_DATA_DIR=" + dataDir}
					if tc.envLog != "" {
						add = append(add, "ENGRAM_CLOUD_SYNC_LOG="+tc.envLog)
					}
					args := tc.projects
					if tc.explicitLog != "" {
						args = append([]string{it.flag, tc.explicitLog}, args...)
					}
					exit, out := run(t, it.name, wrapper, fakeDir, add, args)
					if exit != tc.wantExit {
						t.Fatalf("exit=%d want %d; output:\n%s", exit, tc.wantExit, out)
					}
					assertContains(t, "console", out, tc.wantIn...)
					for _, n := range tc.wantNotIn {
						if strings.Contains(out, n) {
							t.Fatalf("console unexpectedly contains %q:\n%s", n, out)
						}
					}
					if tc.wantLogPath != "" {
						lb, rerr := os.ReadFile(tc.wantLogPath)
						if rerr != nil {
							t.Fatalf("read expected log %s: %v", tc.wantLogPath, rerr)
						}
						assertContains(t, "log", string(lb), tc.wantLog...)
						if tc.explicitLog != "" {
							if _, err := os.Stat(tc.envLog); err == nil {
								t.Fatalf("env log %s should not exist when explicit override used", tc.envLog)
							}
						}
					}
				})
			}
		})
	}
	// PowerShell 5.1 rejection (Windows-only; separate from the pwsh-only matrix): powershell.exe must exit 2 with the exact PS7-required diagnostic.
	if runtime.GOOS == "windows" {
		t.Run("PS5Rejection", func(t *testing.T) {
			ps, err := exec.LookPath("powershell.exe")
			if err != nil {
				t.Skip("powershell.exe not available")
			}
			exit, out := run(t, ps, wrapperAbs(t, "cloud-sync-projects.ps1"), "", nil, []string{"my-project"})
			if exit != 2 {
				t.Fatalf("exit=%d want 2; output:\n%s", exit, out)
			}
			if want := "PowerShell 7 (pwsh) is required"; !strings.Contains(out, want) {
				t.Fatalf("missing %q diagnostic:\n%s", want, out)
			}
		})
	}
}
