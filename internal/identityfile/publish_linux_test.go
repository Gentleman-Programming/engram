package identityfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPublishLinuxUsesNoReplaceRename(t *testing.T) {
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "temporary"), filepath.Join(dir, "identity")
	if err := os.WriteFile(source, []byte("complete\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Probe actual kernel/filesystem support separately so legacy compatibility
	// cannot accidentally make a hard-link-only implementation pass this test.
	probe := filepath.Join(dir, "probe")
	if err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, probe, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
			t.Skipf("no-replace rename unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := Publish(probe, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(probe); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rename did not consume source: %v", err)
	}
}

func TestPublishLinuxFallbackOnlyUnsupported(t *testing.T) {
	t.Run("unsupported rename with successful link", func(t *testing.T) {
		calls := 0
		rename := func(oldfd int, old string, newfd int, new string, flags uint) error {
			if oldfd != unix.AT_FDCWD || newfd != unix.AT_FDCWD || old != "source" || new != "destination" || flags != unix.RENAME_NOREPLACE {
				t.Fatal("incorrect no-replace invocation")
			}
			return unix.ENOSYS
		}
		link := func(old, new string) error {
			calls++
			if old != "source" || new != "destination" {
				t.Fatalf("link paths = %q, %q, want source, destination", old, new)
			}
			return nil
		}
		if err := publish("source", "destination", rename, link); err != nil {
			t.Fatalf("successful fallback returned error: %v", err)
		}
		if calls != 1 {
			t.Fatalf("fallback calls = %d, want 1", calls)
		}
	})

	for _, errno := range []error{nil, unix.ENOSYS, unix.EINVAL, unix.EOPNOTSUPP, unix.EPERM, unix.EACCES, unix.EEXIST, unix.EXDEV, unix.ENOENT} {
		name := "success"
		if errno != nil {
			name = errno.Error()
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			rename := func(oldfd int, old string, newfd int, new string, flags uint) error {
				if oldfd != unix.AT_FDCWD || newfd != unix.AT_FDCWD || old != "source" || new != "destination" || flags != unix.RENAME_NOREPLACE {
					t.Fatal("incorrect no-replace invocation")
				}
				return errno
			}
			link := func(old, new string) error {
				calls++
				return unix.EACCES
			}
			err := publish("source", "destination", rename, link)
			unsupported := errno == unix.ENOSYS || errno == unix.EINVAL || errno == unix.EOPNOTSUPP
			if unsupported {
				if calls != 1 || !errors.Is(err, unix.EACCES) {
					t.Fatalf("fallback calls = %d, error = %v", calls, err)
				}
			} else {
				if calls != 0 {
					t.Fatal("fell back for ordinary error")
				}
				if errno == nil && err != nil || errno != nil && !errors.Is(err, errno) {
					t.Fatalf("error = %v, want %v", err, errno)
				}
			}
		})
	}
}
