package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func piSource(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := AgentContractJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc agentContractDocument
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return piDecode(t, doc.Operations[name].InputSchema)
}

func piDecode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var s map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPiInputSourceDrift(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		mutate     func(map[string]any)
	}{
		{"type", "mem_context", func(s map[string]any) {
			s["properties"].(map[string]any)["max_bytes"].(map[string]any)["type"] = "string"
		}},
		{"bound", "mem_context", func(s map[string]any) {
			s["properties"].(map[string]any)["max_bytes"].(map[string]any)["minimum"] = json.Number("2")
		}},
		{"null-bound", "mem_context", func(s map[string]any) {
			s["properties"].(map[string]any)["max_bytes"].(map[string]any)["minimum"] = nil
		}},
		{"cwd", "mem_save", func(s map[string]any) {
			s["properties"].(map[string]any)["cwd"] = map[string]any{"type": "string", "minLength": json.Number("2")}
		}},
		{"missing-field", "mem_context", func(s map[string]any) { delete(s["properties"].(map[string]any), "max_bytes") }},
		{"missing-properties", "mem_save", func(s map[string]any) { delete(s, "properties") }},
		{"omitted-field-drift", "mem_save", func(s map[string]any) {
			s["properties"].(map[string]any)["session_id"].(map[string]any)["minLength"] = json.Number("2")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := piSource(t, tc.tool)
			tc.mutate(s)
			before, _ := json.Marshal(s)
			_, err := piInputProjection(tc.tool, s)
			if err == nil || err.Error() != "Pi input: unsupported source for "+tc.tool {
				t.Errorf("error=%v; want deterministic unsupported source", err)
			}
			after, _ := json.Marshal(s)
			if !bytes.Equal(before, after) {
				t.Error("source mutated")
			}
		})
	}
}

func TestPiInputCurrentSource(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv("ENGRAM_DATA_DIR", filepath.Join(scratch, "must-not-exist"))
	artifact := "../../docs/contracts/agent-contract-v1.json"
	before, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	for name := range ProfileAgent {
		t.Run(name, func(t *testing.T) {
			s := piSource(t, name)
			if name == "mem_save" {
				s["required"] = append(s["required"].([]any), "future_required")
			}
			raw, _ := json.Marshal(s)
			projected, err := piInputProjection(name, s)
			if err != nil {
				t.Fatal(err)
			}
			if name == "mem_save" && !slices.Contains(projected["required"].([]any), any("future_required")) {
				t.Error("unrelated required name discarded")
			}
			after, _ := json.Marshal(s)
			if !bytes.Equal(raw, after) {
				t.Error("source mutated")
			}
		})
	}
	after, err := os.ReadFile(artifact)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("artifact changed", err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatal("store writes", entries, err)
	}
	t.Logf("artifact SHA256=%x; no store writes", sha256.Sum256(after))
}

func TestPiInputShape(t *testing.T) {
	for _, tc := range []struct {
		a, b  string
		equal bool
	}{
		{`{"type":"object"}`, `{"type":"object","required":[],"description":"annotation"}`, true},
		{`{"required":["a","b"]}`, `{"required":["b","a"]}`, true},
		{`{"properties":{"description":{"type":"string"}}}`, `{"properties":{}}`, false},
		{`{"properties":{"description":{"type":"string"}}}`, `{"properties":{"description":{"type":"number"}}}`, false},
		{`{"minimum":1}`, `{"minimum":2}`, false},
		{`{"unknown":{"description":"a"}}`, `{"unknown":{"description":"b"}}`, false},
	} {
		if reflect.DeepEqual(piInputShape(piDecode(t, []byte(tc.a)), true), piInputShape(piDecode(t, []byte(tc.b)), true)) != tc.equal {
			t.Errorf("comparison %s vs %s", tc.a, tc.b)
		}
	}
}

