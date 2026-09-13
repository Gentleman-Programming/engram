//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import "os"

func replaceInitConfigFile(temporaryPath, configPath string) error {
	return os.Rename(temporaryPath, configPath)
}
