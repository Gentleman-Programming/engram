package cloudstore

import (
	"os"
	"path/filepath"
	"syscall"
)

func openPrivateReceiptDirectory(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) {
		return nil, errReceiptAppendUnproven
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !privateReceiptDirectory(info) {
		return nil, errReceiptAppendUnproven
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !privateReceiptDirectory(opened) || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, errReceiptAppendUnproven
	}
	return root, nil
}

func privateReceiptDirectory(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.IsDir() && info.Mode().Perm()&0o077 == 0
}

func syncReceiptDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
