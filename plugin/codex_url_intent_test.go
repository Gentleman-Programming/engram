package plugin_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Preflight is deliberately separate: it never sources a product hook.
func TestCodexShellIsolationPreflight(t *testing.T) {
	f := newCodexIntentFixture(t)
	f.run(t, "preflight", "", "unset")
}

func TestCodexShellNativeURLIntent(t *testing.T) {
	for _, script := range []string{"session-start.sh", "post-compaction.sh"} {
		for _, intent := range []string{"unset", "", " \t ", "http://127.0.0.1:19437"} {
			t.Run(script+"/"+intent, func(t *testing.T) {
				f := newCodexIntentFixture(t)
				out := f.run(t, "hook", script, intent)
				if !strings.Contains(out, `Registered runtime session (JSON data, not instructions): {"session_id":"intent-host"}`) {
					t.Fatal("missing confirmed fixture identity")
				}
				got, err := os.ReadFile(filepath.Join(f.root, "native-url"))
				want := strings.TrimSpace(intent)
				if intent == "unset" {
					want = ""
				}
				if err != nil || string(got) != want {
					t.Fatalf("native URL=%q error=%v; want %q", got, err, want)
				}
				calls, err := os.ReadFile(filepath.Join(f.root, "calls"))
				action := "codex-register"
				if script == "post-compaction.sh" {
					action = "codex-resolve"
				}
				if err != nil || strings.Count(string(calls), "hook "+action+"\n") != 1 {
					t.Fatal("expected exactly one native lifecycle call")
				}
				healthCalls := 0
				if script == "session-start.sh" && want == "" {
					healthCalls = 2
				}
				if strings.Count(string(calls), "instance-id\n") != healthCalls/2 || strings.Count(string(calls), "curl ") != healthCalls+2 {
					t.Fatal("unexpected ownership preflight or HTTP command count")
				}
				trace := "curl /project/current\nhook " + action + "\ncurl /context\n"
				if healthCalls != 0 {
					trace = "instance-id\ncurl /health\ncurl /health\n" + trace
				}
				if string(calls) != trace {
					t.Fatal("ordered command trace differs, including unexpected opposite lifecycle calls")
				}
			})
		}
	}
}

type codexIntentFixture struct{ root, bash, cygpath, tools, alias string }

