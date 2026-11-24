package hostbind

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ProcessDomain qualifies boot-relative Linux process start times with the
// current boot and PID namespace. An ambiguous procfs view is unavailable.
func ProcessDomain() (string, error) {
	pid, err := os.Readlink("/proc/self")
	if err != nil || pid != strconv.Itoa(os.Getpid()) {
		return "", ErrProcessDomainUnavailable
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || !sameProcPIDNamespace(string(status), pid) {
		return "", ErrProcessDomainUnavailable
	}
	namespace, err := os.Readlink("/proc/self/ns/pid")
	if err != nil || !strings.HasPrefix(namespace, "pid:[") || !strings.HasSuffix(namespace, "]") {
		return "", ErrProcessDomainUnavailable
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(bootID)) == "" {
		return "", ErrProcessDomainUnavailable
	}
	return strings.TrimSpace(string(bootID)) + "/" + namespace, nil
}

// NSpid is measured from the procfs mount's namespace. A second
// value means this process sees an ancestor namespace through that mount.
func sameProcPIDNamespace(status, pid string) bool {
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "NSpid:") {
			fields := strings.Fields(line)
			return len(fields) == 2 && fields[1] == pid
		}
	}
	return false
}

// ProcessTerminated confirms exit in the qualified process domain. A pidfd
// refers to the thread group, so an exited leader alone does not prove exit.
func ProcessTerminated(pid int, start, domain string) (bool, error) {
	currentDomain, err := ProcessDomain()
	if err != nil || currentDomain != domain || pid <= 1 || start == "" {
		return false, ErrProcessDomainUnavailable
	}
	observed, err := ProcessStart(pid)
	if err == nil && observed != start {
		return true, nil
	}
	if err != nil && !errors.Is(err, ErrNoProcess) {
		return false, err
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err == nil {
		defer func() { _ = unix.Close(fd) }()
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 0); err != nil {
			return false, err
		}
		return fds[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0, nil
	}
	if errors.Is(err, unix.ESRCH) {
		return true, nil
	}
	// Fall back to checking for a missing PID on kernels without pidfd or
	// when the descriptor cannot be opened. A present PID stays unverified.
	err = unix.Kill(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return true, nil
	}
	return false, err
}
