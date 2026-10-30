//go:build unix

package store_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRepairBackupPermissions(t *testing.T) {
	if scenario := os.Getenv("ENGRAM_BACKUP_PERMISSION_CASE"); scenario != "" {
		parts := strings.Split(scenario, "/")
		mask := 0022
		if parts[2] == "000" {
			mask = 0
		}
		// Only the isolated child changes its process-global umask.
		syscall.Umask(mask)
		existing := parts[1] == "existing"
		backup, data := exerciseRepairBackup(t, parts[0] == "locked", existing)
		for _, want := range []struct {
			path string
			mode os.FileMode
		}{{backup, 0600}, {filepath.Join(data, "engram.db"), 0600}} {
			info, err := os.Stat(want.path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != want.mode {
				t.Errorf("%s permissions=%04o, want %04o", want.path, info.Mode().Perm(), want.mode)
			}
		}
		info, err := os.Stat(filepath.Dir(backup))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0700)
		if existing {
			want = 0755
		}
		if info.Mode().Perm() != want {
			t.Errorf("backup directory permissions=%04o, want %04o", info.Mode().Perm(), want)
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"ordinary", "locked"} {
		for _, directory := range []string{"new", "existing"} {
			for _, mask := range []string{"000", "022"} {
				scenario := fmt.Sprintf("%s/%s/%s", path, directory, mask)
				t.Run(scenario, func(t *testing.T) {
					cmd := exec.Command(exe, "-test.run=^TestRepairBackupPermissions$", "-test.count=1")
					cmd.Env = append(os.Environ(), "ENGRAM_BACKUP_PERMISSION_CASE="+scenario)
					var stdout, stderr strings.Builder
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					if err := cmd.Run(); err != nil {
						t.Fatalf("child exit: %v\nstdout:\n%sstderr:\n%s", err, stdout.String(), stderr.String())
					}
					if stdout.String() != "PASS\n" || stderr.String() != "" {
						t.Fatalf("unexpected child output: stdout=%q stderr=%q", stdout.String(), stderr.String())
					}
				})
			}
		}
	}
}
