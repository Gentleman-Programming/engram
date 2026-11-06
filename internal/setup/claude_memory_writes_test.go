package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestClaudeMemoryWritePermissionOptIn(t *testing.T) {
	resetSetupSeams(t)
	useTestHome(t)
	path := ClaudeCodeSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	before := `{"theme":"dark","permissions":{"allow":["Read"],"deny":["mcp__engram__mem_save"],"ask":["Bash"]}}`
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := AddClaudeCodeMemoryWriteAllowlist(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := AddClaudeCodeMemoryWriteAllowlist(); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("repeat opt-in changed settings")
	}
	var got struct {
		Theme       string                              `json:"theme"`
		Permissions struct{ Allow, Deny, Ask []string } `json:"permissions"`
	}
	if err := json.Unmarshal(second, &got); err != nil {
		t.Fatal(err)
	}
	if got.Theme != "dark" || !reflect.DeepEqual(got.Permissions.Deny, []string{"mcp__engram__mem_save"}) || !reflect.DeepEqual(got.Permissions.Ask, []string{"Bash"}) {
		t.Fatalf("other settings changed: %s", second)
	}
	// Independent expected list: no wildcard, destructive tool or future profile expansion.
	want := []string{"Read"}
	for _, prefix := range []string{"mcp__engram__", "mcp__plugin_engram_engram__"} {
		for _, tool := range []string{"mem_save", "mem_update", "mem_review", "mem_save_prompt", "mem_pin", "mem_unpin", "mem_session_summary", "mem_session_start", "mem_session_end", "mem_capture_passive", "mem_judge", "mem_compare"} {
			want = append(want, prefix+tool)
		}
	}
	sort.Strings(want)
	sort.Strings(got.Permissions.Allow)
	if !reflect.DeepEqual(got.Permissions.Allow, want) {
		t.Fatalf("unexpected permission grant: %v", got.Permissions.Allow)
	}
}

func TestClaudeMemoryWritePermissionRejectsInvalidSettings(t *testing.T) {
	for _, raw := range []string{`{`, `null`, `[]`, `{"permissions":null}`, `{"permissions":[]}`, `{"permissions":{"allow":42}}`, `{"permissions":{"allow":[1]}}`} {
		t.Run(raw, func(t *testing.T) {
			resetSetupSeams(t)
			useTestHome(t)
			path := ClaudeCodeSettingsPath()
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if err := AddClaudeCodeMemoryWriteAllowlist(); err == nil {
				t.Fatal("invalid settings accepted")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != raw {
				t.Fatal("invalid settings overwritten")
			}
		})
	}
}

func TestClaudeMemoryWritePermissionCreatesSettings(t *testing.T) {
	resetSetupSeams(t)
	useTestHome(t)
	if err := AddClaudeCodeMemoryWriteAllowlist(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(ClaudeCodeSettingsPath())
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct{ Permissions struct{ Allow []string } }
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Permissions.Allow) != 24 {
		t.Fatalf("got %d permission entries", len(cfg.Permissions.Allow))
	}
}
