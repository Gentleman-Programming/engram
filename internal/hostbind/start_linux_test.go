//go:build linux

package hostbind

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestProcessStartRejectsUnreapedProcess(t *testing.T) {
	cmd, input := startHostbindExitFixture(t)
	pid, dir := cmd.Process.Pid, t.TempDir()
	if err := Publish(dir, pid, "fixture"); err != nil {
		t.Fatalf("publish live binding: %v", err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	// Observe the child exit without reaping it. /proc still contains its
	// PID and original start time until the final wait.
	var info unix.Siginfo
	if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil); err != nil {
		t.Fatal(err)
	}
	assertExitedHostBinding(t, dir, pid)
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
