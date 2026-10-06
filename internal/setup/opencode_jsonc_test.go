package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeMCPSetupPreservesJSONC(t *testing.T) {
	for _, original := range []string{
		"// user settings\r\n{\r\n\t\"z\": 9007199254740993, // number\r\n\t\"a\": [1, 2,],\r\n}\r\n",
		`{/* root */ "z":true,"mcp":{/* servers */"other":{"command":["https://example.com/* literal */",],},},"a":false,}`,
		`{ "mcp": { // empty servers` + "\n" + `}, "opaque": "escaped \" // literal" }`,
		`{ "mcp": {"engram":{"command":["custom"],"enabled":false},}, }`,
	} {
		t.Run(original, func(t *testing.T) {
			resetSetupSeams(t)
			useIsolatedProfile(t)
			osExecutable = func() (string, error) { return "", errors.New("bare command") }
			path := filepath.Join(openCodeConfigDir(), "opencode.jsonc")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(original), 0644); err != nil {
				t.Fatal(err)
			}
			// Both files exist: only the JSONC file is the setup target.
			fallback := filepath.Join(openCodeConfigDir(), "opencode.json")
			fallbackBytes := []byte(`{"unrelated":true}`)
			if err := os.WriteFile(fallback, fallbackBytes, 0644); err != nil {
				t.Fatal(err)
			}
			writes := 0
			writeFileFn = func(name string, data []byte, mode os.FileMode) error {
				if name == path {
					writes++
				}
				return os.WriteFile(name, data, mode)
			}
			result, err := Install("opencode")
			if err != nil || !result.MCPConfigured || !result.TUIPluginEnabled || result.Files != 3 {
				t.Fatalf("setup: %+v, %v", result, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			entry, err := json.Marshal(mcpEntry(opencodeObject))
			if err != nil {
				t.Fatal(err)
			}
			insertion := []byte(`"mcp":{"engram":` + string(entry) + `},`)
			if strings.Contains(original, `"mcp"`) {
				insertion = []byte(`"engram":` + string(entry))
				if strings.Contains(original, `"other"`) {
					insertion = append(insertion, ',')
				}
			}
			if strings.Contains(original, `"engram"`) {
				if string(after) != original || writes != 0 {
					t.Fatalf("existing entry changed: %s, writes=%d", after, writes)
				}
			} else {
				// Removing the one inserted member must recover every original byte.
				if bytes.Count(after, insertion) != 1 || string(bytes.Replace(after, insertion, nil, 1)) != original || writes != 1 {
					t.Fatalf("non-surgical change: before=%q after=%q insertion=%q writes=%d", original, after, insertion, writes)
				}
			}
			again, err := Install("opencode")
			if err != nil || !again.MCPConfigured {
				t.Fatalf("second setup: %+v, %v", again, err)
			}
			second, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, second) {
				t.Fatalf("not idempotent: %q, %v", second, err)
			}
			other, err := os.ReadFile(fallback)
			if err != nil || !bytes.Equal(other, fallbackBytes) {
				t.Fatalf("fallback file changed: %q, %v", other, err)
			}
		})
	}
}

func TestOpenCodeMCPRejectsUnsafeJSONC(t *testing.T) {
	for _, original := range []string{
		`null`, `[]`, `{"mcp":null}`, `{"mcp":42}`,
		`{"mcp":{},"mcp":{}}`, `{"mcp":{"other":{},"other":{}}}`,
		`{"mcp":{},"\u006dcp":{}}`, `{"other":{"a":1,"a":2}}`,
		`{"mcp":{/* unfinished}`, `{"mcp":{},} garbage`, `{,}`, `{"mcp":{},,}`,
	} {
		t.Run(original, func(t *testing.T) {
			resetSetupSeams(t)
			useIsolatedProfile(t)
			path := filepath.Join(openCodeConfigDir(), "opencode.jsonc")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(original), 0644); err != nil {
				t.Fatal(err)
			}
			writes := 0
			writeFileFn = func(string, []byte, os.FileMode) error { writes++; return nil }
			err := injectOpenCodeMCP()
			if err == nil {
				t.Fatal("unsafe document accepted")
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != original || writes != 0 {
				t.Fatalf("rejected input changed: %q writes=%d err=%v", after, writes, readErr)
			}
		})
	}
}
