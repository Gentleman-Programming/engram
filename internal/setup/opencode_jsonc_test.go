package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

func TestOpenCodeMCPRefreshesExistingLocalCommand(t *testing.T) {
	for _, tc := range []struct {
		name     string
		original string
		v2       bool
	}{
		{
			name: "V1",
			original: `{
  // preserve V1 comments and formatting
  "mcp": {
    "engram": {"type":"local","command":["/removed/engram","mcp","--tools=agent","--custom"],"enabled":false,"environment":{"DEBUG":"1"}},
    "other": {"type":"local","command":["other"]}
  }
}`,
		},
		{
			name: "V2",
			original: `{
  "mcp": {
    "timeout": {"request": 5000},
    "servers": {
      // preserve V2 comments and formatting
      "engram": {"type":"local","command":["/removed/engram","mcp","--tools=agent","--custom"],"disabled":true,"environment":{"DEBUG":"1"}},
      "other": {"type":"local","command":["other"]}
    }
  }
}`,
			v2: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetSetupSeams(t)
			useIsolatedProfile(t)
			canonical := filepath.Join(t.TempDir(), "current", "engram")
			osExecutable = func() (string, error) { return canonical, nil }
			path := filepath.Join(openCodeConfigDir(), "opencode.jsonc")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.original), 0644); err != nil {
				t.Fatal(err)
			}

			writes := 0
			writeFileFn = func(name string, data []byte, mode os.FileMode) error {
				writes++
				return os.WriteFile(name, data, mode)
			}
			if err := injectOpenCodeMCP(); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			canonicalJSON, err := json.Marshal(canonical)
			if err != nil {
				t.Fatal(err)
			}
			want := bytes.Replace([]byte(tc.original), []byte(`"/removed/engram"`), canonicalJSON, 1)
			if !bytes.Equal(after, want) {
				t.Fatalf("refresh changed bytes other than command[0]:\nwant:\n%s\ngot:\n%s", want, after)
			}

			var config map[string]any
			if err := json.Unmarshal(stripJSONC(after), &config); err != nil {
				t.Fatal(err)
			}
			mcp := config["mcp"].(map[string]any)
			entry := mcp["engram"]
			if tc.v2 {
				if _, legacy := mcp["engram"]; legacy {
					t.Fatalf("V2 configuration gained a legacy mcp.engram entry: %#v", mcp)
				}
				entry = mcp["servers"].(map[string]any)["engram"]
			}
			engram := entry.(map[string]any)
			if command := engram["command"].([]any); !reflect.DeepEqual(command, []any{canonical, "mcp", "--tools=agent", "--custom"}) {
				t.Fatalf("command = %#v, want canonical executable with preserved arguments", command)
			}
			if engram["environment"].(map[string]any)["DEBUG"] != "1" {
				t.Fatalf("custom options were not preserved: %#v", engram)
			}

			if err := injectOpenCodeMCP(); err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, second) || writes != 1 {
				t.Fatalf("setup is not convergent: writes=%d second=%q err=%v", writes, second, err)
			}
		})
	}
}

