package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
)

// VerifyPiInputSchemas checks serialized [{"name":...,"parameters":{...}}]
// registrations against live Go authority and explicit current Pi projections.
// It is pure, excludes schema descriptions, and grants no semantic/release parity.
func VerifyPiInputSchemas(capture []byte) error {
	invalid := errors.New("pi input: invalid capture")
	var tools []struct {
		Name       string         `json:"name"`
		Parameters map[string]any `json:"parameters"`
	}
	d := json.NewDecoder(bytes.NewReader(capture))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(&tools); err != nil || len(tools) == 0 {
		return invalid
	}
	if d.Decode(new(any)) != io.EOF {
		return invalid
	}
	actual := map[string]map[string]any{}
	for _, t := range tools {
		if t.Name == "" || t.Parameters == nil {
			return invalid
		}
		if _, ok := actual[t.Name]; ok {
			return errors.New("pi input: duplicate registration")
		}
		actual[t.Name] = t.Parameters
	}
	raw, err := AgentContractJSON()
	if err != nil {
		return err
	}
	var doc agentContractDocument
	if err = json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	names := make([]string, 0, len(doc.Operations))
	for name := range doc.Operations {
		if actual[name] == nil || len(actual) != len(doc.Operations) {
			return errors.New("pi input: inventory differs")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var expected map[string]any
		d = json.NewDecoder(bytes.NewReader(doc.Operations[name].InputSchema))
		d.UseNumber()
		if err = d.Decode(&expected); err != nil {
			return err
		}
		expected, err = piInputProjection(name, expected)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(piInputShape(expected, true), piInputShape(actual[name], true)) {
			return fmt.Errorf("pi input: %s schema differs", name)
		}
	}
	return nil
}

// piInputProjection owns only the source-specific rules below. Clone first so
// neither success nor unsupported-source failure mutates authority/caller data.
func piInputProjection(name string, source map[string]any) (map[string]any, error) {
	unsupported := fmt.Errorf("pi input: unsupported source for %s", name)
	raw, err := json.Marshal(source)
	if err != nil {
		return nil, unsupported
	}
	var s map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&s) != nil || s == nil {
		return nil, unsupported
	}
	bad := s["type"] != "object"
	object := func(v any) map[string]any {
		m, ok := v.(map[string]any)
		if !ok {
			bad = true
			return map[string]any{}
		}
		return m
	}
	p := object(s["properties"])
	field := func(m map[string]any, key, kind string) map[string]any {
		f := object(m[key])
		if f["type"] != kind {
			bad = true
		}
		return f
	}
	// Every newly supplied bound must be absent, even if a new Go value matches.
	add := func(f map[string]any, key string, value any) {
		if _, exists := f[key]; exists {
			bad = true
		}
		f[key] = value
	}
	required, ok := s["required"].([]any)
	required = append([]any{}, required...)
	if _, present := s["required"]; present && !ok {
		bad = true
	}
	for _, key := range required {
		if _, ok := key.(string); !ok {
			bad = true
		}
	}
	omit := func(keys ...string) {
		for _, key := range keys {
			f := field(p, key, "string")
			// Omitted transport fields currently have no structural constraints.
			if len(piInputShape(f, true).(map[string]any)) != 1 {
				bad = true
			}
			kept := []any{}
			wasRequired := false
			for _, r := range required {
				if r == key {
					wasRequired = true
				} else {
					kept = append(kept, r)
				}
			}
			if wasRequired != (name == "mem_session_finalize" && key == "session_id") {
				bad = true
			}
			required = kept
			delete(p, key)
		}
	}
	switch name {
	case "mem_search":
		// MCP exposes this selector; the current Pi model surface does not.
		// Admit only the exact optional source shape, not arbitrary omissions.
		f := field(p, "search_mode", "string")
		if !reflect.DeepEqual(piInputShape(f, true), map[string]any{
			"type": "string", "enum": []any{"lexical", "semantic"},
		}) {
			bad = true
		}
		for _, r := range required {
			if r == "search_mode" {
				bad = true
			}
		}
		delete(p, "search_mode")
	case "mem_save", "mem_save_prompt", "mem_session_summary", "mem_session_finalize":
		// agent_contract.go:199-230; session_finalize.go:24-28,44-46: host-owned
		// identity and cwd selection, never MCP recovery tokens/model session overrides.
		omit("session_id", "project_choice_reason", "recovery_token")
		add(p, "cwd", map[string]any{"type": "string"})
		if name == "mem_save" {
			omit("observation")
			field(p, "content", "string")
			for _, r := range required {
				if r == "content" {
					bad = true
				}
			}
			required = append(required, "content")
		}
		if name == "mem_session_finalize" {
			add(field(p, "summary", "string"), "minLength", json.Number("1"))
		}
	case "mem_capture_passive":
		omit("session_id") // agent_contract.go:226-230
	case "mem_get_observation":
		omit("project") // agent_contract.go:172-176
	case "mem_current_project":
		omit("directory") // agent_contract.go:178-182
	case "mem_context":
		// mcp.go:2064-2067: positive-integer MODEL domain, not MCP fallback parity.
		f := field(p, "max_bytes", "number")
		f["type"] = "integer"
		add(f, "minimum", json.Number("1"))
	case "mem_consolidate":
		// agentops/consolidate.go:24-42; store/consolidate.go:24-26 (distinct IDs).
		f := field(p, "ids_to_delete", "array")
		add(f, "minItems", json.Number("2"))
		add(f, "uniqueItems", true)
		item := field(f, "items", "integer")
		add(item, "minimum", json.Number("1"))
		add(item, "maximum", json.Number("9007199254740991"))
		for _, key := range []string{"new_title", "new_content", "expected_project"} {
			add(field(p, key, "string"), "minLength", json.Number("1"))
		}
	case "mem_judge_many":
		// agentops/judgments.go:12-14,40-43: envelope size and finite confidence.
		f := field(p, "judgments", "array")
		add(f, "minItems", json.Number("1"))
		add(f, "maxItems", json.Number("32"))
		confidence := field(object(field(f, "items", "object")["properties"]), "confidence", "number")
		add(confidence, "minimum", json.Number("0"))
		add(confidence, "maximum", json.Number("1"))
	}
	if bad {
		return nil, unsupported
	}
	s["required"] = required
	return s, nil
}

// Schema-aware traversal: property names and arbitrary keyword payloads are not
// schema keywords. Unknown keywords remain compared verbatim, including order.
func piInputShape(v any, schema bool) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	for k, v := range m {
		if schema && k == "description" {
			continue
		}
		if schema && k == "required" {
			if a, ok := v.([]any); ok {
				if len(a) == 0 {
					continue
				}
				a = append([]any{}, a...)
				sort.Slice(a, func(i, j int) bool { return fmt.Sprint(a[i]) < fmt.Sprint(a[j]) })
				v = a
			}
		}
		switch k {
		case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
			if children, ok := v.(map[string]any); ok {
				copy := map[string]any{}
				for name, child := range children {
					copy[name] = piInputShape(child, true)
				}
				v = copy
			}
		case "items", "additionalProperties", "contains", "propertyNames", "not", "if", "then", "else", "unevaluatedProperties", "unevaluatedItems":
			v = piInputShape(v, true)
		case "allOf", "anyOf", "oneOf", "prefixItems":
			if a, ok := v.([]any); ok {
				copy := make([]any, len(a))
				for i, child := range a {
					copy[i] = piInputShape(child, true)
				}
				v = copy
			}
		}
		out[k] = v
	}
	return out
}