func TestPiInputCaptureBoundary(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`null`, "invalid capture"}, {`[]`, "invalid capture"}, {`{}`, "invalid capture"}, {`[`, "invalid capture"},
		{`[] null`, "invalid capture"}, {`[{"name":"mem_search","parameters":null}]`, "invalid capture"},
		{`[{"name":"mem_search","parameters":{},"extra":true}]`, "invalid capture"},
		{`[{"name":"mem_search","parameters":{}}]`, "inventory differs"},
		{`[{"name":"mem_search","parameters":{}},{"name":"mem_search","parameters":{}}]`, "duplicate registration"},
	} {
		raw := []byte(tc.raw)
		before := sha256.Sum256(raw)
		err := VerifyPiInputSchemas(raw)
		if err == nil || err.Error() != "Pi input: "+tc.want {
			t.Errorf("%s: error=%v", tc.raw, err)
		}
		if sha256.Sum256(raw) != before {
			t.Error("capture mutated")
		}
	}
}

const piCaptureScript = `
import { withPluginSandbox, importPluginFromSandbox } from './plugin/pi/test/plugin-sandbox.mjs';
const capture = await withPluginSandbox('engram-pi-projection-', async ({sandbox}) => {
 const tools = []; const register = await importPluginFromSandbox(sandbox);
 register({ registerTool({name, parameters}) { tools.push({name, parameters}); }, on() {} });
 return JSON.stringify(tools);
}, {realTypeBox:true});
process.stdout.write(capture);
`

func piRegistrationCapture(ctx context.Context, root, executable, script string) ([]byte, bool, error) {
	node, err := exec.LookPath(executable)
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return nil, true, errors.New("Pi schema prerequisites absent: Node")
	}
	if err != nil {
		return nil, false, errors.New("Pi schema preflight failed")
	}
	// Match plugin-sandbox.mjs's fixed checkout-local dependency, never ancestors.
	_, err = os.Lstat(filepath.Join(root, "plugin/pi/node_modules/typebox"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true, errors.New("Pi schema prerequisites absent: TypeBox")
	}
	if err != nil {
		return nil, false, errors.New("Pi schema preflight failed")
	}
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil || stderr.Len() != 0 {
		return nil, false, errors.New("Pi schema capture failed")
	}
	return raw, false, nil
}

