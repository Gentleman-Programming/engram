package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
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
