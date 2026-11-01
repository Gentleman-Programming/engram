package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPidfileRoundtripAndReplace(t *testing.T) {
	dir := t.TempDir()
	path := PidfilePath(dir)
	if filepath.Base(path) != PidfileName {
		t.Fatalf("pidfile base = %q, want %q", filepath.Base(path), PidfileName)
	}
	if err := WritePidfile(path, 4242); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	pid, err := ReadPidfile(path)
	if err != nil || pid != 4242 {
		t.Fatalf("read pidfile = %d, %v; want 4242", pid, err)
	}
	// A new daemon start must replace the previous pid, not collide with it.
	if err := WritePidfile(path, 5252); err != nil {
		t.Fatalf("rewrite pidfile: %v", err)
	}
	if pid, err := ReadPidfile(path); err != nil || pid != 5252 {
		t.Fatalf("reread pidfile = %d, %v; want 5252", pid, err)
	}
	if err := RemovePidfile(path); err != nil {
		t.Fatalf("remove pidfile: %v", err)
	}
	if _, err := ReadPidfile(path); !errors.Is(err, ErrNoPidfile) {
		t.Fatalf("read removed pidfile = %v, want ErrNoPidfile", err)
	}
	if err := RemovePidfile(path); err != nil {
		t.Fatalf("remove missing pidfile = %v, want nil", err)
	}
}

func TestReadPidfileRejectsMalformedContent(t *testing.T) {
	path := PidfilePath(t.TempDir())
	if err := os.WriteFile(path, []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPidfile(path); err == nil || errors.Is(err, ErrNoPidfile) {
		t.Fatalf("malformed pidfile err = %v, want parse error", err)
	}
}

func TestWritePidfileRejectsInvalidPid(t *testing.T) {
	path := PidfilePath(t.TempDir())
	if err := WritePidfile(path, 0); err == nil {
		t.Fatal("write pidfile with pid 0 = nil, want error")
	}
}

func TestProcessAlive(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Fatal("ProcessAlive(self) = false, want true")
	}
	// pid 0 and negative pids address process groups, never a daemon pid.
	if ProcessAlive(0) || ProcessAlive(-1) {
		t.Fatal("ProcessAlive(invalid pid) = true, want false")
	}
}
