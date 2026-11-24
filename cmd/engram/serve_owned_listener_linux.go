package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func inheritedOwnedListener() (net.Listener, error) {
	if _, present := os.LookupEnv(ownedListenerEnv); !present {
		return nil, nil
	}
	// Do not take ownership of an unrelated runtime descriptor.
	const descriptor = 3
	accepting, err := unix.GetsockoptInt(descriptor, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	if err != nil || accepting != 1 {
		return nil, errors.New("session-owned descriptor 3 is not a listening socket")
	}
	local, err := unix.Getsockname(descriptor)
	if err != nil {
		return nil, fmt.Errorf("read inherited session-owned listener address: %w", err)
	}
	address, ipv4 := local.(*unix.SockaddrInet4)
	if !ipv4 || address.Addr != [4]byte{127, 0, 0, 1} || address.Port == 0 {
		return nil, fmt.Errorf("inherited session-owned listener must be bound to IPv4 loopback (got %T %v)", local, local)
	}
	protocol, err := unix.GetsockoptInt(descriptor, unix.SOL_SOCKET, unix.SO_PROTOCOL)
	if err != nil {
		return nil, fmt.Errorf("read inherited session-owned listener protocol: %w", err)
	}
	// Go's TCP listener can use MPTCP when the kernel supports it.
	if protocol != unix.IPPROTO_TCP && protocol != unix.IPPROTO_MPTCP {
		return nil, fmt.Errorf("inherited session-owned listener must use TCP (got protocol %d)", protocol)
	}
	file := os.NewFile(descriptor, "session-owned listener")
	defer func() { _ = file.Close() }()
	listener, err := net.FileListener(file)
	if err != nil {
		return nil, fmt.Errorf("open inherited session-owned listener: %w", err)
	}
	return listener, nil
}

// The kernel releases this lease at process exit. A bounded HTTP shutdown
// may return while request handlers are still unwinding.
var ownedStoreLease *os.File

const ownedStoreLeaseName = ".owned-core.lock"

func retainOwnedStoreLease(ctx context.Context, dataDir string) (err error) {
	if ownedStoreLease != nil {
		return errors.New("session-owned Core already holds a store lease")
	}
	if err := checkOwnedStoreAncestors(dataDir); err != nil {
		return err
	}
	dirFD, err := unix.Open(dataDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open session-owned store directory: %w", err)
	}
	dir := os.NewFile(uintptr(dirFD), dataDir)
	defer func() { _ = dir.Close() }()
	info, err := dir.Stat()
	if err != nil || !privateOwnedFile(info) {
		return errors.Join(errors.New("session-owned store directory must be private and owner-controlled"), err)
	}
	lockFD, err := unix.Openat(dirFD, ownedStoreLeaseName, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return fmt.Errorf("open session-owned store lease: %w", err)
	}
	lock := os.NewFile(uintptr(lockFD), ownedStoreLeaseName)
	defer func() {
		if err != nil {
			err = errors.Join(err, lock.Close())
		}
	}()
	lockInfo, err := lock.Stat()
	if err != nil {
		return err
	}
	if !lockInfo.Mode().IsRegular() || !privateOwnedFile(lockInfo) {
		return errors.New("session-owned store lease must be a private owned regular file")
	}

	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			// The directory and lock must still name the objects opened
			// before the predecessor released the lease.
			dirNow, err := os.Lstat(dataDir)
			if err != nil || !os.SameFile(info, dirNow) || !privateOwnedFile(dirNow) {
				return errors.Join(errors.New("session-owned store directory changed while waiting for the lease"), err)
			}
			lockNow, err := os.Lstat(filepath.Join(dataDir, ownedStoreLeaseName))
			if err != nil || !os.SameFile(lockInfo, lockNow) || !privateOwnedFile(lockNow) {
				return errors.Join(errors.New("session-owned store lease file changed while waiting"), err)
			}
			ownedStoreLease = lock
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func checkOwnedStoreAncestors(dataDir string) error {
	// Walk from a trusted root. A sticky shared directory such as /tmp
	// protects its next entry when the entry belongs to root or this user.
	path := "/"
	for _, component := range strings.Split(strings.TrimPrefix(filepath.Dir(dataDir), "/"), "/") {
		path = filepath.Join(path, component)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || !trustedOwnedStoreAncestor(info) {
			return errors.New("session-owned store requires trusted parent directories without symlinks")
		}
	}
	return nil
}

func trustedOwnedStoreAncestor(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (stat.Uid == 0 || stat.Uid == uint32(os.Geteuid())) &&
		(info.Mode().Perm()&0022 == 0 || info.Mode()&os.ModeSticky != 0)
}

func privateOwnedFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0077 == 0
}
