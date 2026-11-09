//go:build !linux

package identityfile

import "os"

// Publish exposes a closed, complete temporary file without replacing a winner.
// Callers own temporary-file cleanup. Platforms without Linux no-replace rename
// retain the existing atomic hard-link publication semantics.
func Publish(temporary, destination string) error {
	return os.Link(temporary, destination)
}
