//go:build !windows

package store

import (
	"errors"
	"os"
)

func openPublishedInstanceID(string) (*os.File, error) {
	return nil, errors.ErrUnsupported
}
