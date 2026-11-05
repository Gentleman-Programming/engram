//go:build !windows

package store

import "os"

func statGenerationFile(path string) (os.FileInfo, error) {
	return os.Stat(path)
}