func TestReconcileOpenCodeMCPCommandGuardsAndFailures(t *testing.T) {
	canonical := filepath.Join(t.TempDir(), "current", "engram")
	for _, tc := range []struct {
		name           string
		layout         string
		entry          string
		marshalErr     error
		writeErr       error
		wantErr        string
		wantWriteCount int
	}{
		{
			name:       "returns marshal command error",
			layout:     "V1",
			entry:      `{"type":"local","command":["/removed/engram"]}`,
			marshalErr: errors.New("marshal failed"),
			wantErr:    "marshal engram command: marshal failed",
		},
		{
			name:           "returns write config error",
			layout:         "V2",
			entry:          `{"type":"local","command":["/removed/engram"]}`,
			writeErr:       errors.New("disk full"),
			wantErr:        "write config: disk full",
			wantWriteCount: 1,
		},
		{
			name:   "leaves missing command unchanged",
			layout: "V1",
			entry:  `{"type":"local"}`,
		},
		{
			name:   "leaves non array command unchanged",
			layout: "V2",
			entry:  `{"type":"local","command":"/removed/engram"}`,
		},
		{
			name:   "leaves non string command zero unchanged",
			layout: "V1",
			entry:  `{"type":"local","command":[42,"mcp"]}`,
		},
		{
			name:   "leaves canonical command unchanged",
			layout: "V2",
			entry:  `{"type":"local","command":[` + strconv.Quote(canonical) + `,"mcp"]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetSetupSeams(t)
			osExecutable = func() (string, error) { return canonical, nil }

			original := []byte(`{"mcp":{"engram":` + tc.entry + `}}`)
			if tc.layout == "V2" {
				original = []byte(`{"mcp":{"servers":{"engram":` + tc.entry + `}}}`)
			}
			config, err := parseOpenCodeJSONC(original)
			if err != nil {
				t.Fatal(err)
			}
			engram := config.members["mcp"].members["engram"]
			if tc.layout == "V2" {
				engram = config.members["mcp"].members["servers"].members["engram"]
			}

			path := filepath.Join(t.TempDir(), "opencode.jsonc")
			if err := os.WriteFile(path, original, 0644); err != nil {
				t.Fatal(err)
			}
			writes := 0
			writeFileFn = func(name string, data []byte, mode os.FileMode) error {
				writes++
				if tc.writeErr != nil {
					return tc.writeErr
				}
				return os.WriteFile(name, data, mode)
			}
			if tc.marshalErr != nil {
				jsonMarshalFn = func(any) ([]byte, error) { return nil, tc.marshalErr }
			}

			err = reconcileOpenCodeMCPCommand(path, original, engram)
			if got := ""; err != nil {
				got = err.Error()
				if got != tc.wantErr {
					t.Fatalf("error = %q, want %q", got, tc.wantErr)
				}
			} else if tc.wantErr != "" {
				t.Fatalf("error = nil, want %q", tc.wantErr)
			}
			if writes != tc.wantWriteCount {
				t.Fatalf("writes = %d, want %d", writes, tc.wantWriteCount)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, original) {
				t.Fatalf("config changed:\nwant: %s\ngot: %s", original, after)
			}
		})
	}
}

func TestOpenCodeMCPV2PreservesRemoteEntryAndAddsNoLegacyDuplicate(t *testing.T) {
	resetSetupSeams(t)
	useIsolatedProfile(t)
	path := filepath.Join(openCodeConfigDir(), "opencode.jsonc")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := `{"mcp":{"servers":{"engram":{"type":"remote","url":"https://example.com/mcp"},"other":{"type":"local","command":["other"]}}}}`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	writes := 0
	writeFileFn = func(string, []byte, os.FileMode) error { writes++; return nil }
	if err := injectOpenCodeMCP(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != original || writes != 0 {
		t.Fatalf("remote V2 entry changed or legacy entry added: %q writes=%d err=%v", after, writes, err)
	}
}

func TestOpenCodeMCPV2AddsMissingServerWithoutLegacyDuplicate(t *testing.T) {
	resetSetupSeams(t)
	useIsolatedProfile(t)
	path := filepath.Join(openCodeConfigDir(), "opencode.jsonc")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := `{"mcp":{"timeout":{"request":5000},"servers":{"other":{"type":"local","command":["other"]}}}}`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := injectOpenCodeMCP(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(stripJSONC(after), &config); err != nil {
		t.Fatal(err)
	}
	mcp := config["mcp"].(map[string]any)
	if _, legacy := mcp["engram"]; legacy {
		t.Fatalf("V2 configuration gained a legacy mcp.engram entry: %#v", mcp)
	}
	servers := mcp["servers"].(map[string]any)
	if _, registered := servers["engram"]; !registered {
		t.Fatalf("missing V2 mcp.servers.engram: %#v", servers)
	}
	if _, preserved := servers["other"]; !preserved {
		t.Fatalf("other V2 server was not preserved: %#v", servers)
	}
}

func TestOpenCodeMCPRejectsUnsafeJSONC(t *testing.T) {
	for _, original := range []string{
		`null`, `[]`, `{"mcp":null}`, `{"mcp":42}`,
		`{"mcp":{},"mcp":{}}`, `{"mcp":{"other":{},"other":{}}}`,
		`{"mcp":{},"\u006dcp":{}}`, `{"other":{"a":1,"a":2}}`,
		`{"mcp":{/* unfinished}`, `{"mcp":{},} garbage`, `{,}`, `{"mcp":{},,}`, `{"mcp":{"servers":null}}`,
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
