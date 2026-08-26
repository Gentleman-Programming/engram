//go:build unix

package store

import (
	"fmt"
	"os"
	"syscall"
)

type databaseFileIdentity string

func databaseFileID(path string) (databaseFileIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("read file identity for %s", path)
	}
	return databaseFileIdentity(fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)), nil
}
