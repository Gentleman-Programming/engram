package hostbind

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func fakeProcesses(t *testing.T, starts map[int]string) {
	t.Helper()
	prev := processStart
	processStart = func(pid int) (string, error) {
		if s, ok := starts[pid]; ok {
			return s, nil
		}
		return "", ErrNoProcess
	}
	t.Cleanup(func() { processStart = prev })
}

func TestPublishThenLookupReturnsBoundSession(t *testing.T) {
	dir := t.TempDir()
	fakeProcesses(t, map[int]string{4242: "t1"})
	if err := Publish(dir, 4242, "sess-a"); err != nil {
		t.Fatal(err)
	}
	if got, ok := Lookup(dir, 4242); !ok || got != "sess-a" {
		t.Fatalf("Lookup = %q, %v; want sess-a, true", got, ok)
	}
}

func TestPublishReplacesBindingForClearResumeFork(t *testing.T) {
	dir := t.TempDir()
	fakeProcesses(t, map[int]string{4242: "t1"})
	for _, id := range []string{"sess-a", "sess-b"} {
		if err := Publish(dir, 4242, id); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := Lookup(dir, 4242); got != "sess-b" {
		t.Fatalf("Lookup = %q; want latest binding sess-b", got)
	}
}

func TestLookupIgnoresReusedPID(t *testing.T) {
	dir := t.TempDir()
	starts := map[int]string{4242: "t1"}
	fakeProcesses(t, starts)
	if err := Publish(dir, 4242, "sess-a"); err != nil {
		t.Fatal(err)
	}
	starts[4242] = "t2" // same PID, different process
	if got, ok := Lookup(dir, 4242); ok {
		t.Fatalf("Lookup = %q, true; a reused PID must never match", got)
	}
}

func TestLookupMissingOrCorruptBindingIsNotBound(t *testing.T) {
	dir := t.TempDir()
	fakeProcesses(t, map[int]string{4242: "t1"})
	if _, ok := Lookup(dir, 4242); ok {
		t.Fatal("missing binding must not resolve")
	}
	if err := os.MkdirAll(filepath.Join(dir, dirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path(dir, 4242), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup(dir, 4242); ok {
		t.Fatal("corrupt binding must not resolve")
	}
}

func TestPublishRejectsInvalidInputAndUnknownProcess(t *testing.T) {
	dir := t.TempDir()
	fakeProcesses(t, map[int]string{4242: "t1"})
	for _, tc := range []struct {
		pid int
		id  string
	}{{0, "s"}, {1, "s"}, {4242, "  "}, {9999, "s"}} {
		if err := Publish(dir, tc.pid, tc.id); err == nil {
			t.Fatalf("Publish(%d, %q) succeeded; want error", tc.pid, tc.id)
		}
	}
	if err := Publish("", 4242, "s"); err == nil {
		t.Fatal("empty data dir must be rejected")
	}
}

func TestPublishPrunesBindingsOfExitedHosts(t *testing.T) {
	dir := t.TempDir()
	starts := map[int]string{100: "a", 200: "b"}
	fakeProcesses(t, starts)
	if err := Publish(dir, 100, "old"); err != nil {
		t.Fatal(err)
	}
	delete(starts, 100) // host 100 exited
	if err := Publish(dir, 200, "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path(dir, 100)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale binding for exited host should be pruned, stat err = %v", err)
	}
	if got, ok := Lookup(dir, 200); !ok || got != "new" {
		t.Fatalf("live binding lost: %q, %v", got, ok)
	}
}

func TestProcessStartIsStableForCurrentProcess(t *testing.T) {
	a, err := ProcessStart(os.Getpid())
	if err != nil {
		t.Skipf("process start unavailable on this platform: %v", err)
	}
	b, _ := ProcessStart(os.Getpid())
	if a == "" || a != b {
		t.Fatalf("start token unstable: %q vs %q", a, b)
	}
	if _, err := ProcessStart(1 << 30); !errors.Is(err, ErrNoProcess) {
		t.Fatalf("missing pid err = %v; want ErrNoProcess (pid %s)", err, strconv.Itoa(1<<30))
	}
}
