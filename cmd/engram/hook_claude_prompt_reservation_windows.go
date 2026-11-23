package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

// reserveClaudeWindowsPrompt keeps an exclusive, noninherited handle until
// output completes. Windows removes the reservation when the process exits.
// Other filesystem errors leave the normal bootstrap available.
func reserveClaudeWindowsPrompt(path string) (release func(), inProgress bool) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_DELETE_ON_CLOSE, 0)
	if err != nil {
		if !errors.Is(err, windows.ERROR_FILE_EXISTS) {
			return nil, errors.Is(err, windows.ERROR_SHARING_VIOLATION)
		}
		// A pathname alone does not prove that a process holds the reservation.
		probe, err := windows.CreateFile(name, windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
			windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err == nil {
			_ = windows.CloseHandle(probe)
		}
		return nil, errors.Is(err, windows.ERROR_SHARING_VIOLATION)
	}
	return func() { _ = windows.CloseHandle(handle) }, false
}
