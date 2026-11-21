//go:build !windows

package project

import "os"

func readRepositoryBindingFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
