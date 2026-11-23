package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/plugin"
	"golang.org/x/sys/windows"
)

const (
	claudeNativePromptHook = "claude-windows-user-prompt-submit"
	claudePromptBash       = `"${CLAUDE_PLUGIN_ROOT}/scripts/user-prompt-submit.sh"`
	claudePromptPowerShell = "user-prompt-submit.ps1"
	claudeHooksPath        = "hooks/hooks.json"
	claudeManifestPath     = ".claude-plugin/plugin.json"
)

type claudeBundleFile struct {
	path string
	data []byte
}

type claudeBundle []claudeBundleFile

type stagedClaudeBundle struct {
	files   claudeBundle
	folder  string
	source  string
	version string
	pin     string
}

func loadClaudeBundle() (claudeBundle, error) {
	var bundle claudeBundle
	source := plugin.ClaudeCodeAssets()
	err := fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		bundle = append(bundle, claudeBundleFile{path: name, data: raw})
		return nil
	})
	return bundle, err
}

func (b claudeBundle) verify(root string, githubCheckout bool) error {
	index := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := rejectClaudeReparsePoint(path); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if index >= len(b) || b[index].path != filepath.ToSlash(rel) {
			return fmt.Errorf("unexpected Claude plugin asset %s", rel)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		want := b[index].data
		ext := filepath.Ext(rel)
		if githubCheckout && (ext == ".json" || ext == ".md" || b[index].path == "scripts/"+claudePromptPowerShell) {
			got = bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n"))
			want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("changed Claude plugin asset %s", rel)
		}
		index++
		return nil
	})
	if err == nil && index != len(b) {
		return fmt.Errorf("missing Claude plugin asset %s", b[index].path)
	}
	return err
}

func (b claudeBundle) stage(root, pin string) (stagedClaudeBundle, error) {
	return b.stageWithGuardBudget(root, pin, false)
}

func (b claudeBundle) stageWithGuardBudget(root, pin string, extendedGuard bool) (stagedClaudeBundle, error) {
	files := append(claudeBundle(nil), b...)
	hash := sha256.New()
	for _, file := range b {
		fmt.Fprintf(hash, "%s\x00%x\n", file.path, sha256.Sum256(file.data))
	}
	fmt.Fprintf(hash, "engram-managed-claude-v1\x00%s", pin)
	if extendedGuard {
		_, _ = hash.Write([]byte("\x00extended-pretooluse-v1"))
	}
	id := hex.EncodeToString(hash.Sum(nil))
	bundle := stagedClaudeBundle{
		files: files, pin: pin, source: "./bundles/" + id,
		folder: filepath.Join(root, "bundles", id),
	}
	for index, file := range files {
		switch file.path {
		case claudeManifestPath:
			var manifest map[string]json.RawMessage
			if err := json.Unmarshal(file.data, &manifest); err != nil {
				return bundle, err
			}
			var name, version string
			if json.Unmarshal(manifest["name"], &name) != nil || name != "engram" ||
				json.Unmarshal(manifest["version"], &version) != nil || version == "" {
				return bundle, errors.New("invalid bundled Engram Claude plugin identity")
			}
			bundle.version = version + "+engram." + id[:16]
			manifest["version"], _ = json.Marshal(bundle.version)
			files[index].data, _ = json.MarshalIndent(manifest, "", "  ")
		case claudeHooksPath:
			hooks, err := claudePromptVariant(file.data, pin, extendedGuard)
			if err != nil {
				return bundle, err
			}
			files[index].data = hooks
		}
	}
	if bundle.version == "" {
		return bundle, errors.New("missing bundled Engram Claude plugin manifest")
	}
	parent := filepath.Dir(bundle.folder)
	if err := ensureClaudeManagedDirectory(parent); err != nil {
		return bundle, err
	}
	if _, err := os.Lstat(bundle.folder); errors.Is(err, os.ErrNotExist) {
		stage, err := os.MkdirTemp(parent, ".stage-")
		if err != nil {
			return bundle, err
		}
		defer os.RemoveAll(stage)
		for _, file := range files {
			path := filepath.Join(stage, filepath.FromSlash(file.path))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return bundle, err
			}
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return bundle, err
			}
			err = copyAndSyncClaudeFile(f, bytes.NewReader(file.data))
			if err != nil {
				return bundle, err
			}
		}
		if err := moveClaudeFile(stage, bundle.folder, false); err != nil {
			return bundle, err
		}
	} else if err != nil {
		return bundle, err
	}
	return bundle, files.verify(bundle.folder, false)
}

func claudePromptVariant(data []byte, pin string, extendedGuard bool) ([]byte, error) {
	var manifest map[string]json.RawMessage
	var events map[string]json.RawMessage
	var groups []map[string]json.RawMessage
	var hooks []map[string]json.RawMessage
	if json.Unmarshal(data, &manifest) != nil ||
		json.Unmarshal(manifest["hooks"], &events) != nil ||
		json.Unmarshal(events["UserPromptSubmit"], &groups) != nil || len(groups) != 1 ||
		json.Unmarshal(groups[0]["hooks"], &hooks) != nil || len(hooks) != 1 {
		return nil, errors.New("bundled Engram plugin must have one UserPromptSubmit hook")
	}
	var kind, command string
	if json.Unmarshal(hooks[0]["type"], &kind) != nil || kind != "command" ||
		json.Unmarshal(hooks[0]["command"], &command) != nil || command != claudePromptBash || hooks[0]["args"] != nil {
		return nil, errors.New("unexpected bundled Engram Bash prompt command")
	}
	if pin == "" {
		if extendedGuard {
			return nil, errors.New("extended Claude PreToolUse requires a pinned Engram core")
		}
		return data, nil
	}
	hooks[0]["command"], _ = json.Marshal(pin)
	hooks[0]["args"], _ = json.Marshal([]string{"hook", claudeNativePromptHook})
	groups[0]["hooks"], _ = json.Marshal(hooks)
	events["UserPromptSubmit"], _ = json.Marshal(groups)
	if extendedGuard {
		if err := extendClaudePreToolUse(events, pin); err != nil {
			return nil, err
		}
	}
	manifest["hooks"], _ = json.Marshal(events)
	return json.MarshalIndent(manifest, "", "  ")
}

