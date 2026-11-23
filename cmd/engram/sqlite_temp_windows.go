package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"golang.org/x/sys/windows"
)

// prepareSQLiteTemp configures this process and retains the directories until
// its local SQLite command returns.
func prepareSQLiteTemp(dataDir string) (release func(), err error) {
	if err := store.ValidateDataDirectory(dataDir); err != nil {
		return nil, fmt.Errorf("prepare SQLite temp directory: %s", strings.TrimPrefix(err.Error(), "engram: "))
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("engram: create SQLite temporary directory in %q: %w", dataDir, err)
	}
	// A configured data directory may use a junction. Pin the physical temp
	// location so SQLite's later opens cannot follow a retargeted alias.
	rootHandle, root, err := openSQLiteTempRoot(dataDir)
	if err != nil {
		return nil, fmt.Errorf("engram: resolve SQLite temporary storage root: %w", err)
	}
	defer func() {
		if err != nil {
			_ = windows.CloseHandle(rootHandle)
		}
	}()
	if err := store.ValidateDataDirectory(root); err != nil {
		return nil, err
	}
	parent := filepath.Join(root, "tmp")
	parentHandle, err := openSQLiteTempDirectory(parent)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = windows.CloseHandle(parentHandle)
		}
	}()
	dir := filepath.Join(parent, "sqlite")
	handle, err := openSQLiteTempDirectory(dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = windows.CloseHandle(handle)
		}
	}()

	// A nonempty directory also prevents in-place conversion to a junction.
	// This per-process marker probes writes and is deleted on handle close,
	// including process termination.
	markerName, err := sqliteTempPath(filepath.Join(dir, ".engram-temp-"+rand.Text()))
	if err != nil {
		return nil, err
	}
	marker, err := windows.CreateFile(markerName, windows.GENERIC_WRITE|windows.DELETE, 0, nil,
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_DELETE_ON_CLOSE, 0)
	if err != nil {
		return nil, fmt.Errorf("engram: check SQLite temporary directory %q: %w", dir, err)
	}
	defer func() {
		if err != nil {
			_ = windows.CloseHandle(marker)
		}
	}()
	if err := checkSQLiteTempDirectory(parentHandle, parent); err != nil {
		return nil, err
	}
	if err := checkSQLiteTempDirectory(handle, dir); err != nil {
		return nil, err
	}
	for _, key := range []string{"TEMP", "TMP"} {
		if err := os.Setenv(key, dir); err != nil {
			return nil, fmt.Errorf("engram: set process %s for SQLite temporaries: %w", key, err)
		}
	}
	return func() {
		_ = windows.CloseHandle(marker)
		_ = windows.CloseHandle(handle)
		_ = windows.CloseHandle(parentHandle)
		_ = windows.CloseHandle(rootHandle)
	}, nil
}

func openSQLiteTempRoot(path string) (handle windows.Handle, pathName string, err error) {
	name, err := sqliteTempPath(path)
	if err != nil {
		return 0, "", err
	}
	opened, err := windows.CreateFile(name, windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, "", err
	}
	defer func() {
		if err != nil {
			_ = windows.CloseHandle(opened)
		}
	}()
	final, err := sqliteTempRootPath(opened, windows.GetFinalPathNameByHandle)
	if err != nil {
		return 0, "", err
	}
	return opened, final, nil
}

func sqliteTempRootPath(handle windows.Handle, lookup func(windows.Handle, *uint16, uint32, uint32) (uint32, error)) (string, error) {
	const volumeNameGUID = 1
	var flags uint32
	size := uint32(windows.MAX_PATH)
	for {
		buffer := make([]uint16, size)
		// FILE_NAME_NORMALIZED with VOLUME_NAME_DOS yields the target path
		// rather than the name of the junction used to open it.
		n, err := lookup(handle, &buffer[0], size, flags)
		if flags == 0 && errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			// A volume mounted at a directory may have no drive letter.
			flags = volumeNameGUID
			continue
		}
		if err != nil {
			return "", err
		}
		if n < size {
			final := windows.UTF16ToString(buffer[:n])
			if flags == volumeNameGUID {
				return final, nil
			}
			if strings.HasPrefix(final, `\\?\UNC\`) {
				final = `\\` + strings.TrimPrefix(final, `\\?\UNC\`)
			}
			return strings.TrimPrefix(final, `\\?\`), nil
		}
		size = n + 1
	}
}

// The Windows syscall wrapper expects an extended path for long filenames.
func sqliteTempPath(path string) (*uint16, error) {
	switch {
	case strings.HasPrefix(path, `\\?\`), strings.HasPrefix(path, `\\.\`):
	case strings.HasPrefix(path, `\\`):
		path = `\\?\UNC\` + path[2:]
	default:
		path = `\\?\` + path
	}
	return windows.UTF16PtrFromString(path)
}

func openSQLiteTempDirectory(path string) (windows.Handle, error) {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return 0, fmt.Errorf("engram: create SQLite temporary directory %q: %w", path, err)
	}
	name, err := sqliteTempPath(path)
	if err != nil {
		return 0, err
	}
	// Windows requires data access to enforce share-delete checks on the
	// directory itself. Listing grants that access without write privileges.
	handle, err := windows.CreateFile(name, windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, fmt.Errorf("engram: open SQLite temporary directory %q: %w", path, err)
	}
	if err := checkSQLiteTempDirectory(handle, path); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func checkSQLiteTempDirectory(handle windows.Handle, path string) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("engram: identify SQLite temporary directory %q: %w", path, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("engram: create SQLite temporary directory %q: path must be an ordinary directory", path)
	}
	return nil
}
