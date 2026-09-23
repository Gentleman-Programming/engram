//go:build windows

package store

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

var (
	resolveWindowsPath           = resolveWindowsFinalPath
	windowsDriveType             = getWindowsDriveType
	windowsFinalPathNameByHandle = windows.GetFinalPathNameByHandle
)

const (
	windowsDriveRemote uint32 = 4
	windowsDriveFixed  uint32 = 3
)

func classifyWindowsDriveType(driveType uint32) filesystemInfo {
	switch driveType {
	case windowsDriveRemote:
		return filesystemInfo{Type: "remote volume", Support: filesystemRemote}
	case windowsDriveFixed:
		return filesystemInfo{Type: "fixed volume", Support: filesystemLocal}
	default:
		return filesystemInfo{Type: fmt.Sprintf("drive type %d", driveType), Support: filesystemUnknown}
	}
}

func detectFilesystem(path string) (filesystemInfo, error) {
	resolvedPath, err := resolveWindowsPath(path)
	if err != nil {
		return filesystemInfo{}, err
	}
	driveType, err := windowsDriveType(filepath.VolumeName(resolvedPath) + `\`)
	if err != nil {
		return filesystemInfo{}, err
	}
	return classifyWindowsDriveType(driveType), nil
}

func resolveWindowsFinalPath(path string) (string, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(pathPtr, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	size := uint32(windows.MAX_PATH)
	for {
		buffer := make([]uint16, size)
		n, err := windowsFinalPathNameByHandle(handle, &buffer[0], size, 0x8) // FILE_NAME_OPENED, VOLUME_NAME_DOS
		if err != nil {
			return "", err
		}
		if n < size {
			return normalizeWindowsFinalPath(windows.UTF16ToString(buffer[:n])), nil
		}
		size = n + 1
	}
}

func normalizeWindowsFinalPath(path string) string {
	if strings.HasPrefix(path, `\\?\UNC\`) {
		return `\\` + path[len(`\\?\UNC\`):]
	}
	return strings.TrimPrefix(path, `\\?\`)
}

func getWindowsDriveType(root string) (uint32, error) {
	rootPath, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, err
	}
	return windows.GetDriveType(rootPath), nil
}
