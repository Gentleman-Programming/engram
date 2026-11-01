package plugin_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSubagentStopPreservesUnicodeTransport(t *testing.T) {
	requireHookBinaries(t)
	env := map[string]string{"PI_CODING_AGENT": ""}
	if runtime.GOOS == "windows" {
		git, err := exec.LookPath("git")
		if err != nil {
			t.Skip("Git for Windows is required for its native curl regression")
		}
		var curl string
		for _, root := range []string{filepath.Join(filepath.Dir(git), ".."), filepath.Join(filepath.Dir(git), "..", "..")} {
			candidate := filepath.Join(root, "mingw64", "bin", "curl.exe")
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				curl = candidate
				break
			}
		}
		if curl == "" {
			t.Skip("Git's mingw64/bin/curl.exe is unavailable")
		}
		// Exercise Git's actual Windows argv conversion, not a curl mock or
		// whichever Unicode-capable curl happens to precede it in PATH.
		bootstrap := filepath.Join(t.TempDir(), "curl.sh")
		if err := os.WriteFile(bootstrap, []byte("curl() { \"$ENGRAM_TEST_CURL\" \"$@\"; }\n"), 0600); err != nil {
			t.Fatal(err)
		}
		env["BASH_ENV"] = bashScriptPath(t, bootstrap)
		env["ENGRAM_TEST_CURL"] = bashScriptPath(t, curl)
	}
	for _, tc := range []struct{ name, field, content string }{
		{"ascii", "last_assistant_message", "## Key Learnings\n1. ordinary text stays unchanged\n"},
		{"unicode", "last_assistant_message", "## Key Learnings\r\n1. la sesión añade tildes y eñes → 中文 🧠\r\n\"quoted\" $HOME `id` \\path\n"},
		{"stdout-fallback", "stdout", "## Key Learnings\n1. résumé, ação y eñes permanecen íntegras\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, captured := captureServer(t)
			env["ENGRAM_PORT"] = serverPort(t, srv)
			input, err := json.Marshal(map[string]string{"session_id": "unicode-session", "cwd": t.TempDir(), tc.field: tc.content})
			if err != nil {
				t.Fatal(err)
			}
			runHook(t, "subagent-stop.sh", string(input), env)
			got := captured()
			want := passiveCapture{SessionID: "unicode-session", Project: "engram", Source: "subagent-stop", Content: tc.content}
			if len(got) != 1 || got[0] != want {
				t.Fatalf("capture = %+v, want %+v", got, want)
			}
		})
	}
}
