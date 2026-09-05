//go:build unix

package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// secureUnixSocketPath resolves the parent hierarchy before bind so every
// directory that can name the socket is safe from untrusted replacement.
func secureUnixSocketPath(socketPath string) (string, error) {
	parent, err := filepath.Abs(filepath.Dir(socketPath))
	if err != nil {
		return "", fmt.Errorf("engram server: resolve socket parent: %w", err)
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("engram server: resolve socket parent: %w", err)
	}
	if err := validateUnixSocketParent(parent); err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(socketPath)), nil
}

func validateUnixSocketParent(parent string) error {
	relative, err := filepath.Rel(string(os.PathSeparator), parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("engram server: socket parent is not an absolute Unix path")
	}

	component := string(os.PathSeparator)
	for _, name := range strings.Split(relative, string(os.PathSeparator)) {
		if name != "." {
			component = filepath.Join(component, name)
		}
		info, err := os.Lstat(component)
		if err != nil {
			return fmt.Errorf("engram server: cannot inspect socket parent directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("engram server: socket parent hierarchy contains an unsafe directory")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !trustedUnixSocketDirectory(info.Mode(), int(stat.Uid), os.Geteuid()) {
			return fmt.Errorf("engram server: socket parent hierarchy is writable by an untrusted user")
		}
	}
	return nil
}

func trustedUnixSocketDirectory(mode os.FileMode, ownerUID, effectiveUID int) bool {
	if mode.Perm()&0o022 == 0 {
		return true
	}
	return mode&os.ModeSticky != 0 && (ownerUID == 0 || ownerUID == effectiveUID)
}

// listenSecureUnixSocket binds the socket, restricts its permissions, and only
// then starts accepting connections. This avoids a process-global umask race
// and the interval where net.Listen has already made the socket reachable.
func listenSecureUnixSocket(socketPath string) (net.Listener, os.FileInfo, error) {
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: socketPath}); err != nil {
		_ = syscall.Close(fd)
		return nil, nil, err
	}

	info, err := os.Lstat(socketPath)
	if err != nil {
		_ = syscall.Close(fd)
		return nil, nil, err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = syscall.Close(fd)
		_ = removeOwnedUnixSocket(socketPath, info)
		return nil, nil, err
	}
	if err := syscall.Listen(fd, syscall.SOMAXCONN); err != nil {
		_ = syscall.Close(fd)
		_ = removeOwnedUnixSocket(socketPath, info)
		return nil, nil, err
	}

	file := os.NewFile(uintptr(fd), "engram-unix-socket")
	ln, err := net.FileListener(file)
	_ = file.Close()
	if err != nil {
		_ = removeOwnedUnixSocket(socketPath, info)
		return nil, nil, err
	}
	return ln, info, nil
}
