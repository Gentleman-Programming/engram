//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

var initConfigTempSequence uint64

type unixInitConfigDirectory struct {
	fd int
}

func openStableInitConfigDirectory(path string) (stableInitConfigDirectory, error) {
	if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create .engram directory: %w", err)
	}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf(".engram must be a real directory, not a symlink")
		}
		if errors.Is(err, unix.ENOTDIR) {
			return nil, fmt.Errorf(".engram must be a directory")
		}
		return nil, fmt.Errorf("inspect .engram directory: %w", err)
	}
	return &unixInitConfigDirectory{fd: fd}, nil
}

func (d *unixInitConfigDirectory) close() error {
	return unix.Close(d.fd)
}

func (d *unixInitConfigDirectory) inspect(name string) (bool, bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(d.fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, stat.Mode&unix.S_IFMT == unix.S_IFLNK, nil
}

func (d *unixInitConfigDirectory) create(name string, perm os.FileMode) (*os.File, error) {
	fd, err := unix.Openat(d.fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (d *unixInitConfigDirectory) createTemp(pattern string, perm os.FileMode) (*os.File, string, error) {
	for range 100 {
		name := fmt.Sprintf("%s%d-%d", pattern[:len(pattern)-1], os.Getpid(), atomic.AddUint64(&initConfigTempSequence, 1))
		file, err := d.create(name, perm)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return file, name, nil
	}
	return nil, "", fmt.Errorf("allocate unique temporary config name")
}

func (d *unixInitConfigDirectory) remove(name string) error {
	return unix.Unlinkat(d.fd, name, 0)
}

func (d *unixInitConfigDirectory) rename(oldName, newName string) error {
	return unix.Renameat(d.fd, oldName, d.fd, newName)
}
