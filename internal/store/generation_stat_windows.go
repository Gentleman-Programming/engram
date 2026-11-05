//go:build windows

package store

import (
	"os"

	"golang.org/x/sys/windows"
)

// statGenerationFile captures file identity eagerly. os.Stat on Windows reads
// attributes first and defers another pathname open until os.SameFile.
func statGenerationFile(path string) (os.FileInfo, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return os.Stat(path)
	}
	// Metadata-only access and exclusive sharing match os.SameFile's identity
	// lookup. Follow symlink targets, as os.Stat does, without reading content.
	handle, err := windows.CreateFile(name, 0, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		// Preserve Go's path handling and the existing classification of errors,
		// including sharing failures and long paths unsupported by this fast path.
		return os.Stat(path)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return os.Stat(path)
	}
	info, err := file.Stat()
	_ = file.Close()
	if err != nil {
		return os.Stat(path)
	}
	return info, nil
}
