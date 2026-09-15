package version

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMiseInstallsRoot(t *testing.T) {
	tests := []struct {
		name string
		goos string
		env  map[string]string
		want func(t *testing.T) string
	}{
		{
			name: "MISE_INSTALLS_DIR wins over all other sources",
			goos: "linux",
			env: map[string]string{
				"MISE_INSTALLS_DIR": "/opt/mise/installs",
				"MISE_DATA_DIR":     "/opt/data",
				"XDG_DATA_HOME":     "/opt/xdg",
			},
			want: func(t *testing.T) string { return "/opt/mise/installs" },
		},
		{
			name: "MISE_DATA_DIR wins when MISE_INSTALLS_DIR is unset",
			goos: "linux",
			env: map[string]string{
				"MISE_DATA_DIR": "/opt/data",
				"XDG_DATA_HOME": "/opt/xdg",
			},
			want: func(t *testing.T) string { return filepath.Join("/opt/data", "installs") },
		},
		{
			name: "XDG_DATA_HOME wins over the platform default",
			goos: "linux",
			env: map[string]string{
				"XDG_DATA_HOME": "/opt/xdg",
			},
			want: func(t *testing.T) string { return filepath.Join("/opt/xdg", "mise", "installs") },
		},
		{
			name: "whitespace-only MISE_INSTALLS_DIR falls through to the next source",
			goos: "linux",
			env: map[string]string{
				"MISE_INSTALLS_DIR": "   ",
				"XDG_DATA_HOME":     "/opt/xdg",
			},
			want: func(t *testing.T) string { return filepath.Join("/opt/xdg", "mise", "installs") },
		},
		{
			name: "windows resolves LOCALAPPDATA when nothing higher-precedence is set",
			goos: "windows",
			env: map[string]string{
				"LOCALAPPDATA": `C:\Users\dev\AppData\Local`,
			},
			want: func(t *testing.T) string {
				return filepath.Join(`C:\Users\dev\AppData\Local`, "mise", "installs")
			},
		},
		{
			name: "linux falls back to the home directory default",
			goos: "linux",
			env:  map[string]string{},
			want: func(t *testing.T) string {
				home, err := os.UserHomeDir()
				if err != nil {
					t.Fatalf("os.UserHomeDir() error = %v", err)
				}
				return filepath.Join(home, ".local", "share", "mise", "installs")
			},
		},
		{
			name: "windows falls back to the home directory default when LOCALAPPDATA is unset",
			goos: "windows",
			env:  map[string]string{},
			want: func(t *testing.T) string {
				home, err := os.UserHomeDir()
				if err != nil {
					t.Fatalf("os.UserHomeDir() error = %v", err)
				}
				return filepath.Join(home, "AppData", "Local", "mise", "installs")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearMiseEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			want := tt.want(t)
			if got := miseInstallsRoot(tt.goos); got != want {
				t.Errorf("miseInstallsRoot(%q) = %q, want %q", tt.goos, got, want)
			}
		})
	}

	t.Run("empty string when the home directory is unavailable", func(t *testing.T) {
		clearMiseEnv(t)
		withUserHomeDir(t, "", errors.New("no home directory"))

		if got := miseInstallsRoot("linux"); got != "" {
			t.Errorf("miseInstallsRoot(%q) = %q, want empty string", "linux", got)
		}
	})
}

func TestPathContains(t *testing.T) {
	t.Run("path nested under root", func(t *testing.T) {
		root := t.TempDir()
		nested := filepath.Join(root, "go", "1.25.10", "bin")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}

		if !pathContains(root, filepath.Join(nested, "engram")) {
			t.Error("pathContains() = false, want true for a path nested under root")
		}
	})

	t.Run("path equal to root", func(t *testing.T) {
		root := t.TempDir()

		if !pathContains(root, root) {
			t.Error("pathContains() = false, want true when path is root itself")
		}
	})

	t.Run("sibling directory is not contained", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "mise", "installs")
		sibling := filepath.Join(parent, "mise-evil", "installs")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("MkdirAll(root) error = %v", err)
		}
		if err := os.MkdirAll(sibling, 0o755); err != nil {
			t.Fatalf("MkdirAll(sibling) error = %v", err)
		}

		if pathContains(root, filepath.Join(sibling, "engram")) {
			t.Error("pathContains() = true, want false for a sibling directory")
		}
	})

	t.Run("symlinked ancestor resolves to the real root", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks require elevated privileges on windows")
		}

		parent := t.TempDir()
		realRoot := filepath.Join(parent, "real-installs")
		if err := os.MkdirAll(realRoot, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		linkedRoot := filepath.Join(parent, "linked-installs")
		if err := os.Symlink(realRoot, linkedRoot); err != nil {
			t.Fatalf("Symlink() error = %v", err)
		}

		nested := filepath.Join(linkedRoot, "go", "bin", "engram")
		if !pathContains(realRoot, nested) {
			t.Error("pathContains() = false, want true through a symlinked ancestor")
		}
	})

	t.Run("nonexistent root is never contained", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "does-not-exist")

		if pathContains(root, filepath.Join(root, "engram")) {
			t.Error("pathContains() = true, want false for a nonexistent root")
		}
	})
}

func TestRunningBinaryIsMiseManaged(t *testing.T) {
	t.Run("true when the executable lives under the resolved installs root", func(t *testing.T) {
		clearMiseEnv(t)
		root := t.TempDir()
		t.Setenv("MISE_INSTALLS_DIR", root)

		exe := filepath.Join(root, "go", "1.25.10", "bin", "engram")
		withCurrentExecutable(t, exe, nil)

		if !runningBinaryIsMiseManaged() {
			t.Error("runningBinaryIsMiseManaged() = false, want true")
		}
	})

	t.Run("false when the current executable cannot be resolved", func(t *testing.T) {
		clearMiseEnv(t)
		t.Setenv("MISE_INSTALLS_DIR", t.TempDir())
		withCurrentExecutable(t, "", errors.New("executable not found"))

		if runningBinaryIsMiseManaged() {
			t.Error("runningBinaryIsMiseManaged() = true, want false")
		}
	})

	t.Run("false when no installs root can be resolved", func(t *testing.T) {
		clearMiseEnv(t)
		withUserHomeDir(t, "", errors.New("no home directory"))
		withCurrentExecutable(t, "/some/path/engram", nil)

		if runningBinaryIsMiseManaged() {
			t.Error("runningBinaryIsMiseManaged() = true, want false")
		}
	})
}

func clearMiseEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"MISE_INSTALLS_DIR", "MISE_DATA_DIR", "XDG_DATA_HOME", "LOCALAPPDATA"} {
		t.Setenv(k, "")
	}
}

func withCurrentExecutable(t *testing.T, path string, err error) {
	t.Helper()
	old := currentExecutableFn
	currentExecutableFn = func() (string, error) { return path, err }
	t.Cleanup(func() { currentExecutableFn = old })
}

func withUserHomeDir(t *testing.T, home string, err error) {
	t.Helper()
	old := userHomeDirFn
	userHomeDirFn = func() (string, error) { return home, err }
	t.Cleanup(func() { userHomeDirFn = old })
}
