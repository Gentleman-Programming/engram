package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Environment contract of the stale-binary self-check. The names live here so
// the primitives and the cmd/engram wiring cannot drift apart.
const (
	// BinaryEnvVar optionally names the binary compared with this process. Only
	// an absolute path is honoured; anything else falls back to PATH.
	BinaryEnvVar = "ENGRAM_BIN"
	// RestartEnvVar forces ("1", "true", "on") or disables ("0", "false", "off")
	// the supervised self-exit. Unset means supervisor detection decides.
	RestartEnvVar = "ENGRAM_RESTART_ON_UPGRADE"
	// SelfCheckIntervalEnvVar configures the periodic re-check as a Go duration.
	// "0" disables the re-check; empty or invalid falls back to five minutes.
	SelfCheckIntervalEnvVar = "ENGRAM_SELFCHECK_INTERVAL"

	// launchdEnvVar is set by launchd for managed jobs, systemdEnvVar by systemd
	// for managed units. Both are inherited by children, so on their own they
	// are never evidence of supervision.
	launchdEnvVar = "XPC_SERVICE_NAME"
	systemdEnvVar = "INVOCATION_ID"

	// engramBinaryName is resolved on PATH when BinaryEnvVar is unset.
	engramBinaryName = "engram"
	// binaryVersionPrefix is the product token the candidate must print.
	binaryVersionPrefix = "engram"
)

// DefaultBinaryVersionTimeout bounds the `<candidate> version` probe. A binary
// that cannot answer in time is unknown, never stale.
const DefaultBinaryVersionTimeout = 2 * time.Second

// DefaultSelfCheckInterval is the periodic re-check cadence for a long-lived
// daemon, so a replaced binary is noticed without waiting for a reboot.
const DefaultSelfCheckInterval = 5 * time.Minute

// StaleBinaryReport is the outcome of comparing this process's build version
// with the version reported by the engram binary installed on disk.
//
// It is a pure observation: producing one never exits, signals or touches
// another process. Callers decide whether an actionable report may end this
// process, and only under a supervisor.
type StaleBinaryReport struct {
	// Known is true when the candidate answered with a parsable version and the
	// two versions could be compared.
	Known bool
	// Stale is true when the on-disk version differs from the running version.
	Stale bool
	// Actionable is true when this process may exit so that a supervisor
	// relaunches it with the binary that is on disk. Actionable implies Stale.
	Actionable bool
	// Running is this process's version; OnDisk is the version the candidate
	// reports. Both are empty when Known is false.
	Running string
	OnDisk  string
	// Candidate is the binary that was probed.
	Candidate string
}

// SelfCheckDeps holds every seam the check touches: the environment, PATH, the
// running executable and the candidate binary. Tests substitute deterministic
// fakes, so no test has to run a real engram binary.
type SelfCheckDeps struct {
	// Getenv reads an environment variable (defaults to os.Getenv).
	Getenv func(string) string
	// LookPath resolves a binary name on PATH (defaults to exec.LookPath).
	LookPath func(string) (string, error)
	// Executable returns the path of the running binary (defaults to os.Executable).
	Executable func() (string, error)
	// Stat inspects a path (defaults to os.Stat).
	Stat func(string) (os.FileInfo, error)
	// SameFile reports whether two FileInfos describe the same file
	// (defaults to os.SameFile).
	SameFile func(a, b os.FileInfo) bool
	// EvalSymlinks resolves a path (defaults to filepath.EvalSymlinks).
	EvalSymlinks func(string) (string, error)
	// RunVersion runs `<binary> version` and returns its stdout (defaults to
	// runBinaryVersion).
	RunVersion func(ctx context.Context, binary string) (string, error)
	// VersionTimeout bounds RunVersion. Zero means DefaultBinaryVersionTimeout.
	VersionTimeout time.Duration
}

// DefaultSelfCheckDeps returns the production seams of the stale-binary check.
func DefaultSelfCheckDeps() SelfCheckDeps {
	return SelfCheckDeps{
		Getenv:       os.Getenv,
		LookPath:     exec.LookPath,
		Executable:   os.Executable,
		Stat:         os.Stat,
		SameFile:     os.SameFile,
		EvalSymlinks: filepath.EvalSymlinks,
		RunVersion:   runBinaryVersion,
	}
}

