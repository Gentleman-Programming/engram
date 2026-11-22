//go:build !linux && !windows

package hostbind

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ErrNoProcess reports that the PID does not name a running process.
var ErrNoProcess = errors.New("hostbind: no such process")

// ProcessStart returns the start time of a live pid as reported by ps(1), which every
// supported non-Linux Unix provides.
func ProcessStart(pid int) (string, error) {
	cmd := exec.Command("ps", "-o", "state=", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", ErrNoProcess
		}
		return "", err
	}
	line := strings.TrimSpace(string(out))
	stateEnd := strings.IndexAny(line, " \t")
	if stateEnd <= 0 {
		return "", ErrNoProcess
	}
	// ps reports a zombie's state and original start time until it is reaped.
	if line[0] == 'Z' || line[0] == 'X' {
		return "", ErrNoProcess
	}
	start := strings.TrimSpace(line[stateEnd:])
	if start == "" {
		return "", ErrNoProcess
	}
	return start, nil
}
