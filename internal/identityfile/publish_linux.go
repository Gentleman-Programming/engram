package identityfile

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Publish exposes a closed, complete temporary file without replacing a winner.
// Go also selects Linux files for Android. No hard link is required on modern
// Linux/Termux filesystems. Callers own temporary-file cleanup (rename consumes
// the source, whereas the compatibility hard-link path retains it).
func Publish(temporary, destination string) error {
	return publish(temporary, destination, unix.Renameat2, os.Link)
}

func publish(temporary, destination string, rename func(int, string, int, string, uint) error, link func(string, string) error) error {
	err := rename(unix.AT_FDCWD, temporary, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
	// Older kernels or filesystems may not implement this syscall/flag. Only
	// unsupported-operation errors permit the old atomic hard-link strategy;
	// permission errors must survive, and ordinary rename would overwrite winners.
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return link(temporary, destination)
	}
	if err != nil {
		return &os.LinkError{Op: "renameat2", Old: temporary, New: destination, Err: err}
	}
	return nil
}
