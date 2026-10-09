package store

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

func TestCheckDataDirectoryFilesystemRejectsKnownRemote(t *testing.T) {
	t.Setenv(EnvAllowUnsafeNFS, "0")
	setFilesystemInspector(t, func(string) (filesystemInfo, error) {
		return filesystemInfo{Type: "NFS", Support: filesystemRemote}, nil
	})

	err := checkDataDirectoryFilesystem(t.TempDir())
	var rejection *NetworkFilesystemError
	if !errors.As(err, &rejection) {
		t.Fatalf("checkDataDirectoryFilesystem error = %v, want NetworkFilesystemError", err)
	}
	if rejection.Filesystem != "NFS" {
		t.Errorf("rejection filesystem = %q, want NFS", rejection.Filesystem)
	}
	if !strings.Contains(err.Error(), "ENGRAM_DATA_DIR") {
		t.Errorf("rejection does not require ENGRAM_DATA_DIR: %v", err)
	}
	if !strings.Contains(err.Error(), EnvAllowUnsafeNFS) {
		t.Errorf("rejection does not mention %s: %v", EnvAllowUnsafeNFS, err)
	}
}

func TestCheckDataDirectoryFilesystemBypassWithAllowUnsafeNFS(t *testing.T) {
	setFilesystemInspector(t, func(string) (filesystemInfo, error) {
		return filesystemInfo{Type: "NFS", Support: filesystemRemote}, nil
	})

	truthyValues := []string{"1", "true", "TRUE", "yes"}
	for _, val := range truthyValues {
		t.Run("truthy_"+val, func(t *testing.T) {
			t.Setenv(EnvAllowUnsafeNFS, val)

			var buf bytes.Buffer
			oldLog := log.Writer()
			log.SetOutput(&buf)
			defer log.SetOutput(oldLog)

			dir := t.TempDir()
			if err := checkDataDirectoryFilesystem(dir); err != nil {
				t.Fatalf("checkDataDirectoryFilesystem with %s=%q error = %v, want nil", EnvAllowUnsafeNFS, val, err)
			}

			output := buf.String()
			if !strings.Contains(output, "WARNING") {
				t.Errorf("expected warning in log output; got: %q", output)
			}
			if !strings.Contains(output, EnvAllowUnsafeNFS) {
				t.Errorf("expected log output to mention %s; got: %q", EnvAllowUnsafeNFS, output)
			}
			if !strings.Contains(output, "NFS") {
				t.Errorf("expected log output to mention filesystem type NFS; got: %q", output)
			}
		})
	}

	falsyValues := []string{"0", "false", "no", "", "invalid"}
	for _, val := range falsyValues {
		t.Run("falsy_"+val, func(t *testing.T) {
			t.Setenv(EnvAllowUnsafeNFS, val)

			err := checkDataDirectoryFilesystem(t.TempDir())
			var rejection *NetworkFilesystemError
			if !errors.As(err, &rejection) {
				t.Fatalf("checkDataDirectoryFilesystem with %s=%q error = %v, want NetworkFilesystemError", EnvAllowUnsafeNFS, val, err)
			}
			if rejection.Filesystem != "NFS" {
				t.Errorf("rejection filesystem = %q, want NFS", rejection.Filesystem)
			}
		})
	}
}

func TestCheckDataDirectoryFilesystemPreservesUnknownCompatibility(t *testing.T) {
	setFilesystemInspector(t, func(string) (filesystemInfo, error) {
		return filesystemInfo{Type: "mysteryfs", Support: filesystemUnknown}, nil
	})

	if err := checkDataDirectoryFilesystem(t.TempDir()); err != nil {
		t.Fatalf("checkDataDirectoryFilesystem unknown filesystem: %v", err)
	}
}

func setFilesystemInspector(t *testing.T, inspector func(string) (filesystemInfo, error)) {
	t.Helper()
	original := filesystemInspector
	filesystemInspector = inspector
	t.Cleanup(func() { filesystemInspector = original })
}
