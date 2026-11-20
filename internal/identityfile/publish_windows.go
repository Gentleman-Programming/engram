//go:build windows

package identityfile

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// Publish exposes a closed, complete temporary file without replacing a winner.
// MoveFileEx without MOVEFILE_REPLACE_EXISTING is an atomic no-replace rename
// that also works on volumes without hard links (exFAT, FAT). It consumes the
// temporary file on success; callers own cleanup and tolerate its absence. An
// existing destination yields an error satisfying errors.Is(err, fs.ErrExist).
func Publish(temporary, destination string) error {
	from, err := syscall.UTF16PtrFromString(temporary)
	if err != nil {
		return &os.LinkError{Op: "movefileex", Old: temporary, New: destination, Err: err}
	}
	to, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return &os.LinkError{Op: "movefileex", Old: temporary, New: destination, Err: err}
	}
	if err := windows.MoveFileEx(from, to, 0); err != nil {
		return &os.LinkError{Op: "movefileex", Old: temporary, New: destination, Err: err}
	}
	return nil
}
