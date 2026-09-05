//go:build unix

package server

import (
	"net"
	"os"
	"syscall"
)

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
