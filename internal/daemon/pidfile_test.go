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
	if err := WritePidfile(path, PidRecord{Pid: 4242, Port: 7437}); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	record, err := ReadPidfile(path)
	if err != nil || record.Pid != 4242 || record.Port != 7437 {
		t.Fatalf("read pidfile = %+v, %v; want {4242 7437}", record, err)
	}
	// A new daemon start must replace the previous record, not collide with it.
	if err := WritePidfile(path, PidRecord{Pid: 5252, Port: 8000}); err != nil {
		t.Fatalf("rewrite pidfile: %v", err)
	}
	if record, err := ReadPidfile(path); err != nil || record.Pid != 5252 || record.Port != 8000 {
		t.Fatalf("reread pidfile = %+v, %v; want {5252 8000}", record, err)
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

func TestPidfileKeepsReadableJSONShape(t *testing.T) {
	path := PidfilePath(t.TempDir())
	if err := WritePidfile(path, PidRecord{Pid: 99, Port: 7000}); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"pid\":99,\"port\":7000}\n"
	if string(data) != want {
		t.Fatalf("pidfile content = %q, want %q", data, want)
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
	if err := WritePidfile(path, PidRecord{Pid: 0, Port: 7437}); err == nil {
		t.Fatal("write pidfile with pid 0 = nil, want error")
	}
	if err := WritePidfile(path, PidRecord{Pid: -3, Port: 7437}); err == nil {
		t.Fatal("write pidfile with negative pid = nil, want error")
	}
	if err := WritePidfile(path, PidRecord{Pid: 10, Port: -1}); err == nil {
		t.Fatal("write pidfile with negative port = nil, want error")
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
