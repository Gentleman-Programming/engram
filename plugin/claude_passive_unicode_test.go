package plugin_test

import (
	"encoding/json"
	"fmt"
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
		curl, err := gitNativeCurl(git)
		if err != nil {
			t.Skip(err)
		}
		t.Logf("Git native curl: %s", curl)
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
		{"handback", "tool_input", "## Key Learnings\n1. Entrega íntegra → 中文 🧠\n"},
		{"ascii", "last_assistant_message", "## Key Learnings\n1. ordinary text stays unchanged\n"},
		{"unicode", "last_assistant_message", "## Key Learnings\r\n1. la sesión añade tildes y eñes → 中文 🧠\r\n\"quoted\" $HOME `id` \\path\n"},
		{"stdout-fallback", "stdout", "## Key Learnings\n1. résumé, ação y eñes permanecen íntegras\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, captured := captureServer(t)
			env["ENGRAM_PORT"] = serverPort(t, srv)
			payload := map[string]any{"session_id": "unicode-session", "cwd": t.TempDir(), tc.field: tc.content}
			if tc.field == "tool_input" {
				payload[tc.field] = map[string]string{"message": tc.content}
				payload["hook_event_name"] = "PostToolUse"
				payload["tool_name"] = "SubagentHandback"
			}
			input, err := json.Marshal(payload)
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

// gitNativeCurl resolves only Git-bundled native curl, never a PATH/system substitute.
func gitNativeCurl(git string) (string, error) {
	for _, root := range []string{filepath.Join(filepath.Dir(git), ".."), filepath.Join(filepath.Dir(git), "..", "..")} {
		for _, layout := range []string{"ucrt64", "mingw64"} {
			candidate := filepath.Join(root, layout, "bin", "curl.exe")
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return filepath.Clean(candidate), nil
			}
		}
	}
	return "", fmt.Errorf("Git native curl unavailable near %s", git)
}

func TestGitNativeCurlLayouts(t *testing.T) {
	for _, gitDir := range []string{"cmd", "bin", "usr/bin", "mingw64/bin", "ucrt64/bin"} {
		for _, layout := range []string{"mingw64", "ucrt64"} {
			t.Run(gitDir+"/"+layout, func(t *testing.T) {
				root := t.TempDir()
				want := filepath.Join(root, layout, "bin", "curl.exe")
				if err := os.MkdirAll(filepath.Dir(want), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(want, []byte("fixture, never executed"), 0600); err != nil {
					t.Fatal(err)
				}
				got, err := gitNativeCurl(filepath.Join(root, filepath.FromSlash(gitDir), "git.exe"))
				if err != nil || got != want {
					t.Fatalf("curl = %q, %v; want %q", got, err, want)
				}
			})
		}
	}
	t.Run("reject missing and directory without PATH fallback", func(t *testing.T) {
		root := t.TempDir()
		for _, layout := range []string{"mingw64", "ucrt64", "System32"} {
			if err := os.MkdirAll(filepath.Join(root, layout, "bin", "curl.exe"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := gitNativeCurl(filepath.Join(root, "cmd", "git.exe")); err == nil || got != "" {
			t.Fatalf("unexpected curl %q, %v", got, err)
		}
	})
}
