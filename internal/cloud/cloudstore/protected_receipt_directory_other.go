//go:build !linux

package cloudstore

import "os"

func openPrivateReceiptDirectory(string) (*os.Root, error) {
	return nil, errReceiptAppendUnproven
}

// Non-Linux builds can exercise the file format in tagged tests.
func syncReceiptDirectory(*os.Root) error { return nil }
