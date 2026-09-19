package server

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"
)

// fakeFileInfo is a minimal os.FileInfo so the injectable Stat/SameFile seams
// can be exercised without touching the real filesystem.
type fakeFileInfo struct {
	name string
	ino  uint64
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

// hermeticSelfCheckDeps stubs every seam, so a test only overrides the scenario
// it cares about and nothing reaches PATH, the filesystem or a real binary.
func hermeticSelfCheckDeps() SelfCheckDeps {
	return SelfCheckDeps{
		Getenv:       func(string) string { return "" },
		LookPath:     func(string) (string, error) { return "/usr/local/bin/engram", nil },
		Executable:   func() (string, error) { return "/usr/local/bin/engram", nil },
		Stat:         func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		SameFile:     func(os.FileInfo, os.FileInfo) bool { return false },
		EvalSymlinks: func(string) (string, error) { return "", os.ErrNotExist },
		RunVersion:   func(context.Context, string) (string, error) { return "", errors.New("no candidate") },
	}
}

func versionRunner(onDisk string) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return onDisk, nil }
}

// The two paths a real install exposes: the running executable as the kernel
// reports it and the PATH entry a supervisor would use.
const (
	ownBinaryPath       = "/opt/homebrew/Cellar/engram/2.0.0/bin/engram"
	candidateBinaryPath = "/opt/homebrew/bin/engram"
)

// sameFileDeps models an in-place replacement: the candidate and the running
// executable are the same file on disk under different paths. os.EvalSymlinks
// stays failing, so only os.SameFile can make the restart actionable.
func sameFileDeps(onDisk string) SelfCheckDeps {
	deps := hermeticSelfCheckDeps()
	deps.Executable = func() (string, error) { return ownBinaryPath, nil }
	deps.LookPath = func(string) (string, error) { return candidateBinaryPath, nil }
	deps.RunVersion = versionRunner(onDisk)
	info := fakeFileInfo{name: "engram", ino: 7}
	deps.Stat = func(string) (os.FileInfo, error) { return info, nil }
	deps.SameFile = func(a, b os.FileInfo) bool {
		left, okLeft := a.(fakeFileInfo)
		right, okRight := b.(fakeFileInfo)
		return okLeft && okRight && left.ino == right.ino
	}
	return deps
}

// resolvedPathDeps models a symlinked install: two different paths that may or
// may not resolve to the same file. os.SameFile is stubbed to false so only the
// resolved-path comparison can make the restart actionable.
func resolvedPathDeps(onDisk, ownTarget, candidateTarget string) SelfCheckDeps {
	deps := hermeticSelfCheckDeps()
	deps.Executable = func() (string, error) { return ownBinaryPath, nil }
	deps.LookPath = func(string) (string, error) { return candidateBinaryPath, nil }
	deps.RunVersion = versionRunner(onDisk)
	deps.Stat = func(string) (os.FileInfo, error) { return fakeFileInfo{name: "engram", ino: 1}, nil }
	deps.EvalSymlinks = func(path string) (string, error) {
		switch path {
		case ownBinaryPath:
			return ownTarget, nil
		case candidateBinaryPath:
			return candidateTarget, nil
		}
		return "", os.ErrNotExist
	}
	return deps
}

