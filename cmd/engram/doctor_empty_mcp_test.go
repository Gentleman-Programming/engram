package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/diagnostic"
)

func TestDoctorBlankMCPConfig(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		missing   bool
		wantError bool
	}{
		{name: "absent", missing: true},
		{name: "empty"},
		{name: "JSON whitespace", body: " \t\r\n"},
		{name: "empty object", body: "{}"},
		{name: "malformed JSON", body: "{invalid", wantError: true},
		{name: "non-JSON whitespace", body: "\u00a0", wantError: true},
	} {
		for _, laterStale := range []bool{false, true} {
			name := tc.name + "/alone"
			if laterStale {
				name = tc.name + "/later stale adapter"
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("USERPROFILE", home)
				t.Setenv("XDG_CONFIG_HOME", "")
				t.Setenv("APPDATA", "")
				t.Setenv("KIMI_CODE_HOME", "")
				cfg := testConfig(t)
				write := func(path string, data []byte) {
					t.Helper()
					if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, data, 0644); err != nil {
						t.Fatal(err)
					}
				}
				first := filepath.Join(home, ".gemini", "config", "mcp_config.json")
				if !tc.missing {
					write(first, []byte(tc.body))
				}
				missingBinary := filepath.Join(home, "missing-engram")
				if laterStale {
					raw, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
						"engram": map[string]any{"command": missingBinary, "args": []string{"mcp", "--tools=agent"}},
					}})
					if err != nil {
						t.Fatal(err)
					}
					write(filepath.Join(home, ".codeium", "windsurf", "mcp_config.json"), raw)
				}
				snapshot := func() map[string]string {
					t.Helper()
					files := map[string]string{}
					err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
						if err != nil {
							return err
						}
						if !entry.IsDir() {
							data, err := os.ReadFile(path)
							if err != nil {
								return err
							}
							files[path] = string(data)
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					return files
				}
				before := snapshot()
				oldExit := exitFunc
				t.Cleanup(func() { exitFunc = oldExit })
				exitCode := 0
				exitFunc = func(code int) { exitCode = code }
				withArgs(t, "engram", "doctor", "--json")
				out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
				if exitCode != 0 || stderr != "" {
					t.Fatalf("exit=%d stderr=%q stdout=%s", exitCode, stderr, out)
				}
				if after := snapshot(); !reflect.DeepEqual(before, after) {
					t.Fatalf("doctor modified client files: before=%v after=%v", before, after)
				}
				var report diagnostic.Report
				if err := json.Unmarshal([]byte(out), &report); err != nil {
					t.Fatalf("invalid doctor JSON: %v\n%s", err, out)
				}
				wantStatus := "ok"
				if tc.wantError {
					wantStatus = "error"
				} else if laterStale {
					wantStatus = "warning"
				}
				if report.Status != wantStatus {
					t.Fatalf("status=%q, want %q\n%s", report.Status, wantStatus, out)
				}
				inspectionErrors, staleFindings := 0, 0
				for _, check := range report.Checks {
					switch check.CheckID {
					case "mcp_inspection_error":
						inspectionErrors++
						if !strings.Contains(check.Message, first) || check.Result != "error" {
							t.Fatalf("unexpected inspection error: %+v", check)
						}
					case "stale_mcp_command":
						for _, finding := range check.Findings {
							staleFindings++
							var evidence struct{ Slug, Command string }
							if err := json.Unmarshal(finding.Evidence, &evidence); err != nil {
								t.Fatal(err)
							}
							if evidence.Slug != "windsurf" || evidence.Command != missingBinary || finding.SafeNextStep != "engram setup windsurf" {
								t.Fatalf("unexpected stale finding: %+v", finding)
							}
						}
					}
				}
				wantErrors, wantStale := 0, 0
				if tc.wantError {
					wantErrors = 1
				} else if laterStale {
					wantStale = 1
				}
				if inspectionErrors != wantErrors || report.Summary.Errors != wantErrors || staleFindings != wantStale || report.Summary.Warnings != wantStale {
					t.Fatalf("unexpected diagnostic counts: %s", out)
				}
			})
		}
	}
}
