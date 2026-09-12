//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type windowsInitConfigDirectory struct {
	path   string
	handle windows.Handle
}

func openStableInitConfigDirectory(path string) (stableInitConfigDirectory, error) {
	if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create .engram directory: %w", err)
	}

	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("inspect .engram directory: %w", err)
	}

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("inspect .engram directory: %w", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf(".engram must be a real directory, not a symlink")
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf(".engram must be a directory")
	}
	return &windowsInitConfigDirectory{path: path, handle: handle}, nil
}

func (d *windowsInitConfigDirectory) close() error {
	return windows.CloseHandle(d.handle)
}

func (d *windowsInitConfigDirectory) inspect(name string) (bool, bool, error) {
	info, err := os.Lstat(filepath.Join(d.path, name))
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, info.Mode()&os.ModeSymlink != 0, nil
}

func (d *windowsInitConfigDirectory) create(name string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(filepath.Join(d.path, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
}

func (d *windowsInitConfigDirectory) createTemp(pattern string, _ os.FileMode) (*os.File, string, error) {
	file, err := os.CreateTemp(d.path, pattern)
	if err != nil {
		return nil, "", err
	}
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, "", err
	}
	return file, filepath.Base(file.Name()), nil
}

func (d *windowsInitConfigDirectory) remove(name string) error {
	return os.Remove(filepath.Join(d.path, name))
}

func (d *windowsInitConfigDirectory) rename(oldName, newName string) error {
	return os.Rename(filepath.Join(d.path, oldName), filepath.Join(d.path, newName))
}
