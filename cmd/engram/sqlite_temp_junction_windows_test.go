package main

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSQLiteTempDataRootWithoutDriveLetter(t *testing.T) {
	const path = `\\?\Volume{11111111-1111-1111-1111-111111111111}\engram`
	var mode []uint32
	resolved, err := sqliteTempRootPath(0, func(_ windows.Handle, out *uint16, size, flags uint32) (uint32, error) {
		mode = append(mode, flags)
		if flags == 0 {
			return 0, windows.ERROR_PATH_NOT_FOUND
		}
		utf16, err := windows.UTF16FromString(path)
		if err != nil {
			return 0, err
		}
		copy(unsafe.Slice(out, int(size)), utf16)
		return uint32(len(utf16) - 1), nil
	})
	if err != nil || resolved != path || len(mode) != 2 || mode[1] != 1 {
		t.Fatalf("resolved = %q, modes = %v, error = %v", resolved, mode, err)
	}
}

func TestSQLiteTempRejectsManagedJunctions(t *testing.T) {
	for _, relative := range []string{"tmp", filepath.Join("tmp", "sqlite")} {
		t.Run(relative, func(t *testing.T) {
			root, target := t.TempDir(), t.TempDir()
			link := filepath.Join(root, relative)
			makeSQLiteTestJunction(t, link, target)
			release, err := prepareSQLiteTemp(root)
			if release != nil {
				release()
			}
			if err == nil || !strings.Contains(err.Error(), "path must be an ordinary directory") {
				t.Fatalf("managed junction accepted: %v", err)
			}
			children, err := os.ReadDir(target)
			if err != nil || len(children) != 0 {
				t.Fatalf("junction target changed: %v, %v", children, err)
			}
		})
	}
}

func TestSQLiteTempResolvesConfiguredDataJunction(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	t.Setenv("TEMP", root)
	t.Setenv("TMP", root)
	link := filepath.Join(root, "data")
	makeSQLiteTestJunction(t, link, target)
	release, err := prepareSQLiteTemp(link)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	physical, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(physical, "tmp", "sqlite")
	for _, key := range []string{"TEMP", "TMP"} {
		if got := os.Getenv(key); !strings.EqualFold(got, want) {
			t.Errorf("%s = %q, want physical path %q", key, got, want)
		}
	}
}

func TestSQLiteTempRejectsInPlaceJunctionUntilRelease(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	t.Setenv("TEMP", root)
	t.Setenv("TMP", root)
	release, err := prepareSQLiteTemp(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("TEMP")
	mutationErr := setSQLiteTestJunction(dir, target)
	release()
	if !errors.Is(mutationErr, windows.ERROR_DIR_NOT_EMPTY) {
		t.Fatalf("in-place reparse result: %v, want directory not empty", mutationErr)
	}
	if err := setSQLiteTestJunction(dir, target); err != nil {
		t.Fatalf("empty directory control after handle release: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Remove(dir); err != nil {
			t.Error(err)
		}
	})
}

func makeSQLiteTestJunction(t *testing.T, path, target string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := setSQLiteTestJunction(path, target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
	})
}

func setSQLiteTestJunction(path, target string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	substitute, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		return err
	}
	display, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	// The mount-point payload holds byte offsets into a UTF-16 path buffer.
	header := []uint16{uint16(8 + 2*(len(substitute)+len(display))), 0,
		0, uint16(2 * (len(substitute) - 1)), uint16(2 * len(substitute)), uint16(2 * (len(display) - 1))}
	buffer := binary.LittleEndian.AppendUint32(nil, windows.IO_REPARSE_TAG_MOUNT_POINT)
	for _, words := range [][]uint16{header, substitute, display} {
		for _, word := range words {
			buffer = binary.LittleEndian.AppendUint16(buffer, word)
		}
	}
	handle, err := windows.CreateFile(name, windows.FILE_WRITE_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var returned uint32
	return windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, &buffer[0],
		uint32(len(buffer)), nil, 0, &returned, nil)
}
