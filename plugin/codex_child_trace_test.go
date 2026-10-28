package plugin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const codexChildTraceEnv = "ENGRAM_CODEX_CHILD_TRACE"

type codexTracePatch struct {
	path          string
	before, after []byte
	mode          os.FileMode
}

func codexChildTraceEnabled(goos, name, dir string) bool {
	return goos == "windows" && name == "empty confirmation" && dir != ""
}

// Restore even the failed write, which may have partially changed that file.
func applyCodexTracePatches(patches []codexTracePatch, write func(string, []byte, os.FileMode) error) error {
	for i, patch := range patches {
		if err := write(patch.path, patch.after, patch.mode); err != nil {
			problems := []error{err}
			for j := i; j >= 0; j-- {
				problems = append(problems, write(patches[j].path, patches[j].before, patches[j].mode))
			}
			return errors.Join(problems...)
		}
	}
	return nil
}

// Observer failures are reported separately; they never replace the test oracle.
func startCodexChildTrace(t *testing.T, name, root string, cmd *exec.Cmd, stdout, stderr *bytes.Buffer) func() {
	t.Helper()
	out := os.Getenv(codexChildTraceEnv)
	if !codexChildTraceEnabled(runtime.GOOS, name, out) {
		return func() {}
	}
	if !filepath.IsAbs(out) {
		t.Log("Codex child observer unavailable: trace directory must be absolute")
		return func() {}
	}
	var problems []string
	note := func(err error) {
		if err != nil {
			problems = append(problems, err.Error())
			t.Logf("Codex child observer unavailable: %v", err)
		}
	}
	save := func(name string, data []byte) { note(os.WriteFile(filepath.Join(out, name), data, 0600)) }
	snapshot := func() map[string]any {
		dirs := map[string]bool{}
		for _, p := range []string{root, os.TempDir(), filepath.Join(root, "tmp")} {
			info, err := os.Stat(p)
			dirs[p] = err == nil && info.IsDir()
		}
		return map[string]any{"directories": dirs}
	}
	trace := filepath.Join(root, "codex-child.trace")
	prepare := func() error {
		if !filepath.IsAbs(out) {
			return errors.New("child trace directory must be absolute")
		}
		if err := os.MkdirAll(out, 0700); err != nil {
			return err
		}
		fixtureBash, err := filepath.EvalSymlinks(filepath.FromSlash(os.Getenv("CODEX_TEST_TRUSTED_BASH")))
		if err != nil {
			return err
		}
		if !strings.HasSuffix(strings.ToLower(filepath.ToSlash(fixtureBash)), "/usr/bin/bash.exe") {
			return errors.New("expected fixture usr/bin Bash, not test launcher")
		}
		converter := filepath.Join(filepath.Dir(fixtureBash), "cygpath.exe")
		info, err := os.Stat(converter)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("converter must be regular")
		}
		env := map[string]string{}
		for _, e := range cmd.Env {
			key, value, _ := strings.Cut(e, "=")
			switch key {
			case "TEMP", "TMP", "TMPDIR", "PATH", "BASH_ENV", "HOME", "USERPROFILE", "SystemRoot", "WINDIR", "LC_ALL", "MSYS_NO_PATHCONV":
				env[key] = value
			}
		}
		before := snapshot()
		before["launcher"] = cmd.Path
		before["fixtureBash"] = fixtureBash
		before["converter"] = converter
		before["env"] = env
		data, err := json.MarshalIndent(before, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, "before.json"), data, 0600); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		convert := exec.CommandContext(ctx, converter, "-u", "--", trace)
		convert.Env = cmd.Env
		var diagnostic bytes.Buffer
		convert.Stderr = &diagnostic
		converted, err := convert.Output()
		if err != nil {
			return fmt.Errorf("observer path conversion: %w", err)
		}
		if diagnostic.Len() != 0 {
			return fmt.Errorf("observer conversion stderr: %s", &diagnostic)
		}
		posix := strings.TrimRight(string(converted), "\r\n")
		if !strings.HasPrefix(posix, "/") || strings.ContainsAny(posix, "\r\n") {
			return errors.New("invalid observer trace path")
		}
		logger := func(stage string) []byte {
			return []byte(fmt.Sprintf(`
__engram_child_trace() {
 local present=no mapping code
 [ ! -d /tmp ] || present=yes
 mapping=$(%s -w -- /tmp); code=$?
 builtin printf 'stage=%%s\tpid=%%s\tppid=%%s\tbash=%%s\tversion=%%s\ttmpdir=%%s\ttmp=%%s\ttemp=%%s\tpwd=%%s\ttmp_exists=%%s\tnative_tmp=%%s\tmap_exit=%%s\n' %s "$BASHPID" "$PPID" "$BASH" "$BASH_VERSION" "${TMPDIR-<unset>}" "${TMP-<unset>}" "${TEMP-<unset>}" "$PWD" "$present" "$mapping" "$code" >> %s
}
__engram_child_trace 2>> %s
unset -f __engram_child_trace
`, codexIntentQuote(filepath.ToSlash(converter)), codexIntentQuote(stage), codexIntentQuote(posix), codexIntentQuote(posix+".observer-errors")))
		}
		gate := filepath.Join(root, "private-startup.sh")
		if !strings.EqualFold(filepath.Clean(filepath.FromSlash(env["BASH_ENV"])), filepath.Clean(gate)) {
			return errors.New("unexpected observer startup gate")
		}
		paths := []string{gate}
		for _, tool := range []string{"engram", "curl", "cat", "dirname", "jq", "date", "touch", "bash"} {
			paths = append(paths, filepath.Join(root, "bin", tool))
		}
		var patches []codexTracePatch
		for _, p := range paths {
			original, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			info, err := os.Stat(p)
			if err != nil {
				return err
			}
			if bytes.Contains(original, []byte("__engram_child_trace")) {
				return errors.New("observer already installed")
			}
			at, stage := len(original), "gate"
			if p != gate {
				at = bytes.IndexByte(original, '\n') + 1
				stage = "shim-" + filepath.Base(p)
				if at == 0 || !bytes.HasPrefix(original, []byte("#!")) {
					return errors.New("invalid observer shim")
				}
			}
			changed := append([]byte{}, original[:at]...)
			changed = append(changed, logger(stage)...)
			changed = append(changed, original[at:]...)
			patches = append(patches, codexTracePatch{p, original, changed, info.Mode().Perm()})
		}
		return applyCodexTracePatches(patches, os.WriteFile)
	}
	prepared := prepare()
	note(prepared)
	return func() {
		// No Fatal/Error: original assertions, including rejected-data counters, run.
		save("hook.stdout", stdout.Bytes())
		save("hook.stderr", stderr.Bytes())
		for _, pair := range [][2]string{{trace, "child.trace"}, {trace + ".observer-errors", "observer.stderr"}} {
			data, err := os.ReadFile(pair[0])
			if err != nil {
				note(err)
			} else {
				save(pair[1], data)
			}
		}
		data, err := json.MarshalIndent(snapshot(), "", "  ")
		note(err)
		if err == nil {
			save("after.json", data)
		}
		data, err = json.MarshalIndent(map[string]any{"available": prepared == nil && len(problems) == 0, "errors": problems, "prepared": prepared == nil}, "", "  ")
		note(err)
		if err == nil {
			save("status.json", data)
		}
	}
}
