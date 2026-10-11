package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

const (
	compatibilityFixtureVersion = "fixture"
	compatibilityUsage          = "Usage: engram compatibility --data-dir <absolute-directory> --json\n"
	compatibilityUnsafe         = "compatibility: unsafe or invalid fixture metadata\n"
	compatibilityInstanceID     = "0123456789abcdef0123456789abcdef"
)

func TestCompatibilityEarlyDispatch(t *testing.T) {
	oldArgs, oldConfig, oldUpdate, oldExit := os.Args, storeDefaultConfig, checkForUpdates, exitFunc
	defer func() {
		os.Args, storeDefaultConfig, checkForUpdates, exitFunc = oldArgs, oldConfig, oldUpdate, oldExit
	}()
	// Any fallthrough into configuration is forbidden, even for invalid input.
	storeDefaultConfig = func() (store.Config, error) {
		t.Fatal("compatibility resolved configuration")
		return store.Config{}, nil
	}
	exitFunc = func(code int) {
		if code != 2 {
			t.Errorf("exit = %d, want 2", code)
		}
	}
	os.Args = []string{"engram", "compatibility", "--unsupported"}
	main()
}

// buildCompatibilityBinary builds the candidate CLI into a temporary directory.
// Tests never execute an installed engram.
func buildCompatibilityBinary(t *testing.T) string {
	t.Helper()
	binaryPath := filepath.Join(t.TempDir(), "engram")
	build := exec.Command("go", "build", "-ldflags=-X main.version="+compatibilityFixtureVersion, "-o", binaryPath, ".")
	build.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOPROXY=off", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("isolated build: %v %s", err, out)
	}
	return binaryPath
}

// compatibilityTreeHash covers names, modes, symlink targets and regular-file bytes.
func compatibilityTreeHash(t *testing.T, root string) [32]byte {
	t.Helper()
	var b bytes.Buffer
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		fmt.Fprintln(&b, path, info.Mode())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			b.WriteString(target)
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			b.Write(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b.Bytes())
}

type compatibilityRun struct {
	stdout, stderr string
	exit           int
}

