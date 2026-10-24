package setup

import (
	"os"
	"strings"
	"testing"
)

func TestInstallCodexTOMLPreservation(t *testing.T) {
	for _, tt := range []struct{ name, input, keep, absent string }{
		{"multiline basic", "model_instructions_file = \"\"\"\ninstructions.md\"\"\"\nkeep = \"yes\"\n", "keep = \"yes\"\n", "instructions.md"},
		{"multiline literal", "experimental_compact_prompt_file = '''\ncompact.md'''\nkeep = 'yes'\n", "keep = 'yes'\n", "compact.md"},
		{"unrelated multiline", "notes = \"\"\"\nmodel_instructions_file = 'text'\n[mcp_servers.engram]\n[not.a.table]\n\"\"\"\n", "notes = \"\"\"\nmodel_instructions_file = 'text'\n[mcp_servers.engram]\n[not.a.table]\n\"\"\"\n", ""},
		{"quoted keys", "\"model_instructions_file\" = 'old'\n'experimental_compact_prompt_file' = \"old2\"\nkeep = true\n", "keep = true\n", "old"},
		{"escaped key", "\"model_instructions_\\u0066ile\" = 'old'\nkeep = true\n", "keep = true\n", "old"},
		{"table entries", "model_instructions_file_extra = 'keep'\n[profile]\nmodel_instructions_file = 'table'\n", "model_instructions_file_extra = 'keep'\n[profile]\nmodel_instructions_file = 'table'\n", ""},
		{"existing setup", "model = 'gpt-5'\n[mcp_servers.engram]\ncommand = 'old'\nargs = ['old']\n[mcp_servers.other]\ncommand = 'other'\n", "[mcp_servers.other]\ncommand = 'other'\n", "'old'"},
		{"CRLF and BOM", "\ufeff# keep\r\nmodel_instructions_file = '''old'''\r\nnotes = '''a\r\nb'''\r\n", "# keep\r\nnotes = '''a\r\nb'''\r\n", "old"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resetSetupSeams(t)
			useIsolatedProfile(t)
			path := codexConfigPath()
			if err := os.MkdirAll(strings.TrimSuffix(path, "config.toml"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.input), 0644); err != nil {
				t.Fatal(err)
			}
			lookPathFn = func(string) (string, error) { return "mock-codex", nil }
			calls, writes := 0, 0
			runCommand = func(string, ...string) ([]byte, error) { calls++; return []byte("ok"), nil }
			writeFileFn = func(p string, b []byte, m os.FileMode) error {
				if p == path {
					writes++
				}
				return os.WriteFile(p, b, m)
			}
			var first string
			for run := 0; run < 2; run++ {
				result, err := Install("codex")
				if err != nil || result == nil {
					t.Fatalf("install: %#v, %v", result, err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				got := string(raw)
				if !strings.Contains(got, tt.keep) || tt.absent != "" && strings.Contains(got, tt.absent) {
					t.Fatalf("content changed incorrectly:\n%s", got)
				}
				if run == 0 {
					first = got
				} else if got != first {
					t.Fatal("not idempotent")
				}
				if writes != run+1 {
					t.Fatalf("config writes = %d, want %d", writes, run+1)
				}
			}
			if calls != 4 {
				t.Fatalf("plugin calls=%d", calls)
			}
			if strings.HasPrefix(tt.input, "\ufeff") && !strings.HasPrefix(first, "\ufeff") {
				t.Fatal("BOM lost")
			}
		})
	}
}

func TestInstallCodexTOMLRejectedUnchanged(t *testing.T) {
	for _, input := range []string{
		"notes = \"\"\"\nunfinished\n",
		"model_instructions_file = [\"old\",\n",
		"notes = 'unfinished\n",
		"model_instructions_file = { path = 'old' }\n",
		"mcp_servers.engram.command = \"old\"\n",
		"mcp_servers = { engram = { command = 'old' } }\n",
		"[mcp_servers]\nengram.command = 'old'\n",
		"[mcp_servers]\nengram = { command = 'old' }\n",
		"[[mcp_servers]]\ncommand = 'old'\n",
	} {
		t.Run(input, func(t *testing.T) {
			resetSetupSeams(t)
			useIsolatedProfile(t)
			path := codexConfigPath()
			if err := os.MkdirAll(strings.TrimSuffix(path, "config.toml"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(input), 0644); err != nil {
				t.Fatal(err)
			}
			writes, calls := 0, 0
			writeFileFn = func(string, []byte, os.FileMode) error { writes++; return nil }
			lookPathFn = func(string) (string, error) { calls++; return "mock", nil }
			runCommand = func(string, ...string) ([]byte, error) { calls++; return nil, nil }
			result, err := Install("codex")
			if result != nil || err == nil {
				t.Fatalf("want rejection, got %#v, %v", result, err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || string(got) != input {
				t.Fatalf("config changed: %q %v", got, readErr)
			}
			if writes != 0 || calls != 0 {
				t.Fatalf("writes=%d, plugin calls=%d", writes, calls)
			}
		})
	}
}
