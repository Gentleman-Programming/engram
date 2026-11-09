//go:build !linux && !darwin && !windows

package store

// detectFilesystem preserves compatibility on unsupported platforms where
// Engram has no platform API capable of proving a filesystem is remote.
func detectFilesystem(string) (filesystemInfo, error) {
	return filesystemInfo{Type: "unclassified", Support: filesystemUnknown}, nil
}