func extendClaudePreToolUse(events map[string]json.RawMessage, pin string) error {
	var groups []map[string]json.RawMessage
	var hooks []map[string]json.RawMessage
	if json.Unmarshal(events["PreToolUse"], &groups) != nil || len(groups) != 1 ||
		json.Unmarshal(groups[0]["hooks"], &hooks) != nil || len(hooks) != 1 {
		return errors.New("bundled Engram plugin must have one PreToolUse hook")
	}
	hook := hooks[0]
	var kind, command, failure string
	var args []string
	if json.Unmarshal(hook["type"], &kind) != nil || kind != "command" ||
		json.Unmarshal(hook["command"], &command) != nil || command != "engram" ||
		json.Unmarshal(hook["args"], &args) != nil || len(args) != 2 ||
		args[0] != "hook" || args[1] != "claude-pre-tool-use" ||
		json.Unmarshal(hook["onFailure"], &failure) != nil || failure != "block" {
		return errors.New("unexpected bundled Engram PreToolUse command")
	}
	hook["command"], _ = json.Marshal(pin)
	hook["args"], _ = json.Marshal(append(args, ClaudeWindowsExtendedGuardArg))
	hook["timeout"] = json.RawMessage("10")
	groups[0]["hooks"], _ = json.Marshal(hooks)
	events["PreToolUse"], _ = json.Marshal(groups)
	return nil
}

func (b stagedClaudeBundle) publish(root string) error {
	if err := b.files.verify(b.folder, false); err != nil {
		return err
	}
	if b.pin != "" {
		if err := verifyClaudePromptPin(b.pin); err != nil {
			return err
		}
	}
	dir := filepath.Join(root, ".claude-plugin")
	if err := ensureClaudeManagedDirectory(dir); err != nil {
		return err
	}
	catalog := filepath.Join(dir, "marketplace.json")
	if err := rejectClaudeReparsePoint(catalog); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(map[string]any{
		"name":    "engram",
		"owner":   map[string]string{"name": "Gentleman Programming", "email": "gentlemanprogramming.dev@gmail.com"},
		"plugins": []map[string]string{{"name": "engram", "version": b.version, "source": b.source}},
	}, "", "  ")
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(catalog); err == nil && bytes.Equal(raw, old) {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".marketplace-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := copyAndSyncClaudeFile(tmp, bytes.NewReader(raw)); err != nil {
		return err
	}
	if err := moveClaudeFile(tmp.Name(), catalog, true); err != nil {
		return fmt.Errorf("replace the managed Claude catalog: %w", err)
	}
	got, err := os.ReadFile(catalog)
	if err != nil || !bytes.Equal(got, raw) {
		return errors.New("managed Claude catalog readback failed; rerun setup to reconcile")
	}
	return nil
}

func pinClaudePromptCore(root string) (string, error) {
	exe, err := osExecutable()
	if err != nil {
		return "", err
	}
	source, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer source.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, source); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	dir := filepath.Join(root, "cores", digest)
	if err := ensureClaudeManagedDirectory(dir); err != nil {
		return "", err
	}
	pin := filepath.Join(dir, "engram.exe")
	if err := rejectClaudeReparsePoint(pin); err != nil {
		return "", err
	}
	if _, err := os.Stat(pin); errors.Is(err, os.ErrNotExist) {
		if _, err := source.Seek(0, 0); err != nil {
			return "", err
		}
		staged, err := os.CreateTemp(dir, ".engram-*.exe")
		if err != nil {
			return "", err
		}
		defer os.Remove(staged.Name())
		if err := copyAndSyncClaudeFile(staged, source); err != nil {
			return "", err
		}
		if err := compareClaudeDigest(staged.Name(), digest); err != nil {
			return "", err
		}
		if err := moveClaudeFile(staged.Name(), pin, false); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return pin, verifyClaudePromptPin(pin)
}

func verifyClaudePromptPin(pin string) error {
	digest := filepath.Base(filepath.Dir(pin))
	if err := rejectClaudeReparsePoint(pin); err != nil {
		return err
	}
	if err := compareClaudeDigest(pin, digest); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pin, "hook", claudeNativePromptHook, "--check")
	var stdout, stderr claudeCapabilityOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pinned Engram core failed the native Claude prompt capability check: %w", err)
	}
	want := `{"hook":"claude-windows-user-prompt-submit","version":1}`
	if strings.TrimSpace(stdout.buf.String()) != want || stderr.buf.Len() != 0 {
		return errors.New("pinned Engram core did not report the supported native Claude prompt capability")
	}
	return compareClaudeDigest(pin, digest)
}

type claudeCapabilityOutput struct{ buf bytes.Buffer }

func (b *claudeCapabilityOutput) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > 256 {
		return 0, errors.New("unexpected Claude prompt capability output size")
	}
	return b.buf.Write(p)
}

func compareClaudeDigest(path, digest string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("pinned Engram core checksum changed")
	}
	return nil
}

func copyAndSyncClaudeFile(file *os.File, data io.Reader) error {
	_, copyErr := io.Copy(file, data)
	return errors.Join(copyErr, file.Sync(), file.Close())
}

func moveClaudeFile(from, to string, replace bool) error {
	src, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	dst, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(src, dst, flags)
}
