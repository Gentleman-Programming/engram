package hostbind

import (
	"errors"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSameProcPIDNamespace(t *testing.T) {
	if !sameProcPIDNamespace("Name:\tfixture\nNSpid:\t123\n", "123") {
		t.Fatal("qualified proc namespace rejected")
	}
	for _, status := range []string{"Name:\tfixture\n", "NSpid:\t123\t123\n", "NSpid:\t456\n"} {
		if sameProcPIDNamespace(status, "123") {
			t.Fatalf("uncertain proc namespace accepted: %q", status)
		}
	}
}

func TestProcessTerminatedBeforeReap(t *testing.T) {
	domain, err := ProcessDomain()
	if err != nil {
		t.Skipf("qualified procfs unavailable: %v", err)
	}
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	pid := child.Process.Pid
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
		t.Skipf("pidfd unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	start, err := ProcessStart(pid)
	if err != nil {
		t.Fatal(err)
	}
	if exited, err := ProcessTerminated(pid, start, domain); err != nil || exited {
		t.Fatalf("live child reported terminated: %v, %v", exited, err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Waitid(unix.P_PID, pid, &unix.Siginfo{}, unix.WEXITED|unix.WNOWAIT, nil); err != nil {
		t.Fatal(err)
	}
	if exited, err := ProcessTerminated(pid, start, domain); err != nil || !exited {
		t.Fatalf("exited child awaiting reap reported live: %v, %v", exited, err)
	}
}
