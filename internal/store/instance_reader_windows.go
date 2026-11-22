package store

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func openPublishedInstanceID(dataDir string) (*os.File, error) {
	// A concurrent repair must be able to reuse the old name before this
	// reader closes. Restrict the fast path to Windows 11 volumes that
	// advertise POSIX unlink support.
	const (
		windows11Build                = 22000
		fileSupportsPosixUnlinkRename = 0x00000400
	)
	if windows.RtlGetVersion().BuildNumber < windows11Build {
		return nil, errors.ErrUnsupported
	}
	dir, err := os.Open(dataDir)
	if err != nil {
		return nil, err
	}
	var flags uint32
	err = windows.GetVolumeInformationByHandle(windows.Handle(dir.Fd()), nil, 0, nil, nil, &flags, nil, 0)
	_ = dir.Close()
	if err != nil {
		return nil, err
	}
	if flags&fileSupportsPosixUnlinkRename == 0 {
		return nil, errors.ErrUnsupported
	}
	const marker = ".instance-id"
	info, err := os.Lstat(filepath.Join(dataDir, marker))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.ErrUnsupported
	}
	// The root-relative open shares delete access, unlike ordinary os.Open.
	return os.OpenInRoot(dataDir, marker)
}
