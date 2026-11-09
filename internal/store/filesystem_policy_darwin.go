//go:build darwin

package store

import (
	"strings"
	"syscall"
)

func classifyFilesystemType(filesystem string) filesystemInfo {
	switch strings.ToLower(strings.TrimSpace(filesystem)) {
	case "apfs", "btrfs", "ext2", "ext3", "ext4", "exfat", "fat", "fat32", "hfs", "hfs+", "ntfs", "refs", "tmpfs", "ufs", "xfs", "zfs":
		return filesystemInfo{Type: filesystem, Support: filesystemLocal}
	case "nfs", "nfs4":
		return filesystemInfo{Type: "NFS", Support: filesystemRemote}
	case "cifs", "smb", "smb2", "smbfs":
		return filesystemInfo{Type: "SMB/CIFS", Support: filesystemRemote}
	case "afpfs", "webdav":
		return filesystemInfo{Type: filesystem, Support: filesystemRemote}
	default:
		return filesystemInfo{Type: filesystem, Support: filesystemUnknown}
	}
}

func classifyDarwinFilesystemType(filesystem string) filesystemInfo {
	return classifyFilesystemType(filesystem)
}

func detectFilesystem(path string) (filesystemInfo, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return filesystemInfo{}, err
	}
	name := make([]byte, len(stat.Fstypename))
	for i, char := range stat.Fstypename {
		name[i] = byte(char)
	}
	return classifyDarwinFilesystemType(strings.TrimRight(string(name), "\x00")), nil
}
