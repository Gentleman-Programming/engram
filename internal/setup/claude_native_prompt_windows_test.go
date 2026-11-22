package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestManagedClaudePromptBundlePreservesAssets(t *testing.T) {
	files, err := loadClaudeBundle()
	if err != nil {
		t.Fatal(err)
	}
	if err := files.verify(filepath.Join("..", "..", "plugin", "claude-code"), false); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bash, err := files.stage(root, "")
	if err != nil {
		t.Fatal(err)
	}
	pin := filepath.Join(root, "pinned core", "engram.exe")
	native, err := files.stage(root, pin)
	if err != nil {
		t.Fatal(err)
	}
	if bash.version == native.version || bash.folder == native.folder {
		t.Fatal("Bash and native share a bundle identity")
	}
	for i, original := range files {
		switch original.path {
		case claudeHooksPath:
			var bashManifest, nativeManifest map[string]any
			if err := json.Unmarshal(bash.files[i].data, &bashManifest); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(native.files[i].data, &nativeManifest); err != nil {
				t.Fatal(err)
			}
			bashHooks := bashManifest["hooks"].(map[string]any)
			nativeHooks := nativeManifest["hooks"].(map[string]any)
			groups := nativeHooks["UserPromptSubmit"].([]any)
			hooks := groups[0].(map[string]any)["hooks"].([]any)
			command := hooks[0].(map[string]any)
			if len(groups) != 1 || len(hooks) != 1 || command["command"] != pin ||
				!reflect.DeepEqual(command["args"], []any{"hook", claudeNativePromptHook}) {
				t.Fatalf("native prompt command = %#v", command)
			}
			command["command"] = claudePromptBash
			delete(command, "args")
			if !reflect.DeepEqual(bashHooks, nativeHooks) || !reflect.DeepEqual(bashManifest, nativeManifest) {
				t.Fatal("managed native changes an unrelated hook field")
			}
		case claudeManifestPath:
			var bashManifest, nativeManifest map[string]any
			if err := json.Unmarshal(bash.files[i].data, &bashManifest); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(native.files[i].data, &nativeManifest); err != nil {
				t.Fatal(err)
			}
			delete(bashManifest, "version")
			delete(nativeManifest, "version")
			if !reflect.DeepEqual(bashManifest, nativeManifest) {
				t.Fatal("managed native changes an unrelated manifest field")
			}
		default:
			if !bytes.Equal(original.data, native.files[i].data) || !bytes.Equal(original.data, bash.files[i].data) {
				t.Fatalf("managed variant changed %s", original.path)
			}
		}
	}
	if err := bash.publish(root); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(root, ".claude-plugin", "marketplace.json")
	prior, err := os.ReadFile(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := native.publish(root); err == nil {
		t.Fatal("published a native bundle with a missing executable")
	}
	if err := os.Remove(filepath.Join(bash.folder, filepath.FromSlash(files[len(files)-1].path))); err != nil {
		t.Fatal(err)
	}
	if err := bash.publish(root); err == nil || !strings.Contains(err.Error(), "missing Claude plugin asset") {
		t.Fatalf("missing bundled asset was accepted: %v", err)
	}
	got, err := os.ReadFile(catalog)
	if err != nil || !bytes.Equal(got, prior) {
		t.Fatalf("failed publication changed prior catalog: %v", err)
	}
}

func TestManagedClaudePromptCheckoutNormalization(t *testing.T) {
	root := t.TempDir()
	files := claudeBundle{{path: "hook.sh", data: []byte("bash\n")}, {path: "skills.md", data: []byte("markdown\n")}}
	if err := os.WriteFile(filepath.Join(root, "hook.sh"), []byte("bash\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills.md"), []byte("markdown\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := files.verify(root, true); err != nil {
		t.Fatal(err)
	}
	if err := files.verify(root, false); err == nil {
		t.Fatal("staged bundle accepted changed bytes")
	}
	if err := os.WriteFile(filepath.Join(root, "hook.sh"), []byte("bash\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := files.verify(root, true); err == nil {
		t.Fatal("GitHub bundle accepted changed Bash bytes")
	}
}

func TestManagedClaudePromptLockBeforeOptIn(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uncreated-profile")
	release, err := lockManagedClaude(root)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 1)
	go func() {
		another, err := lockManagedClaude(strings.ToLower(root))
		if err == nil {
			another()
		}
		results <- err
	}()
	got := <-results
	release()
	if got == nil {
		t.Fatal("same profile admitted concurrent setups")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock changed an unconfigured profile: %v", err)
	}
	release, err = lockManagedClaude(root)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestManagedClaudePromptCapabilityOutputIsBounded(t *testing.T) {
	var out claudeCapabilityOutput
	if _, err := io.Copy(&out, strings.NewReader(strings.Repeat("x", 257))); err == nil {
		t.Fatal("unbounded capability output was accepted")
	}
}
