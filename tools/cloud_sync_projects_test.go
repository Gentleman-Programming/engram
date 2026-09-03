package tools_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

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

type fakeFailure struct {
	project string
	phase   string
}

func fakeEngram(t *testing.T, dir string, fail fakeFailure) {
	t.Helper()
	if runtime.GOOS == "windows" {
		body := "@echo off\r\nset \"ARGS=%*\"\r\nset PROJ=\r\nset IMPORT=0\r\n:parse\r\nif \"%1\"==\"\" goto run\r\nif \"%1\"==\"--import\" (set IMPORT=1& shift & goto parse)\r\nif \"%1\"==\"--project\" (set PROJ=%~2& shift & shift & goto parse)\r\nshift\r\ngoto parse\r\n:run\r\nset PHASE=export\r\nif \"%IMPORT%\"==\"1\" set PHASE=import\r\nif not \"%FAKE_ENGRAM_ARGS_LOG%\"==\"\" echo %ARGS%>>\"%FAKE_ENGRAM_ARGS_LOG%\"\r\necho stdout: %PHASE% project=%PROJ%\r\necho stderr: %PHASE% project=%PROJ% 1>&2\r\n"
		if fail.project != "" {
			body += "if \"%PROJ%\"==\"" + fail.project + "\" if \"%PHASE%\"==\"" + fail.phase + "\" (echo fake: forced " + fail.phase + " failure for %PROJ% 1>&2 & exit 1)\r\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "engram.cmd"), []byte(body+"exit 0\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return
	}
	s := "#!/usr/bin/env bash\nargv=\"$*\"\nproj=\"\"\nphase=export\nwhile [ $# -gt 0 ]; do case \"$1\" in --import) phase=import; shift ;; --project) proj=\"$2\"; shift 2 ;; *) shift ;; esac; done\nif [ -n \"${FAKE_ENGRAM_ARGS_LOG:-}\" ]; then printf '%s\\n' \"$argv\" >>\"$FAKE_ENGRAM_ARGS_LOG\"; fi\nprintf 'stdout: %s project=%s\\n' \"$phase\" \"$proj\"; printf 'stderr: %s project=%s\\n' \"$phase\" \"$proj\" >&2\n"
	if fail.project != "" {
		s += fmt.Sprintf("if [ \"$proj\" = %q ] && [ \"$phase\" = %q ]; then echo \"fake: forced %s failure for $proj\" >&2; exit 1; fi\n", fail.project, fail.phase, fail.phase)
	}
	if err := os.WriteFile(filepath.Join(dir, "engram"), []byte(s+"exit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
func run(t *testing.T, interp, wrapper, fakeDir string, add, args []string) (int, string) {
	t.Helper()
	pathOverride := false
	for _, e := range add {
		key, _, _ := strings.Cut(e, "=")
		pathOverride = pathOverride || strings.EqualFold(key, "PATH")
	}
	env := []string{}
	for _, e := range os.Environ() {
		k, v, ok := strings.Cut(e, "=")
		if !ok || strings.EqualFold(k, "ENGRAM_CLOUD_SYNC_LOG") || strings.EqualFold(k, "ENGRAM_DATA_DIR") {
			continue
		}
		if pathOverride && strings.EqualFold(k, "PATH") {
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

type wcase struct {
	name                                 string
	projects                             []string
	fail                                 fakeFailure
	envLog, explicitLog, wantLogPath     string
	wantExit                             int
	wantIn, wantLog, wantNotIn, wantArgs []string
}

func assertInvocationOrder(t *testing.T, path string, want []string) {
	t.Helper()
	if len(want) == 0 {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected CLI invocation log %s", path)
		}
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CLI invocation log: %v", err)
	}
	got := strings.FieldsFunc(string(b), func(r rune) bool { return r == '\r' || r == '\n' })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CLI argv sequence = %#v, want %#v", got, want)
	}
}

func TestCloudSyncWrappers(t *testing.T) {
	type interp struct{ name, file, flag string }
	var interps []interp
	if runtime.GOOS != "windows" {
		if _, err := exec.LookPath("bash"); err != nil {
			t.Fatal("bash is required to test cloud-sync-projects.sh")
		}
		interps = append(interps, interp{"bash", "cloud-sync-projects.sh", "--log"})
	} else if p, err := exec.LookPath("pwsh"); err != nil {
		t.Fatal("pwsh is required to test cloud-sync-projects.ps1")
	} else {
		interps = append(interps, interp{p, "cloud-sync-projects.ps1", "-LogPath"})
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
			defLog := filepath.Join(dataDir, "cloud-sync-projects.log")
			envLog, envLogUnused, explicitLog, pfLog := filepath.Join(tmp, "env.log"), filepath.Join(tmp, "env-unused.log"), filepath.Join(tmp, "explicit.log"), filepath.Join(tmp, "pf.log")
			argvLog := filepath.Join(tmp, "engram-argv.log")
			spaceArgs := []string{"sync --cloud --project my project", "sync --cloud --import --project my project"}
			if runtime.GOOS == "windows" {
				spaceArgs = []string{"sync --cloud --project \"my project\"", "sync --cloud --import --project \"my project\""}
			}
			cases := []wcase{
				{name: "DefaultLogPath", projects: []string{"alpha"}, wantExit: 0, wantLogPath: defLog, wantIn: []string{"stdout: export project=alpha", "stdout: import project=alpha", "phase=import SUCCESS project=alpha exit=0", "project SUCCESS project=alpha"}, wantLog: []string{"] phase=export SUCCESS project=alpha exit=0", "stderr: import project=alpha", "] project SUCCESS project=alpha"}, wantArgs: []string{"sync --cloud --project alpha", "sync --cloud --import --project alpha"}},
				{name: "EnvLogOverride", projects: []string{"beta"}, envLog: envLog, wantExit: 0, wantLogPath: envLog, wantIn: []string{"stdout: export project=beta", "phase=import SUCCESS project=beta exit=0"}, wantLog: []string{"] phase=import SUCCESS project=beta exit=0"}, wantArgs: []string{"sync --cloud --project beta", "sync --cloud --import --project beta"}},
				{name: "ExplicitLogPrecedence", projects: []string{"gamma"}, envLog: envLogUnused, explicitLog: explicitLog, wantExit: 0, wantLogPath: explicitLog, wantIn: []string{"stdout: export project=gamma", "phase=import SUCCESS project=gamma exit=0"}, wantLog: []string{"] phase=import SUCCESS project=gamma exit=0"}, wantArgs: []string{"sync --cloud --project gamma", "sync --cloud --import --project gamma"}},
				{name: "ExportFailureSkipsImportAndContinues", projects: []string{"good", "mid", "tail"}, fail: fakeFailure{project: "mid", phase: "export"}, envLog: pfLog, wantExit: 1, wantLogPath: pfLog, wantIn: []string{"phase=export FAILURE project=mid exit=1", "project FAILURE project=mid phase=export exit=1", "project START project=tail", "wrapper END result=failure overall=1"}, wantLog: []string{"] phase=export FAILURE project=mid exit=1", "] project FAILURE project=mid phase=export exit=1"}, wantArgs: []string{"sync --cloud --project good", "sync --cloud --import --project good", "sync --cloud --project mid", "sync --cloud --project tail", "sync --cloud --import --project tail"}},
				{name: "ImportFailureReachesAggregateResult", projects: []string{"good", "mid", "tail"}, fail: fakeFailure{project: "mid", phase: "import"}, envLog: pfLog, wantExit: 1, wantLogPath: pfLog, wantIn: []string{"phase=import FAILURE project=mid exit=1", "project FAILURE project=mid phase=import exit=1", "project START project=tail", "wrapper END result=failure overall=1"}, wantLog: []string{"] phase=import FAILURE project=mid exit=1", "] project FAILURE project=mid phase=import exit=1"}, wantArgs: []string{"sync --cloud --project good", "sync --cloud --import --project good", "sync --cloud --project mid", "sync --cloud --import --project mid", "sync --cloud --project tail", "sync --cloud --import --project tail"}},
				{name: "SpaceInProjectName", projects: []string{"my project"}, wantExit: 0, wantLogPath: defLog, wantIn: []string{"stdout: export project=my project", "phase=import SUCCESS project=my project exit=0"}, wantLog: []string{"] phase=import SUCCESS project=my project exit=0"}, wantArgs: spaceArgs},
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
					if err := os.Remove(argvLog); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					fakeEngram(t, fakeDir, tc.fail)
					add := []string{"ENGRAM_DATA_DIR=" + dataDir, "FAKE_ENGRAM_ARGS_LOG=" + argvLog}
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
					assertInvocationOrder(t, argvLog, tc.wantArgs)
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

func TestCloudSyncPowerShellMissingEngram(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell wrapper test is Windows-only")
	}
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Fatal("pwsh is required to test cloud-sync-projects.ps1")
	}
	tmp := t.TempDir()
	bin, data := filepath.Join(tmp, "bin"), filepath.Join(tmp, "data")
	for _, dir := range []string{bin, data} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exit, out := run(t, pwsh, wrapperAbs(t, "cloud-sync-projects.ps1"), bin, []string{"PATH=" + bin, "ENGRAM_DATA_DIR=" + data}, []string{"alpha"})
	if exit != 1 {
		t.Fatalf("exit=%d want 1; output:\n%s", exit, out)
	}
	assertContains(t, "console", out, "engram executable not found", "project FAILURE project=alpha phase=export exit=-1", "wrapper END result=failure overall=1")
	log, err := os.ReadFile(filepath.Join(data, "cloud-sync-projects.log"))
	if err != nil {
		t.Fatalf("read durable log: %v", err)
	}
	assertContains(t, "log", string(log), "project FAILURE project=alpha phase=export exit=-1")
	for _, args := range [][]string{{"-Help"}, {}} {
		exit, out = run(t, pwsh, wrapperAbs(t, "cloud-sync-projects.ps1"), bin, []string{"PATH=" + bin, "ENGRAM_DATA_DIR=" + data}, args)
		if exit != map[bool]int{true: 0, false: 2}[len(args) > 0] {
			t.Fatalf("args=%q exit=%d; output:\n%s", args, exit, out)
		}
		if len(args) > 0 {
			assertContains(t, "help", out, "Usage:")
		} else {
			assertContains(t, "usage", out, "at least one project is required")
		}
	}
}