// CheckStaleBinary compares the running version with the version reported by
// the engram binary installed on disk.
//
// It is deliberately conservative and loop-free by construction:
//
//   - an empty or "dev" running version is never actionable;
//   - an unresolvable, unparsable, failing or slow candidate is unknown;
//   - an equal version is current;
//   - a different version is actionable only when the candidate is this very
//     process's own file (removed by the upgrade or replaced in place).
//
// The last rule is what makes a restart loop impossible: when PATH merely points
// at a different install, the finding is reported and the process keeps serving.
func CheckStaleBinary(ctx context.Context, current string, deps SelfCheckDeps) StaleBinaryReport {
	deps = deps.withDefaults()
	current = strings.TrimSpace(current)
	if !selfCheckVersionUsable(current) {
		return StaleBinaryReport{}
	}

	candidate, ok := deps.resolveCandidate()
	if !ok {
		return StaleBinaryReport{}
	}

	stdout, err := deps.probeVersion(ctx, candidate)
	if err != nil {
		return StaleBinaryReport{}
	}
	onDisk, ok := parseBinaryVersion(stdout)
	if !ok {
		return StaleBinaryReport{}
	}

	report := StaleBinaryReport{
		Known:     true,
		Running:   current,
		OnDisk:    onDisk,
		Candidate: candidate,
	}
	if normalizeBinaryVersion(onDisk) == normalizeBinaryVersion(current) {
		return report
	}

	report.Stale = true
	report.Actionable = deps.ownsCandidate(candidate)
	return report
}

// ParseSelfCheckInterval interprets SelfCheckIntervalEnvVar: a Go duration where
// "0" disables the periodic re-check and empty, negative or invalid values fall
// back to DefaultSelfCheckInterval.
func ParseSelfCheckInterval(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultSelfCheckInterval
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval < 0 {
		return DefaultSelfCheckInterval
	}
	return interval
}

// SupervisorSource names the supervisor that will relaunch a process after it
// exits.
type SupervisorSource string

const (
	// SupervisorNone means no supervisor was detected.
	SupervisorNone SupervisorSource = ""
	// SupervisorLaunchd means a launchd job with KeepAlive.
	SupervisorLaunchd SupervisorSource = "launchd"
	// SupervisorSystemd means a systemd unit with Restart.
	SupervisorSystemd SupervisorSource = "systemd"
	// SupervisorForced means RestartEnvVar asked for the supervised path.
	SupervisorForced SupervisorSource = "forced"
)

// SupervisionContext is the observed process context used to decide whether a
// supervisor will relaunch this process.
type SupervisionContext struct {
	// XPCServiceName is $XPC_SERVICE_NAME, set by launchd for managed jobs.
	XPCServiceName string
	// InvocationID is $INVOCATION_ID, set by systemd for managed units.
	InvocationID string
	// PPID is os.Getppid(). A supervised process is reparented to PID 1.
	PPID int
	// ParentComm is the parent's process name, read from /proc/<ppid>/comm on
	// Linux only, and corroborates systemd when the parent is not PID 1.
	ParentComm string
	// Override is the raw RestartEnvVar value.
	Override string
}

// SupervisorDecision is the outcome of supervision detection.
type SupervisorDecision struct {
	// Supervised is true when a supervisor will relaunch this process.
	Supervised bool
	// Source names that supervisor.
	Source SupervisorSource
	// Disabled is true when RestartEnvVar explicitly forbade a self-exit.
	Disabled bool
}

// Decide reports whether this process is supervised.
//
// The parent-process corroboration is required because XPC_SERVICE_NAME and
// INVOCATION_ID are inherited by children: an `engram serve` spawned by an agent
// that itself runs under launchd must not be mistaken for a supervised server.
func (c SupervisionContext) Decide() SupervisorDecision {
	switch strings.ToLower(strings.TrimSpace(c.Override)) {
	case "1", "true", "on":
		return SupervisorDecision{Supervised: true, Source: SupervisorForced}
	case "0", "false", "off":
		return SupervisorDecision{Disabled: true}
	}

	if strings.TrimSpace(c.XPCServiceName) != "" && c.PPID == 1 {
		return SupervisorDecision{Supervised: true, Source: SupervisorLaunchd}
	}
	if strings.TrimSpace(c.InvocationID) != "" && (c.PPID == 1 || strings.TrimSpace(c.ParentComm) == "systemd") {
		return SupervisorDecision{Supervised: true, Source: SupervisorSystemd}
	}
	return SupervisorDecision{Source: SupervisorNone}
}

// SupervisionDeps holds the seams used to observe the process context.
type SupervisionDeps struct {
	// Getenv reads an environment variable (defaults to os.Getenv).
	Getenv func(string) string
	// Getppid returns the parent process id (defaults to os.Getppid).
	Getppid func() int
	// ReadFile reads a file (defaults to os.ReadFile). It is only used on Linux.
	ReadFile func(string) ([]byte, error)
	// GOOS selects platform behavior (defaults to runtime.GOOS).
	GOOS string
}

// DefaultSupervisionDeps returns the production seams of supervision detection.
func DefaultSupervisionDeps() SupervisionDeps {
	return SupervisionDeps{
		Getenv:   os.Getenv,
		Getppid:  os.Getppid,
		ReadFile: os.ReadFile,
		GOOS:     runtime.GOOS,
	}
}

