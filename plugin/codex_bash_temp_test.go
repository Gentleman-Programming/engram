package plugin_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Capture the effective native directory before tests can change the ambient
// environment. os.TempDir includes Windows fallback behavior when TMP/TEMP are
// absent; fixture USERPROFILE must not change that lifetime owner.
var codexTempProcessStart = os.TempDir()

// This is a constructor/OS-environment contract, not a reproduction of the
// native MSYS warning. No Bash process is needed to expose the lifetime error.
func TestCodexBashTempStartupLifetime(t *testing.T) {
	root := t.TempDir()
	env := codexHandoffEnv(t, root, "http://127.0.0.1:19437")
	values := codexTempEnvValues(env)
	for _, key := range []string{"TMP", "TEMP"} {
		want := filepath.Join(root, "tmp")
		if runtime.GOOS == "windows" {
			want = codexTempProcessStart
		}
		if values[key] != want {
			t.Errorf("first Bash startup %s=%q; want %q (process-start native temp on Windows, fixture temp on POSIX)", key, values[key], want)
		}
	}
	for key, subdir := range map[string]string{
		"TMPDIR": "tmp", "HOME": "home", "USERPROFILE": "home",
		"APPDATA": "config", "LOCALAPPDATA": "data",
		"ENGRAM_DATA_DIR": "data", "ENGRAM_CONFIG_DIR": "config",
	} {
		if want := filepath.Join(root, subdir); values[key] != want {
			t.Errorf("private %s=%q; want %q", key, values[key], want)
		}
	}
}

// Exercise the existing trusted BASH_ENV path and a real native jq child.
// Keep this separate from the startup assertion so RED still exercises privacy.
func TestCodexBashTempHandoffChildPrivacy(t *testing.T) {
	root := t.TempDir()
	f := newCodexIntentFixture(t)
	env := codexHandoffEnv(t, root, "http://127.0.0.1:19437")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bash, "--noprofile", "--norc", "-c", `
set -eu
# Compare filesystem identity: MSYS may convert the native root spelling.
[ "$TMP" -ef "$PWD/tmp" ]
[ "$TEMP" -ef "$PWD/tmp" ]
[ "$TMPDIR" -ef "$PWD/tmp" ]
[ "$HOME" -ef "$PWD/home" ]
[ "$ENGRAM_DATA_DIR" -ef "$PWD/data" ]
for key in TMP TEMP TMPDIR; do
  child_temp=$(jq -ern --arg key "$key" 'env[$key]')
  child_temp=${child_temp%$'\r'}
  [ "$child_temp" -ef "$PWD/tmp" ]
done
printf 'private-child-ok\n'
`)
	cmd.Env, cmd.Dir = env, root
	cmd.WaitDelay = time.Second
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil || stderr.Len() != 0 || stdout.String() != "private-child-ok\n" {
		t.Fatalf("post-startup child privacy: exit=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func codexTempEnvValues(env []string) map[string]string {
	values := make(map[string]string)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	return values
}
