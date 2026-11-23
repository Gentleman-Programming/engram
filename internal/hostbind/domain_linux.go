package hostbind

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
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

// ProcessTerminated confirms that the qualified process has exited. Signal
// zero distinguishes a missing PID from a live process hidden by procfs.
func ProcessTerminated(pid int, start, domain string) (bool, error) {
	currentDomain, err := ProcessDomain()
	if err != nil || currentDomain != domain || pid <= 1 || start == "" {
		return false, ErrProcessDomainUnavailable
	}
	observed, err := ProcessStart(pid)
	if err == nil {
		return observed != start, nil
	}
	if !errors.Is(err, ErrNoProcess) {
		return false, err
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}