// Copy only source inputs, never checkout state, credentials, stores or modules.
func piCopySources(t *testing.T, root string, goSources bool) string {
	t.Helper()
	dst := t.TempDir()
	paths := []string{"plugin/pi/index.ts", "plugin/pi/compaction-recovery.js", "plugin/pi/memory-tool-chrome.js", "plugin/pi/private-redaction.js", "plugin/pi/runtime-contract.js", "plugin/pi/runtime-contract.json", "plugin/pi/test/plugin-sandbox.mjs"}
	if goSources {
		paths = append(paths, "go.mod", "go.sum", "docs/contracts/agent-contract-v1.json")
		err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") && filepath.Base(path) != "agent_contract_pi_test.go" {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err == nil {
				paths = append(paths, rel)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range paths {
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dst, path)
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(target, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func TestPiInputCapturePrerequisites(t *testing.T) {
	if testing.Short() {
		t.Skip("external Pi schema checks")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// A nonexistent executable is an isolated seam, not a PATH mutation.
	_, missing, err := piRegistrationCapture(context.Background(), root, filepath.Join(t.TempDir(), "missing-node"), piCaptureScript)
	if !missing || err == nil || err.Error() != "Pi schema prerequisites absent: Node" {
		t.Errorf("missing Node: missing=%v err=%v", missing, err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		if !errors.Is(err, exec.ErrNotFound) {
			t.Fatal(err)
		}
		t.Log("Node absent: only executable-absence classification ran")
		return
	}
	for _, tc := range []struct {
		name, manifest, script, want string
		missing                      bool
	}{
		{"absent-package", "", piCaptureScript, "Pi schema prerequisites absent: TypeBox", true},
		{"wrong-version", `{"name":"typebox","version":"0.0.0"}`, piCaptureScript, "Pi schema capture failed", false},
		{"wrong-package-name", `{"name":"wrong","version":"1.3.34"}`, piCaptureScript, "Pi schema capture failed", false},
		{"invalid-manifest", `{`, piCaptureScript, "Pi schema capture failed", false},
		{"broken-exports", `{"name":"typebox","version":"1.3.34","exports":"./missing.js"}`, piCaptureScript, "Pi schema capture failed", false},
		{"capture-exception", `{}`, `throw new Error('capture exception')`, "Pi schema capture failed", false},
		{"stderr-not-absence", `{}`, `process.stderr.write('missing dependency')`, "Pi schema capture failed", false},
		{"cleanup-exception", `{}`, `try { process.stdout.write('[]') } finally { throw new Error('cleanup failed') }`, "Pi schema capture failed", false},
		{"deadline", `{}`, piCaptureScript, "Pi schema capture failed", false},
		{"malformed-output", `{}`, `process.stdout.write('null')`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := piCopySources(t, root, false)
			if tc.manifest != "" {
				path := filepath.Join(copy, "plugin/pi/node_modules/typebox")
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "package.json"), []byte(tc.manifest), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if tc.name == "deadline" {
				cancel()
			}
			defer cancel()
			raw, missing, err := piRegistrationCapture(ctx, copy, "node", tc.script)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if missing != tc.missing || got != tc.want {
				t.Errorf("missing=%v err=%q", missing, got)
			}
			if tc.name == "malformed-output" {
				if err := VerifyPiInputSchemas(raw); err == nil || err.Error() != "Pi input: invalid capture" {
					t.Errorf("malformed output: %v", err)
				}
			}
		})
	}
}

func TestPiInputRealCapture(t *testing.T) {
	if testing.Short() {
		t.Skip("external Pi schema capture")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	t.Setenv("ENGRAM_DATA_DIR", filepath.Join(scratch, "must-not-exist"))
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, scratch)
	}
	authority, err := AgentContractJSON()
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(root, "docs/contracts/agent-contract-v1.json")
	before, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	packagePath := filepath.Join(root, "plugin/pi/node_modules/typebox/package.json")
	packageBefore, packageErr := os.ReadFile(packagePath)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	capture, missing, err := piRegistrationCapture(ctx, root, "node", piCaptureScript)
	if missing && os.Getenv("ENGRAM_PI_SCHEMA_REQUIRE_REAL") != "1" {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if packageErr != nil {
		t.Fatal(packageErr)
	}
	repeated, missing, err := piRegistrationCapture(ctx, root, "node", piCaptureScript)
	if err != nil || missing || !bytes.Equal(capture, repeated) {
		t.Fatal("repeated capture differs", err)
	}
	check := func(t *testing.T, raw []byte, want string) {
		t.Helper()
		hash := sha256.Sum256(raw)
		got := ""
		if err := VerifyPiInputSchemas(raw); err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("error=%q want=%q", got, want)
		}
		if sha256.Sum256(raw) != hash {
			t.Error("capture mutated")
		}
	}
	t.Run("accept", func(t *testing.T) { check(t, capture, "") })
	type registration struct {
		Name       string         `json:"name"`
		Parameters map[string]any `json:"parameters"`
	}
	for _, tc := range []struct {
		name, tool string
		mutate     func(map[string]any)
	}{
		{"missing-field", "mem_search", func(s map[string]any) { delete(s["properties"].(map[string]any), "query") }},
		{"extra-description-property", "mem_search", func(s map[string]any) {
			s["properties"].(map[string]any)["description"] = map[string]any{"type": "string"}
		}},
		{"type", "mem_search", func(s map[string]any) { s["properties"].(map[string]any)["query"].(map[string]any)["type"] = "number" }},
		{"required", "mem_search", func(s map[string]any) { delete(s, "required") }},
		{"max-bytes", "mem_context", func(s map[string]any) {
			s["properties"].(map[string]any)["max_bytes"].(map[string]any)["minimum"] = float64(0)
		}},
		{"batch-bound", "mem_judge_many", func(s map[string]any) {
			s["properties"].(map[string]any)["judgments"].(map[string]any)["maxItems"] = float64(33)
		}},
		{"consolidation-constraint", "mem_consolidate", func(s map[string]any) {
			delete(s["properties"].(map[string]any)["ids_to_delete"].(map[string]any), "uniqueItems")
		}},
		{"unapproved-keyword", "mem_search", func(s map[string]any) { s["additionalProperties"] = false }},
		{"host-session-injection", "mem_session_finalize", func(s map[string]any) {
			s["properties"].(map[string]any)["session_id"] = map[string]any{"type": "string"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tools []registration
			if err := json.Unmarshal(capture, &tools); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tool := range tools {
				if tool.Name == tc.tool {
					tc.mutate(tool.Parameters)
					found = true
				}
			}
			if !found {
				t.Fatal(tc.tool)
			}
			raw, err := json.Marshal(tools)
			if err != nil {
				t.Fatal(err)
			}
			check(t, raw, "Pi input: "+tc.tool+" schema differs")
		})
	}
	var tools []registration
	if err := json.Unmarshal(capture, &tools); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		tools []registration
		want  string
	}{
		{"missing-tool", tools[1:], "inventory differs"},
		{"extra-tool", append(append([]registration{}, tools...), registration{"mem_unknown", map[string]any{"type": "object"}}), "inventory differs"},
		{"duplicate-tool", append(append([]registration{}, tools...), tools[0]), "duplicate registration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.tools)
			if err != nil {
				t.Fatal(err)
			}
			check(t, raw, "Pi input: "+tc.want)
		})
	}
	check(t, append(append([]byte{}, capture...), []byte(" null")...), "Pi input: invalid capture")
	after, err := os.ReadFile(artifact)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("artifact changed", err)
	}
	packageAfter, err := os.ReadFile(packagePath)
	if err != nil || !bytes.Equal(packageBefore, packageAfter) {
		t.Fatal("installed TypeBox manifest changed", err)
	}
	current, err := AgentContractJSON()
	if err != nil || !bytes.Equal(authority, current) {
		t.Fatal("Go authority changed", err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatal("store/sandbox writes remain", entries, err)
	}
	t.Logf("real TypeBox 1.3.34 ran twice; capture SHA256=%x artifact SHA256=%x; no store writes", sha256.Sum256(capture), sha256.Sum256(after))
}

func TestPiInputDependencyCopy(t *testing.T) {
	if testing.Short() {
		t.Skip("external Pi schema dependency probe")
	}
	if os.Getenv("ENGRAM_PI_SCHEMA_PROBE_CHILD") == "1" {
		return
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	copy := piCopySources(t, root, true)
	prerequisite := "TypeBox"
	if _, err := exec.LookPath("node"); errors.Is(err, exec.ErrNotFound) {
		prerequisite = "Node"
	} else if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"", "1"} {
		t.Run("required="+required, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "test", "./internal/mcp", "-run", "^TestPiInput", "-count=1", "-v")
			cmd.Dir = copy
			cmd.Env = slices.DeleteFunc(os.Environ(), func(s string) bool {
				return strings.HasPrefix(s, "ENGRAM_PI_SCHEMA_REQUIRE_REAL=") || strings.HasPrefix(s, "ENGRAM_PI_SCHEMA_PROBE_CHILD=")
			})
			cmd.Env = append(cmd.Env, "ENGRAM_PI_SCHEMA_REQUIRE_REAL="+required, "ENGRAM_PI_SCHEMA_PROBE_CHILD=1")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			stdout, err := cmd.Output()
			code := 0
			if err != nil {
				exit, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			want := 0
			marker := "--- SKIP: TestPiInputRealCapture"
			if required == "1" {
				want = 1
				marker = "--- FAIL: TestPiInputRealCapture"
			}
			if code != want || stderr.Len() != 0 || !strings.Contains(string(stdout), marker) || !strings.Contains(string(stdout), "Pi schema prerequisites absent: "+prerequisite) {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr.String())
			}
			if required == "1" && strings.Count(string(stdout), "\n--- FAIL:") != 1 {
				t.Fatalf("unexpected additional failure: %s", stdout)
			}
			t.Logf("isolated copy command: ENGRAM_PI_SCHEMA_REQUIRE_REAL=%s GOPROXY=off GOSUMDB=off go test ./internal/mcp -run '^TestPiInput' -count=1 -v; exit=%d stdout=%s stderr=%q", required, code, stdout, stderr.String())
		})
	}
}
