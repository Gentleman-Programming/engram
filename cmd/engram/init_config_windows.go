//go:build windows

package main

import "golang.org/x/sys/windows"

func replaceInitConfigFile(temporaryPath, configPath string) error {
	return windows.MoveFileEx(
		windows.StringToUTF16Ptr(temporaryPath),
		windows.StringToUTF16Ptr(configPath),
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}