func TestCheckStaleBinaryDecisionTable(t *testing.T) {
	tests := []struct {
		name    string
		current string
		deps    SelfCheckDeps
		want    StaleBinaryReport
	}{
		{
			name:    "equal versions are current",
			current: "2.0.0",
			deps: func() SelfCheckDeps {
				deps := hermeticSelfCheckDeps()
				deps.RunVersion = versionRunner("engram 2.0.0\n")
				return deps
			}(),
			want: StaleBinaryReport{Known: true, Running: "2.0.0", OnDisk: "2.0.0", Candidate: "/usr/local/bin/engram"},
		},
		{
			name:    "a v prefix on the on-disk version is not stale",
			current: "2.0.0",
			deps: func() SelfCheckDeps {
				deps := hermeticSelfCheckDeps()
				deps.RunVersion = versionRunner("engram v2.0.0\n")
				return deps
			}(),
			want: StaleBinaryReport{Known: true, Running: "2.0.0", OnDisk: "v2.0.0", Candidate: "/usr/local/bin/engram"},
		},
		{
			name:    "our own file removed by the upgrade is actionable",
			current: "2.0.0",
			deps:    hermeticSelfCheckDepsWithVersion("engram 2.1.0\n"),
			want: StaleBinaryReport{
				Known:      true,
				Stale:      true,
				Actionable: true,
				Running:    "2.0.0",
				OnDisk:     "2.1.0",
				Candidate:  "/usr/local/bin/engram",
			},
		},
		{
			name:    "a candidate replaced in place is actionable",
			current: "2.0.0",
			deps:    sameFileDeps("engram 2.1.0\n"),
			want: StaleBinaryReport{
				Known:      true,
				Stale:      true,
				Actionable: true,
				Running:    "2.0.0",
				OnDisk:     "2.1.0",
				Candidate:  candidateBinaryPath,
			},
		},
		{
			name:    "the same file reached through a symlink is actionable",
			current: "2.0.0",
			deps:    resolvedPathDeps("engram 2.1.0\n", "/opt/homebrew/Cellar/engram/2.1.0/bin/engram", "/opt/homebrew/Cellar/engram/2.1.0/bin/engram"),
			want: StaleBinaryReport{
				Known:      true,
				Stale:      true,
				Actionable: true,
				Running:    "2.0.0",
				OnDisk:     "2.1.0",
				Candidate:  candidateBinaryPath,
			},
		},
		{
			name:    "a different install only reports",
			current: "2.0.0",
			deps:    resolvedPathDeps("engram 2.1.0\n", "/opt/running/2.0.0/engram", "/opt/other/2.1.0/engram"),
			want: StaleBinaryReport{
				Known:     true,
				Stale:     true,
				Running:   "2.0.0",
				OnDisk:    "2.1.0",
				Candidate: candidateBinaryPath,
			},
		},
		{
			name:    "an unresolvable candidate is unknown",
			current: "2.0.0",
			deps: func() SelfCheckDeps {
				deps := hermeticSelfCheckDeps()
				deps.LookPath = func(string) (string, error) { return "", errors.New("not found in PATH") }
				return deps
			}(),
			want: StaleBinaryReport{},
		},
		{
			name:    "unparsable output is unknown",
			current: "2.0.0",
			deps:    hermeticSelfCheckDepsWithVersion("engram\n"),
			want:    StaleBinaryReport{},
		},
		{
			name:    "empty output is unknown",
			current: "2.0.0",
			deps:    hermeticSelfCheckDepsWithVersion(""),
			want:    StaleBinaryReport{},
		},
		{
			name:    "a failing runner is unknown",
			current: "2.0.0",
			deps: func() SelfCheckDeps {
				deps := hermeticSelfCheckDeps()
				deps.RunVersion = func(context.Context, string) (string, error) { return "", errors.New("exec failed") }
				return deps
			}(),
			want: StaleBinaryReport{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckStaleBinary(context.Background(), tt.current, tt.deps)
			if got != tt.want {
				t.Fatalf("CheckStaleBinary() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func hermeticSelfCheckDepsWithVersion(onDisk string) SelfCheckDeps {
	deps := hermeticSelfCheckDeps()
	deps.RunVersion = versionRunner(onDisk)
	return deps
}

func TestCheckStaleBinaryRunnerTimeoutIsUnknown(t *testing.T) {
	deps := hermeticSelfCheckDeps()
	deps.VersionTimeout = 20 * time.Millisecond
	deps.RunVersion = func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}

	start := time.Now()
	got := CheckStaleBinary(context.Background(), "2.0.0", deps)
	elapsed := time.Since(start)

	if got != (StaleBinaryReport{}) {
		t.Fatalf("a timed out probe must stay unknown, got %+v", got)
	}
	if elapsed >= time.Second {
		t.Fatalf("probe took %s, want the injected VersionTimeout to bound it", elapsed)
	}
}

func TestCheckStaleBinaryIgnoresUnusableRunningVersions(t *testing.T) {
	tests := []struct {
		name    string
		current string
	}{
		{name: "dev build", current: "dev"},
		{name: "empty version", current: ""},
		{name: "blank version", current: "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probed := false
			deps := sameFileDeps("engram 2.1.0\n")
			deps.RunVersion = func(context.Context, string) (string, error) {
				probed = true
				return "engram 2.1.0\n", nil
			}

			got := CheckStaleBinary(context.Background(), tt.current, deps)
			if got != (StaleBinaryReport{}) {
				t.Fatalf("running version %q must never act, got %+v", tt.current, got)
			}
			if probed {
				t.Fatal("an unusable running version must not even spawn the candidate probe")
			}
		})
	}
}

func TestCheckStaleBinaryPrefersAbsoluteBinaryEnv(t *testing.T) {
	var probed string
	deps := hermeticSelfCheckDeps()
	deps.Getenv = func(key string) string {
		if key == BinaryEnvVar {
			return "/custom/install/engram"
		}
		return ""
	}
	deps.LookPath = func(string) (string, error) { return "/usr/local/bin/engram", nil }
	deps.RunVersion = func(_ context.Context, binary string) (string, error) {
		probed = binary
		return "engram 2.0.0\n", nil
	}

	got := CheckStaleBinary(context.Background(), "2.0.0", deps)
	if got.Candidate != "/custom/install/engram" || probed != "/custom/install/engram" {
		t.Fatalf("candidate = %q (probed %q), want the absolute %s", got.Candidate, probed, BinaryEnvVar)
	}
}

func TestCheckStaleBinaryFallsBackToPathForRelativeBinaryEnv(t *testing.T) {
	var probed string
	deps := hermeticSelfCheckDeps()
	deps.Getenv = func(key string) string {
		if key == BinaryEnvVar {
			return "bin/engram"
		}
		return ""
	}
	deps.LookPath = func(name string) (string, error) {
		if name != "engram" {
			return "", errors.New("unexpected lookup")
		}
		return "/usr/local/bin/engram", nil
	}
	deps.RunVersion = func(_ context.Context, binary string) (string, error) {
		probed = binary
		return "engram 2.0.0\n", nil
	}

	got := CheckStaleBinary(context.Background(), "2.0.0", deps)
	if got.Candidate != "/usr/local/bin/engram" || probed != "/usr/local/bin/engram" {
		t.Fatalf("candidate = %q (probed %q), want the PATH lookup to win for a relative %s", got.Candidate, probed, BinaryEnvVar)
	}
}

func TestCheckStaleBinaryTreatsUnknownOwnExecutableAsNotActionable(t *testing.T) {
	tests := []struct {
		name string
		deps func() SelfCheckDeps
	}{
		{
			name: "executable lookup fails",
			deps: func() SelfCheckDeps {
				deps := hermeticSelfCheckDepsWithVersion("engram 2.1.0\n")
				deps.Executable = func() (string, error) { return "", errors.New("no executable") }
				return deps
			},
		},
		{
			name: "own file stat fails with a non-ENOENT error",
			deps: func() SelfCheckDeps {
				deps := hermeticSelfCheckDepsWithVersion("engram 2.1.0\n")
				deps.Stat = func(string) (os.FileInfo, error) { return nil, errors.New("permission denied") }
				return deps
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckStaleBinary(context.Background(), "2.0.0", tt.deps())
			if !got.Stale || got.Actionable {
				t.Fatalf("report = %+v, want stale but never actionable", got)
			}
		})
	}
}

func TestParseBinaryVersion(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
		ok   bool
	}{
		{name: "exact form", out: "engram 2.0.0\n", want: "2.0.0", ok: true},
		{name: "no trailing newline", out: "engram 2.0.0", want: "2.0.0", ok: true},
		{name: "v prefix", out: "engram v2.0.0\n", want: "v2.0.0", ok: true},
		{name: "empty output", out: "", ok: false},
		{name: "whitespace only", out: " \t\n", ok: false},
		{name: "missing product name", out: "2.0.0\n", ok: false},
		{name: "different product", out: "other 2.0.0\n", ok: false},
		{name: "extra token", out: "engram 2.0.0 extra\n", ok: false},
		{name: "extra line", out: "engram 2.0.0\nnotice\n", ok: false},
		{name: "missing version token", out: "engram \n", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseBinaryVersion(tt.out)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("parseBinaryVersion(%q) = (%q, %v), want (%q, %v)", tt.out, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestSupervisionDecisionTable(t *testing.T) {
	tests := []struct {
		name           string
		ctx            SupervisionContext
		wantSupervised bool
		wantSource     SupervisorSource
		wantDisabled   bool
	}{
		{
			name:           "launchd job reparented to pid 1",
			ctx:            SupervisionContext{XPCServiceName: "com.gentleman.engram", PPID: 1},
			wantSupervised: true,
			wantSource:     SupervisorLaunchd,
		},
		{
			name: "launchd variables inherited by a child whose parent is alive",
			ctx:  SupervisionContext{XPCServiceName: "com.gentleman.engram", PPID: 4242},
		},
		{
			name:           "systemd unit reparented to pid 1",
			ctx:            SupervisionContext{InvocationID: "9f1a", PPID: 1},
			wantSupervised: true,
			wantSource:     SupervisorSystemd,
		},
		{
			name:           "systemd unit whose parent is systemd itself",
			ctx:            SupervisionContext{InvocationID: "9f1a", PPID: 900, ParentComm: "systemd\n"},
			wantSupervised: true,
			wantSource:     SupervisorSystemd,
		},
		{
			name: "systemd variables inherited by a child without /proc evidence",
			ctx:  SupervisionContext{InvocationID: "9f1a", PPID: 900},
		},
		{
			name: "a parent that merely has systemd in its name is not evidence",
			ctx:  SupervisionContext{InvocationID: "9f1a", PPID: 900, ParentComm: "systemd-logind\n"},
		},
		{
			name:           "the env override forces the supervised path",
			ctx:            SupervisionContext{Override: "1", PPID: 4242},
			wantSupervised: true,
			wantSource:     SupervisorForced,
		},
		{
			name:           "the env override accepts true and on",
			ctx:            SupervisionContext{Override: "ON"},
			wantSupervised: true,
			wantSource:     SupervisorForced,
		},
		{
			name:         "the env override disables self exit entirely",
			ctx:          SupervisionContext{Override: "0", XPCServiceName: "com.gentleman.engram", PPID: 1},
			wantDisabled: true,
		},
		{
			name:         "the env override accepts false and off",
			ctx:          SupervisionContext{Override: " off ", XPCServiceName: "com.gentleman.engram", PPID: 1},
			wantDisabled: true,
		},
		{
			name:           "an unrecognized override falls back to detection",
			ctx:            SupervisionContext{Override: "maybe", XPCServiceName: "com.gentleman.engram", PPID: 1},
			wantSupervised: true,
			wantSource:     SupervisorLaunchd,
		},
		{
			name: "no supervisor evidence at all",
			ctx:  SupervisionContext{PPID: 4242},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ctx.Decide()
			if got.Supervised != tt.wantSupervised || got.Source != tt.wantSource || got.Disabled != tt.wantDisabled {
				t.Fatalf("Decide() = %+v, want supervised=%v source=%q disabled=%v",
					got, tt.wantSupervised, tt.wantSource, tt.wantDisabled)
			}
		})
	}
}

func TestReadSupervisionContext(t *testing.T) {
	env := map[string]string{
		"XPC_SERVICE_NAME":          "com.gentleman.engram",
		"INVOCATION_ID":             "9f1a",
		"ENGRAM_RESTART_ON_UPGRADE": "1",
		"ENGRAM_SELFCHECK_INTERVAL": "1s",
	}

	t.Run("darwin never reads /proc", func(t *testing.T) {
		read := false
		deps := SupervisionDeps{
			Getenv:   func(key string) string { return env[key] },
			Getppid:  func() int { return 900 },
			ReadFile: func(string) ([]byte, error) { read = true; return []byte("systemd\n"), nil },
			GOOS:     "darwin",
		}

		got := ReadSupervisionContext(deps)
		if got.XPCServiceName != "com.gentleman.engram" || got.InvocationID != "9f1a" || got.Override != "1" || got.PPID != 900 {
			t.Fatalf("context = %+v, want the environment and ppid to be carried over", got)
		}
		if got.ParentComm != "" || read {
			t.Fatalf("parent comm = %q (read=%v), want no /proc read outside Linux", got.ParentComm, read)
		}
	})

	t.Run("linux corroborates systemd through /proc", func(t *testing.T) {
		var readPath string
		deps := SupervisionDeps{
			Getenv:  func(key string) string { return env[key] },
			Getppid: func() int { return 900 },
			ReadFile: func(path string) ([]byte, error) {
				readPath = path
				return []byte("systemd\n"), nil
			},
			GOOS: "linux",
		}

		got := ReadSupervisionContext(deps)
		if got.ParentComm != "systemd\n" {
			t.Fatalf("parent comm = %q, want the /proc content", got.ParentComm)
		}
		if readPath != "/proc/900/comm" {
			t.Fatalf("read %q, want /proc/900/comm", readPath)
		}
		if !got.Decide().Supervised {
			t.Fatalf("linux systemd corroboration must decide supervised, got %+v", got.Decide())
		}
	})

	t.Run("linux skips /proc when the parent is already pid 1", func(t *testing.T) {
		read := false
		deps := SupervisionDeps{
			Getenv:   func(string) string { return "" },
			Getppid:  func() int { return 1 },
			ReadFile: func(string) ([]byte, error) { read = true; return nil, errors.New("boom") },
			GOOS:     "linux",
		}

		got := ReadSupervisionContext(deps)
		if read {
			t.Fatal("a pid 1 parent needs no /proc corroboration")
		}
		if got.ParentComm != "" {
			t.Fatalf("parent comm = %q, want empty", got.ParentComm)
		}
	})

	t.Run("an unreadable /proc entry stays empty", func(t *testing.T) {
		deps := SupervisionDeps{
			Getenv:   func(string) string { return "" },
			Getppid:  func() int { return 900 },
			ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
			GOOS:     "linux",
		}

		if got := ReadSupervisionContext(deps); got.ParentComm != "" {
			t.Fatalf("parent comm = %q, want empty", got.ParentComm)
		}
	})
}

func TestParseSelfCheckInterval(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "unset falls back to the default", raw: "", want: DefaultSelfCheckInterval},
		{name: "invalid falls back to the default", raw: "every 5 minutes", want: DefaultSelfCheckInterval},
		{name: "negative falls back to the default", raw: "-1s", want: DefaultSelfCheckInterval},
		{name: "explicit duration is honoured", raw: "30s", want: 30 * time.Second},
		{name: "whitespace is trimmed", raw: " 2m \n", want: 2 * time.Minute},
		{name: "zero disables the re-check", raw: "0", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseSelfCheckInterval(tt.raw); got != tt.want {
				t.Fatalf("ParseSelfCheckInterval(%q) = %s, want %s", tt.raw, got, tt.want)
			}
		})
	}
}
