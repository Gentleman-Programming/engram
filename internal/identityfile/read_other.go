//go:build !windows

package identityfile

import "os"

// ReadFile reads a published identity file.
func ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