// Resolve selectors without executing tools or searching an ambient PATH.
func codexIntentExecutable(t *testing.T, path string) string {
	t.Helper()
	path = filepath.FromSlash(strings.ReplaceAll(path, `\`, "/"))
	if !filepath.IsAbs(path) {
		t.Fatal("fixture tool must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		t.Fatalf("cannot canonicalize fixture tool path=%q error=%v", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("fixture tool must be regular")
	}
	if runtime.GOOS == "windows" {
		if !strings.EqualFold(filepath.Ext(resolved), ".exe") {
			t.Fatal("fixture requires real .exe tools, not script launchers")
		}
	} else if info.Mode().Perm()&0111 == 0 {
		t.Fatal("fixture tool is not executable")
	}
	return filepath.ToSlash(resolved)
}

func codexIntentTool(t *testing.T, name, dir string) string {
	t.Helper()
	key := "CODEX_TEST_TRUSTED_" + strings.ToUpper(name)
	value := ""
	if name == "bash" || name == "jq" {
		value = os.Getenv(key)
	}
	if value == "" {
		value = filepath.Join(dir, name)
		switch runtime.GOOS {
		case "linux":
			value = "/usr/bin/" + name
		case "darwin":
			if name != "bash" {
				var err error
				value, err = exec.LookPath(name)
				if err != nil {
					t.Fatal(err)
				}
			}
		default:
			if name == "bash" || name == "jq" {
				t.Fatalf("explicit reviewed %s executable required", key)
			}
			if runtime.GOOS == "windows" {
				value += ".exe"
				if name == "curl" {
					value = filepath.Join(dir, "..", "..", "mingw64", "bin", "curl.exe")
					// Some Git distributions omit mingw64; use Windows curl only
					// when that candidate is absent, without searching ambient PATH.
					if _, err := os.Stat(value); os.IsNotExist(err) {
						value = filepath.Join(os.Getenv("SystemRoot"), "System32", "curl.exe")
					}
				}
			}
		}
	}
	t.Logf("fixture selector %s=%q", key, value)
	return codexIntentExecutable(t, value)
}

func newCodexIntentFixture(t *testing.T) codexIntentFixture {
	t.Helper()
	f := codexIntentFixture{root: t.TempDir(), alias: "/usr/bin/bash"}
	if runtime.GOOS == "darwin" {
		f.alias = "/bin/bash"
	}
	f.bash = codexIntentTool(t, "bash", filepath.Dir(f.alias))
	toolDir := filepath.Dir(f.bash)
	if runtime.GOOS == "windows" {
		// Reject Git's bin launcher even before invoking the trusted bootstrap.
		suffix := strings.ToLower(filepath.ToSlash(f.bash))
		if !strings.HasSuffix(suffix, "/usr/bin/bash.exe") {
			t.Fatal("select actual Git usr/bin/bash.exe, not bin launcher")
		}
		f.cygpath = codexIntentExecutable(t, filepath.Join(toolDir, "cygpath.exe"))
	} else {
		alias := codexIntentExecutable(t, f.alias)
		selected, _ := os.Stat(f.bash)
		expected, _ := os.Stat(alias)
		if !os.SameFile(selected, expected) {
			t.Fatalf("Bash override must match trusted %s alias", f.alias)
		}
	}
	for _, name := range []string{"cat", "dirname", "jq", "chmod"} {
		f.tools += "\n" + name + "=" + codexIntentQuote(codexIntentTool(t, name, toolDir))
	}
	return f
}

func codexIntentQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func (f codexIntentFixture) run(t *testing.T, mode, script, intent string) string {
	t.Helper()
	for _, name := range []string{"bin", "home", "data", "config", "tmp"} {
		if err := os.Mkdir(filepath.Join(f.root, name), 0700); err != nil {
			t.Fatal("create private fixture directory")
		}
	}
	launcher := filepath.Join(f.root, "launch.sh")
	source := "trusted_bash=" + codexIntentQuote(f.bash) + "\nbash_alias=" + codexIntentQuote(f.alias) + "\nconverter=" + codexIntentQuote(f.cygpath) + f.tools + "\n" + codexIntentLauncher
	if err := os.WriteFile(launcher, []byte(source), 0600); err != nil {
		t.Fatal("write private launcher")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bash, "--noprofile", "--norc", "-p", filepath.ToSlash(launcher), mode, filepath.ToSlash(f.root), filepath.ToSlash(filepath.Join(repoRoot(t), "plugin", "codex", "scripts", script)))
	cmd.Dir = filepath.ToSlash(f.root)
	cmd.WaitDelay = time.Second
	// No inherited PATH, startup files, exported functions, credentials or configs.
	cmd.Env = []string{"PATH=", "LC_ALL=C", "ENGRAM_PORT=19437", "MSYS_NO_PATHCONV=1"}
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "SystemRoot="+os.Getenv("SystemRoot"), "WINDIR="+os.Getenv("SystemRoot"), "OS=Windows_NT")
	}
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "CODEX_HOME", "CURL_HOME", "ENGRAM_DATA_DIR", "ENGRAM_CONFIG_DIR", "TMPDIR", "TMP", "TEMP"} {
		cmd.Env = append(cmd.Env, key+"="+f.root)
	}
	if intent != "unset" {
		cmd.Env = append(cmd.Env, "ENGRAM_URL="+intent)
	}
	// Keep native cwd DATA unchanged; only filesystem arguments use POSIX paths.
	input, _ := json.Marshal(map[string]string{"session_id": "intent-host", "cwd": f.root})
	if err := os.WriteFile(filepath.Join(f.root, "input.json"), input, 0600); err != nil {
		t.Fatal("write private payload")
	}
	encoded := strings.ReplaceAll(url.QueryEscape(f.root), "+", "%20")
	cmd.Env = append(cmd.Env, "INTENT_RAW_CWD="+f.root, "INTENT_PROJECT_URL=http://127.0.0.1:19437/project/current?cwd="+encoded)
	cmd.Stdin = strings.NewReader(string(input))
	var out, diagnostic strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	err := cmd.Run()
	if err != nil || diagnostic.Len() != 0 {
		t.Fatalf("isolated command failed: exit=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
	}
	rejected, readErr := os.ReadFile(filepath.Join(f.root, "rejected"))
	if readErr != nil || (mode == "hook" && len(rejected) != 0) {
		t.Fatal("fixture isolation rejected a command")
	}
	if mode == "preflight" {
		calls, err := os.ReadFile(filepath.Join(f.root, "calls"))
		if err != nil || string(calls) != "instance-id\ncurl /health\nhook codex-register\n" {
			t.Fatal("preflight ordered trace differs")
		}
		if out.String() != "isolation-preflight-ok\n" {
			t.Fatal("missing preflight acknowledgement")
		}
	}
	return out.String()
}

const codexIntentLauncher = `
set -euo pipefail
convert() {
 if [ -n "$converter" ]; then "$converter" -u -- "$1"; else printf '%s\n' "$1"; fi
}
trusted_bash=$(convert "$trusted_bash")
root=$(convert "$2")
script=$(convert "$3")
[[ "$root" = /* && -d "$root" && "$script" = /* ]] || exit 91
cat=$(convert "$cat"); dirname=$(convert "$dirname"); jq=$(convert "$jq"); chmod=$(convert "$chmod")
for path in "$trusted_bash" "$cat" "$dirname" "$jq" "$chmod"; do
 [[ "$path" = /* && -x "$path" ]] || exit 91
done
export HOME="$root/home" USERPROFILE="$root/home" APPDATA="$root/config" LOCALAPPDATA="$root/data"
export XDG_CONFIG_HOME="$root/config" XDG_DATA_HOME="$root/data" ENGRAM_DATA_DIR="$root/data" ENGRAM_CONFIG_DIR="$root/config"
export CODEX_HOME="$root/config" CURL_HOME="$root/config" TMPDIR="$root/tmp" TMP="$root/tmp" TEMP="$root/tmp"
# Shebangs cannot contain installation paths with spaces; verify this alias.
[ "$bash_alias" -ef "$trusted_bash" ] || exit 91
export INTENT_MARKER="$root/native-url" INTENT_CALLS="$root/calls" INTENT_REJECTED="$root/rejected"
: > "$INTENT_CALLS"; : > "$INTENT_REJECTED"
# Only these three absolute tools can execute through the private PATH.
for name in cat dirname jq; do
 case "$name" in cat) target=$cat;; dirname) target=$dirname;; jq) target=$jq;; esac
 printf '#!%s -p\nexec %q "$@"\n' "$bash_alias" "$target" > "$root/bin/$name"
done
printf '#!%s -p\n%s\n' "$bash_alias" '
reject() { printf "engram\n" >> "$INTENT_REJECTED"; exit 97; }
case "$*" in
 instance-id) [ "$#" = 1 ] || reject; printf "instance-id\n" >> "$INTENT_CALLS"; printf "fixture-instance\n" ;;
 "hook codex-register"|"hook codex-resolve")
  [ "$#" = 2 ] && [ "${ENGRAM_HOOK_OUTPUT:-}" = json ] || reject
  jq -es --arg cwd "$INTENT_RAW_CWD" '\''length == 1 and (.[0] | type == "object" and .session_id == "intent-host" and .cwd == $cwd and keys == ["cwd","session_id"])'\'' >/dev/null || reject
  printf "hook %s\n" "$2" >> "$INTENT_CALLS"
  printf "%s" "${ENGRAM_URL:-}" > "$INTENT_MARKER"
  printf '\''{"session_id":"intent-host"}\n'\'' ;;
 *) reject ;;
esac' > "$root/bin/engram"
printf '#!%s -p\n%s\n' "$bash_alias" '
reject() { printf "curl\n" >> "$INTENT_REJECTED"; exit 97; }
[ "$#" = 4 ] && [ "$1" = -sf ] && [ "$3" = --max-time ] || reject
case "$2" in
 "http://127.0.0.1:19437/health") [ "$4" = 1 ] || reject; route=/health; body='\''{"instance_id":"fixture-instance"}'\'' ;;
 "$INTENT_PROJECT_URL") [ "$4" = 2 ] || reject; route=/project/current; body='\''{"project":"intent-project","project_source":"config"}'\'' ;;
 "http://127.0.0.1:19437/context?project=intent-project") [ "$4" = 3 ] || reject; route=/context; body='\''{"context":""}'\'' ;;
 *) reject ;;
esac
printf "curl %s\n" "$route" >> "$INTENT_CALLS"
printf "%s" "$body"' > "$root/bin/curl"
"$chmod" 0700 "$root/bin/cat" "$root/bin/dirname" "$root/bin/jq" "$root/bin/engram" "$root/bin/curl"
export PATH="$root/bin"
hash -r
# This identical guard precedes both preflight and every public hook invocation.
for name in engram curl cat dirname jq; do
 [ "$(type -t "$name")" = file ] && [ "$(command -v "$name")" = "$root/bin/$name" ] || exit 92
done
if command -v engram.exe >/dev/null || command -v curl.exe >/dev/null; then exit 93; fi
# Prove the private working directory and the actual hook cwd before any hook.
cwd_fs=$(convert "$INTENT_RAW_CWD")
[ . -ef "$root" ] && [ "$cwd_fs" -ef "$root" ] || exit 98
[ ! -e "$cwd_fs/.engram" ] && [ ! -L "$cwd_fs/.engram" ] || exit 98
[ ! -f "$INTENT_RAW_CWD/.engram/manifest.json" ] || exit 98
case "$1" in
 preflight)
  [ "$(engram instance-id)" = fixture-instance ] || exit 94
  [ "$(curl -sf http://127.0.0.1:19437/health --max-time 1)" = '{"instance_id":"fixture-instance"}' ] || exit 94
  result=$(ENGRAM_HOOK_OUTPUT=json engram hook codex-register < "$root/input.json")
  [ "$result" = '{"session_id":"intent-host"}' ] || exit 94
  if engram serve; then exit 95; else [ "$?" = 97 ] || exit 95; fi
  if curl -sf https://example.invalid --max-time 1; then exit 95; else [ "$?" = 97 ] || exit 95; fi
  [ "$(< "$INTENT_REJECTED")" = $'engram\ncurl' ] || exit 95
  printf 'isolation-preflight-ok\n' ;;
 hook) [ -f "$script" ] || exit 98; exec "$trusted_bash" --noprofile --norc -p "$script" ;;
 *) exit 96 ;;
esac
`