// runCompatibilityBinary executes the isolated binary with an isolated HOME and
// a sentinel data directory, then proves that no watched tree changed.
func runCompatibilityBinary(t *testing.T, binaryPath string, watched []string, args ...string) compatibilityRun {
	t.Helper()
	home, workdir := t.TempDir(), t.TempDir()
	watched = append(watched, home, workdir)
	before := make([][32]byte, len(watched))
	for i, root := range watched {
		before[i] = compatibilityTreeHash(t, root)
	}
	cmd := exec.Command(binaryPath, args...)
	cmd.Dir = workdir
	cmd.Env = []string{"HOME=" + home, "ENGRAM_DATA_DIR=" + filepath.Join(home, "sentinel"), "ENGRAM_HTTP_TOKEN=fixture-only"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := compatibilityRun{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	for i, root := range watched {
		if compatibilityTreeHash(t, root) != before[i] {
			t.Fatalf("run %v changed %s", args, root)
		}
	}
	return result
}

// captureCompatibilityStdout derives expected output from the unchanged in-process printer.
func captureCompatibilityStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout, oldVersion := os.Stdout, version
	os.Stdout, version = w, compatibilityFixtureVersion
	done := make(chan []byte)
	go func() { data, _ := io.ReadAll(r); done <- data }()
	fn()
	_ = w.Close()
	os.Stdout, version = oldStdout, oldVersion
	return string(<-done)
}

func writeCompatibilityHeader(t *testing.T, path string, schema uint32) {
	t.Helper()
	header := make([]byte, 512)
	copy(header, "SQLite format 3\x00")
	binary.BigEndian.PutUint16(header[16:18], 512)
	header[18], header[19] = 1, 1
	binary.BigEndian.PutUint32(header[60:64], schema)
	if err := os.WriteFile(path, header, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCompatibilityPublicBinary(t *testing.T) {
	binaryPath := buildCompatibilityBinary(t)
	writeFile := func(t *testing.T, path string, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name    string
		prepare func(t *testing.T, dir string) []string
		status  string
		stderr  string
	}{
		{name: "fresh", status: "fresh"},
		{name: "legacy_schema", status: "legacy_schema", prepare: existingCompatibilityStore(0)},
		{name: "current_header_unknown_shape", status: "unknown_shape", prepare: existingCompatibilityStore(1)},
		{name: "future_schema", status: "future_schema", prepare: existingCompatibilityStore(2)},
		{name: "unsupported_option", stderr: compatibilityUsage, prepare: func(t *testing.T, dir string) []string {
			return []string{"compatibility", "--data-dir", dir, "--json", "--force"}
		}},
		{name: "duplicate_json", stderr: compatibilityUsage, prepare: func(t *testing.T, dir string) []string {
			return []string{"compatibility", "--data-dir", dir, "--json", "--json"}
		}},
		{name: "missing_json", stderr: compatibilityUsage, prepare: func(t *testing.T, dir string) []string {
			return []string{"compatibility", "--data-dir", dir}
		}},
		{name: "relative_path", stderr: compatibilityUnsafe, prepare: func(t *testing.T, dir string) []string {
			return []string{"compatibility", "--data-dir", "relative", "--json"}
		}},
		{name: "missing_directory", stderr: compatibilityUnsafe, prepare: func(t *testing.T, dir string) []string {
			return []string{"compatibility", "--data-dir", filepath.Join(dir, "missing"), "--json"}
		}},
		{name: "unexpected_file_without_database", stderr: compatibilityUnsafe, prepare: func(t *testing.T, dir string) []string {
			writeFile(t, filepath.Join(dir, "notes.txt"), "fixture")
			return nil
		}},
		{name: "malformed_header", stderr: compatibilityUnsafe, prepare: func(t *testing.T, dir string) []string {
			existingCompatibilityStore(1)(t, dir)
			writeFile(t, filepath.Join(dir, "engram.db"), "invalid")
			return nil
		}},
		{name: "nonregular_database", stderr: compatibilityUnsafe, prepare: func(t *testing.T, dir string) []string {
			if err := os.Mkdir(filepath.Join(dir, "engram.db"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, ".instance-id"), compatibilityInstanceID+"\n")
			return nil
		}},
		{name: "missing_identity", stderr: compatibilityUnsafe, prepare: func(t *testing.T, dir string) []string {
			writeCompatibilityHeader(t, filepath.Join(dir, "engram.db"), 1)
			return nil
		}},
		{name: "invalid_identity", stderr: compatibilityUnsafe, prepare: func(t *testing.T, dir string) []string {
			existingCompatibilityStore(1)(t, dir)
			writeFile(t, filepath.Join(dir, ".instance-id"), "broken\n")
			return nil
		}},
		{name: "wal_sidecar", stderr: compatibilityUnsafe, prepare: sidecarCompatibilityStore("engram.db-wal")},
		{name: "shm_sidecar", stderr: compatibilityUnsafe, prepare: sidecarCompatibilityStore("engram.db-shm")},
		{name: "journal_sidecar", stderr: compatibilityUnsafe, prepare: sidecarCompatibilityStore("engram.db-journal")},
		{name: "ambiguous_database", stderr: compatibilityUnsafe, prepare: sidecarCompatibilityStore("other.db")},
		{name: "database_alias", stderr: compatibilityUnsafe, prepare: func(t *testing.T, dir string) []string {
			existingCompatibilityStore(1)(t, dir)
			if err := os.Symlink("engram.db", filepath.Join(dir, "alias.db")); err != nil {
				t.Fatal(err)
			}
			return nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := []string{"compatibility", "--data-dir", dir, "--json"}
			if tt.prepare != nil {
				if custom := tt.prepare(t, dir); custom != nil {
					args = custom
				}
			}
			got := runCompatibilityBinary(t, binaryPath, []string{dir}, args...)
			if tt.stderr != "" {
				if got.exit != 2 || got.stdout != "" || got.stderr != tt.stderr {
					t.Fatalf("got %+v", got)
				}
				return
			}
			identity := ""
			if tt.status != "fresh" {
				identity = `,"instance_id":"` + compatibilityInstanceID + `"`
			}
			want := `{"revision":1,"source":"offline","status":"` + tt.status + `","offline_automatic_opening_authorized":false,"active_reuse_authorized":false` + identity + "}\n"
			if got.exit != 0 || got.stdout != want || got.stderr != "" {
				t.Fatalf("got %+v, want stdout %q", got, want)
			}
		})
	}
}

func existingCompatibilityStore(schema uint32) func(t *testing.T, dir string) []string {
	return func(t *testing.T, dir string) []string {
		writeCompatibilityHeader(t, filepath.Join(dir, "engram.db"), schema)
		if err := os.WriteFile(filepath.Join(dir, ".instance-id"), []byte(compatibilityInstanceID+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return nil
	}
}

func sidecarCompatibilityStore(name string) func(t *testing.T, dir string) []string {
	return func(t *testing.T, dir string) []string {
		existingCompatibilityStore(1)(t, dir)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return nil
	}
}

// TestCompatibilityEarlyDispatchPreservesCommands checks the commands that
// share main's pre-configuration dispatch region. Expected output comes from the
// unchanged baseline printers and literals; none of these forms reach a store.
func TestCompatibilityEarlyDispatchPreservesCommands(t *testing.T) {
	binaryPath := buildCompatibilityBinary(t)
	usage := captureCompatibilityStdout(t, printUsage)
	testUsage := captureCompatibilityStdout(t, printTestUsage)
	type dispatchCase struct {
		name    string
		args    []string
		watched []string
		want    compatibilityRun
	}
	tests := []dispatchCase{
		{name: "no_arguments", want: compatibilityRun{stdout: usage, exit: 1}},
		{name: "help", args: []string{"help"}, want: compatibilityRun{stdout: usage}},
		{name: "test_help", args: []string{"test", "--help"}, want: compatibilityRun{stdout: testUsage}},
		{name: "test_unknown_flag", args: []string{"test", "--bogus"}, want: compatibilityRun{stdout: testUsage, stderr: "error: unknown test flag \"--bogus\"\n\n", exit: 1}},
		{name: "export_help", args: []string{"export", "--help"}, want: compatibilityRun{stdout: "Usage: engram export [file.json] [--project NAME | --all]\n\nOptions:\n  --project NAME  Export one project (default: current project)\n  --all           Export every project\n  --help          Show this help\n"}},
		{name: "import_help", args: []string{"import", "--help"}, want: compatibilityRun{stdout: "Usage: engram import <file.json>\n\nOptions:\n  --help          Show this help\n"}},
		{name: "serve_background_usage", args: []string{"serve-background"}, want: compatibilityRun{stderr: "engram: usage: engram serve-background LOG_PATH\n", exit: 1}},
		{name: "compatibility_help_rejected", args: []string{"compatibility", "--help"}, want: compatibilityRun{stderr: compatibilityUsage, exit: 2}},
	}
	if runtime.GOOS != "windows" {
		// Only non-Windows platforms reject before opening the log or starting a process.
		logDir := t.TempDir()
		tests = append(tests, dispatchCase{name: "serve_background_unsupported_platform", args: []string{"serve-background", filepath.Join(logDir, "serve.log")}, watched: []string{logDir}, want: compatibilityRun{stderr: "engram: background serve is supported only on Windows\n", exit: 1}})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runCompatibilityBinary(t, binaryPath, tt.watched, tt.args...); got != tt.want {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
	if !strings.Contains(usage, "compatibility --data-dir <absolute-directory> --json") || !strings.Contains(usage, "never opens, migrates or repairs") {
		t.Fatalf("usage omits read-only compatibility command:\n%s", usage)
	}
}
