//go:build linux

package hostbind

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ErrNoProcess reports that the PID does not name a running process.
var ErrNoProcess = errors.New("hostbind: no such process")

// ProcessStart returns the kernel start time (clock ticks since boot) of pid.
func ProcessStart(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNoProcess
		}
		return "", err
	}
	// The command name is parenthesized and may contain spaces or ')', so the
	// numeric fields are counted from the last ')'. starttime is field 22; the
	// remainder after ')' starts at field 3.
	text := string(data)
	idx := strings.LastIndexByte(text, ')')
	if idx < 0 {
		return "", fmt.Errorf("hostbind: unexpected /proc stat format")
	}
	fields := strings.Fields(text[idx+1:])
	if len(fields) < 20 {
		return "", fmt.Errorf("hostbind: unexpected /proc stat format")
	}
	return fields[19], nil
}
