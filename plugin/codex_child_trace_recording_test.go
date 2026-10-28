package plugin_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCodexChildTraceRecordingFailureKeepsOracle(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only observer")
	}
	root := t.TempDir()
	fixture := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer fixture.Close()
	env := codexHandoffEnv(t, root, fixture.URL)
	out := filepath.Join(t.TempDir(), "capture")
	t.Setenv(codexChildTraceEnv, out)
	cmd := exec.Command(codexTestBash(t), "-c", "printf '{}\\n'")
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	finish := startCodexChildTrace(t, "empty confirmation", root, cmd, &stdout, &stderr)
	if _, err := os.Stat(filepath.Join(out, "before.json")); err != nil {
		t.Fatalf("observer preparation did not run: %v", err)
	}
	// Only owned test data is replaced, deliberately making every later capture fail.
	if err := os.RemoveAll(out); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("unwritable capture directory"), 0600); err != nil {
		t.Fatal(err)
	}
	err := cmd.Run()
	finish()
	if err != nil || stdout.String() != "{}\n" || stderr.Len() != 0 {
		t.Fatalf("original oracle changed: %v stdout=%q stderr=%q", err, &stdout, &stderr)
	}
}
