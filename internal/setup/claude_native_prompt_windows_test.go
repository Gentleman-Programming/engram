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
	files := claudeBundle{
		{path: "hook.sh", data: []byte("bash\n")},
		{path: "scripts/user-prompt-submit.ps1", data: []byte("Write-Output fixture\n")},
		{path: "skills.md", data: []byte("markdown\n")},
		{path: "unrelated.ps1", data: []byte("Write-Output unrelated\n")},
	}
	if err := os.WriteFile(filepath.Join(root, "hook.sh"), []byte("bash\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills.md"), []byte("markdown\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	powershell := filepath.Join(root, "scripts", claudePromptPowerShell)
	if err := os.Mkdir(filepath.Dir(powershell), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(powershell, []byte("Write-Output fixture\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "unrelated.ps1")
	if err := os.WriteFile(unrelated, []byte("Write-Output unrelated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := files.verify(root, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills.md"), []byte("markdown\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := files.verify(root, false); err == nil || !strings.Contains(err.Error(), "user-prompt-submit.ps1") {
		t.Fatalf("staged bundle PowerShell byte check returned %v", err)
	}
	if err := os.WriteFile(powershell, []byte("Write-Output modified\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := files.verify(root, true); err == nil || !strings.Contains(err.Error(), "user-prompt-submit.ps1") {
		t.Fatalf("GitHub bundle PowerShell content check returned %v", err)
	}
	if err := os.WriteFile(powershell, []byte("Write-Output fixture\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hook.sh"), []byte("bash\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := files.verify(root, true); err == nil {
		t.Fatal("GitHub bundle accepted changed Bash bytes")
	}
	if err := os.WriteFile(filepath.Join(root, "hook.sh"), []byte("bash\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("Write-Output unrelated\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := files.verify(root, true); err == nil || !strings.Contains(err.Error(), "unrelated.ps1") {
		t.Fatalf("GitHub bundle unrelated PowerShell byte check returned %v", err)
	}
}

func TestManagedClaudePromptFindsDecodedUserOverride(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook string
		want bool
	}{
		{"documented PowerShell", `{"type":"command","command":"pwsh -NoProfile -ExecutionPolicy Bypass -File \"C:\\path\\to\\engram\\plugin\\claude-code\\scripts\\user-prompt-submit\u002eps1\"","timeout":10}`, true},
		{"native exec arguments", `{"type":"command","command":"engram.exe","args":["hook","claude-windows-user-prompt-submit"]}`, true},
		{"legacy Bash", `{"type":"command","command":"bash user-prompt-submit.sh"}`, true},
		{"unrelated hook status text", `{"type":"command","command":"echo fixture","statusMessage":"user-prompt-submit.sh"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := t.TempDir()
			data := []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[` + tc.hook + `]}]}}`)
			if err := os.WriteFile(filepath.Join(profile, "settings.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := checkClaudeUserPromptSettings(profile); (err != nil) != tc.want {
				t.Fatalf("manual override check returned %v, want rejection %v", err, tc.want)
			}
		})
	}
}

func TestManagedClaudePromptDisableWithoutDependenciesOrOverrideAdmission(t *testing.T) {
	resetSetupSeams(t)
	profile := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", profile)
	root := filepath.Join(profile, managedClaudeDirectory)
	settings := filepath.Join(profile, "settings.json")
	userSettings := []byte(`{"env":{"KEEP":"yes"},"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"bash user-prompt-submit.sh"}]}]}}`)
	if err := os.WriteFile(settings, userSettings, 0600); err != nil {
		t.Fatal(err)
	}
	files, err := loadClaudeBundle()
	if err != nil {
		t.Fatal(err)
	}
	bash, err := files.stage(root, "")
	if err != nil {
		t.Fatal(err)
	}
	lookPathFn = func(name string) (string, error) {
		if name != "claude" {
			return "", os.ErrNotExist
		}
		return "claude.exe", nil
	}
	runCommand = func(_ string, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "plugin marketplace list --json":
			return json.Marshal([]claudeMarketplace{{Name: "engram", Source: "directory", Path: root}})
		case "plugin list --json":
			return json.Marshal([]claudeInstalledPlugin{{ID: managedClaudePlugin, Scope: "user", Enabled: true, Version: bash.version, ReadFromFolder: bash.folder}})
		case "plugin marketplace update engram", "plugin update engram@engram --scope user":
			return nil, nil
		default:
			t.Fatalf("unexpected Claude operation %v", args)
			return nil, nil
		}
	}
	if err := SetClaudeWindowsNativePrompt(false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(settings)
	if err != nil || !bytes.Equal(got, userSettings) {
		t.Fatalf("disable changed the user's settings: %v", err)
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
