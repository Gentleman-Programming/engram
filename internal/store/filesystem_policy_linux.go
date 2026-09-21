//go:build linux

package store

import (
	"fmt"
	"syscall"
)

const (
	linuxNFSFilesystemMagic  int64 = 0x6969
	linuxCIFSFilesystemMagic int64 = 0xFF534D42
	linuxSMB2FilesystemMagic int64 = 0xFE534D42
)

func classifyLinuxFilesystemMagic(magic int64) filesystemInfo {
	switch magic {
	case linuxNFSFilesystemMagic:
		return filesystemInfo{Type: "NFS", Support: filesystemRemote}
	case linuxCIFSFilesystemMagic, linuxSMB2FilesystemMagic:
		return filesystemInfo{Type: "SMB/CIFS", Support: filesystemRemote}
	default:
		return filesystemInfo{Type: fmt.Sprintf("filesystem magic %#x", magic), Support: filesystemUnknown}
	}
}

func detectFilesystem(path string) (filesystemInfo, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return filesystemInfo{}, err
	}
	return classifyLinuxFilesystemMagic(int64(stat.Type)), nil
}