// ReadSupervisionContext observes the process context that Decide evaluates.
//
// The parent's comm is read from /proc on Linux only. On other platforms it
// stays empty, so an inherited XPC_SERVICE_NAME or INVOCATION_ID can never mark
// a child process as supervised on its own.
func ReadSupervisionContext(deps SupervisionDeps) SupervisionContext {
	deps = deps.withDefaults()

	context := SupervisionContext{
		XPCServiceName: deps.Getenv(launchdEnvVar),
		InvocationID:   deps.Getenv(systemdEnvVar),
		PPID:           deps.Getppid(),
		Override:       deps.Getenv(RestartEnvVar),
	}
	// A PID 1 parent is already conclusive, so /proc is only consulted when the
	// parent is somebody else.
	if deps.GOOS == "linux" && context.PPID > 1 {
		if comm, err := deps.ReadFile(fmt.Sprintf("/proc/%d/comm", context.PPID)); err == nil {
			context.ParentComm = string(comm)
		}
	}
	return context
}

// resolveCandidate picks the binary the running version is compared against:
// an absolute $ENGRAM_BIN when set, otherwise `engram` resolved on PATH.
func (d SelfCheckDeps) resolveCandidate() (string, bool) {
	if binary := strings.TrimSpace(d.Getenv(BinaryEnvVar)); binary != "" && filepath.IsAbs(binary) {
		return binary, true
	}
	path, err := d.LookPath(engramBinaryName)
	if err != nil || strings.TrimSpace(path) == "" {
		return "", false
	}
	return path, true
}

// probeVersion runs the candidate's `version` command under a bounded timeout.
func (d SelfCheckDeps) probeVersion(ctx context.Context, candidate string) (string, error) {
	timeout := d.VersionTimeout
	if timeout <= 0 {
		timeout = DefaultBinaryVersionTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return d.RunVersion(probeCtx, candidate)
}

// ownsCandidate reports whether the candidate binary is this very process's own
// file on disk, which is the only situation where exiting for a relaunch cannot
// produce a restart loop.
func (d SelfCheckDeps) ownsCandidate(candidate string) bool {
	own, err := d.Executable()
	if err != nil || strings.TrimSpace(own) == "" {
		return false
	}
	own = strings.TrimSpace(own)

	ownInfo, ownErr := d.Stat(own)
	if errors.Is(ownErr, os.ErrNotExist) {
		// The upgrade removed the file this process was started from.
		return true
	}
	if ownErr != nil {
		return false
	}

	if candidateInfo, candidateErr := d.Stat(candidate); candidateErr == nil && d.SameFile(ownInfo, candidateInfo) {
		return true
	}

	ownResolved, ownResolveErr := d.EvalSymlinks(own)
	candidateResolved, candidateResolveErr := d.EvalSymlinks(candidate)
	return ownResolveErr == nil && candidateResolveErr == nil && filepath.Clean(ownResolved) == filepath.Clean(candidateResolved)
}

// runBinaryVersion executes `<binary> version` and returns its stdout.
func runBinaryVersion(ctx context.Context, binary string) (string, error) {
	var stdout strings.Builder
	command := exec.CommandContext(ctx, binary, "version")
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// parseBinaryVersion accepts exactly the `engram <version>` stdout that
// `engram version` prints.
func parseBinaryVersion(stdout string) (string, bool) {
	line := strings.TrimSpace(stdout)
	if line == "" || strings.Contains(line, "\n") {
		return "", false
	}
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[0] != binaryVersionPrefix {
		return "", false
	}
	return fields[1], true
}

// selfCheckVersionUsable reports whether a running version can take part in the
// comparison at all. Local source builds and empty values never act.
func selfCheckVersionUsable(current string) bool {
	return current != "" && !strings.EqualFold(current, "dev")
}

// normalizeBinaryVersion drops a release tag's "v" prefix so `engram v2.0.0` and
// `engram 2.0.0` compare equal.
func normalizeBinaryVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

// withDefaults fills the seams a caller left nil so partial deps stay safe.
func (d SelfCheckDeps) withDefaults() SelfCheckDeps {
	if d.Getenv == nil {
		d.Getenv = os.Getenv
	}
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	if d.Executable == nil {
		d.Executable = os.Executable
	}
	if d.Stat == nil {
		d.Stat = os.Stat
	}
	if d.SameFile == nil {
		d.SameFile = os.SameFile
	}
	if d.EvalSymlinks == nil {
		d.EvalSymlinks = filepath.EvalSymlinks
	}
	if d.RunVersion == nil {
		d.RunVersion = runBinaryVersion
	}
	return d
}

// withDefaults fills the seams a caller left nil so partial deps stay safe.
func (d SupervisionDeps) withDefaults() SupervisionDeps {
	if d.Getenv == nil {
		d.Getenv = os.Getenv
	}
	if d.Getppid == nil {
		d.Getppid = os.Getppid
	}
	if d.ReadFile == nil {
		d.ReadFile = os.ReadFile
	}
	if d.GOOS == "" {
		d.GOOS = runtime.GOOS
	}
	return d
}
