//go:build windows

package store

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

type databaseFileIdentity string

func databaseFileID(path string) (databaseFileIdentity, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return "", err
	}
	return databaseFileIdentity(fmt.Sprintf("%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)), nil
}
