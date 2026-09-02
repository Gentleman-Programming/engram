package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

type mcpToolSchema struct {
	Types      []string
	Properties map[string]mcpToolSchema
	Required   []string
	Items      *mcpToolSchema
	Enum       []string
	Additional bool
}

func TestMCPToolSchemaCodec(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"root duplicate", `{"x":1,"x":2}`, "/input/x"},
		{"properties duplicate", `{"type":"object","properties":{"a/b":{"type":"string","type":"number"}}}`, "/input/properties/a~1b/type"},
		{"items duplicate", `{"type":"array","items":{"type":"string","type":"number"}}`, "/input/items/type"},
		{"array duplicate", `[{"x":1,"x":2}]`, "/input/0/x"},
		{"malformed", `{"type":`, "malformed"},
		{"trailing", `{"type":"string"} {}`, "trailing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeMCPToolSchema([]byte(tc.raw), "/input")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}

	value, err := decodeMCPToolSchema([]byte(`{"enum":[1.25,900719925474099312345]}`), "/input")
	if err != nil {
		t.Fatal(err)
	}
	enum := value.(map[string]any)["enum"].([]any)
	if enum[0] != json.Number("1.25") || enum[1] != json.Number("900719925474099312345") {
		t.Fatalf("numbers = %#v", enum)
	}
}

func TestNormalizeMCPToolContract(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"supported recursion", `{"type":"object","title":"ignored","properties":{"a/b":{"type":"array","items":{"type":["integer","string"],"description":"ignored"}}},"required":["a/b","a/b"],"additionalProperties":false}`, ""},
		{"missing type", `{"properties":{}}`, "/input"},
		{"unknown keyword", `{"type":"string","pattern":"x"}`, "/input/pattern"},
		{"unknown live-only addition", `{"type":"object","properties":{"new":{"type":"string","pattern":"x"}}}`, "/input/properties/new/pattern"},
		{"malformed properties", `{"type":"object","properties":[]}`, "/input/properties"},
		{"invalid required", `{"type":"object","properties":{},"required":["missing"]}`, "/input/required"},
		{"tuple items", `{"type":"array","items":[{"type":"string"}]}`, "/input/items"},
		{"schema additional", `{"type":"object","additionalProperties":{"type":"string"}}`, "/input/additionalProperties"},
		{"escaped pointer", `{"type":"object","properties":{"a/b~c":{"type":"string","oneOf":[]}}}`, "/input/properties/a~1b~0c/oneOf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeMCPToolContract([]byte(tc.raw), "/input")
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}

	one := mustNormalize(t, `{"type":"string","enum":[900719925474099312345,1.25,1.25]}`)
	two := mustNormalize(t, `{"examples":["ignored"],"enum":[1.25,900719925474099312345],"type":["string"]}`)
	if strings.Join(one.Enum, ",") != "1.25,900719925474099312345" || strings.Join(one.Enum, ",") != strings.Join(two.Enum, ",") {
		t.Fatalf("enum normalization differs: %#v %#v", one.Enum, two.Enum)
	}
}

func TestObserveMCPToolContract(t *testing.T) {
	server := NewServer(newMCPTestStore(t))
	live, err := observeMCPToolContract(server.ListTools())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) == 0 {
		t.Fatal("default registry is empty")
	}
	if len(live) != len(server.ListTools()) {
		t.Fatalf("observed %d of %d live tools", len(live), len(server.ListTools()))
	}
	for name := range server.ListTools() {
		if _, ok := live[name]; !ok {
			t.Fatalf("%s bypassed normalization", name)
		}
	}
}

func mustNormalize(t *testing.T, raw string) mcpToolSchema {
	t.Helper()
	schema, err := normalizeMCPToolContract([]byte(raw), "/input")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func decodeMCPToolSchema(raw []byte, path string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	value, err := uniqueJSONValue(decoder, path)
	if err != nil {
		return nil, fmt.Errorf("%s: malformed-schema: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%s: malformed-schema: trailing JSON value", path)
		}
		return nil, fmt.Errorf("%s: malformed-schema: %w", path, err)
	}
	return value, nil
}

func uniqueJSONValue(decoder *json.Decoder, path string) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		out, seen := map[string]any{}, map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name := key.(string)
			childPath := jsonPointer(path, name)
			if seen[name] {
				return nil, fmt.Errorf("duplicate member at %s", childPath)
			}
			seen[name] = true
			out[name], err = uniqueJSONValue(decoder, childPath)
			if err != nil {
				return nil, err
			}
		}
		_, err = decoder.Token()
		return out, err
	case json.Delim('['):
		out := []any{}
		for index := 0; decoder.More(); index++ {
			value, err := uniqueJSONValue(decoder, fmt.Sprintf("%s/%d", path, index))
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		_, err = decoder.Token()
		return out, err
	default:
		return token, nil
	}
}

func normalizeMCPToolContract(raw []byte, path string) (mcpToolSchema, error) {
	value, err := decodeMCPToolSchema(raw, path)
	if err != nil {
		return mcpToolSchema{}, err
	}
	return normalizeMCPToolSchema(value, path)
}

func normalizeMCPToolSchema(value any, path string) (mcpToolSchema, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return mcpToolSchema{}, schemaError(path, "schema must be an object")
	}
	schema := mcpToolSchema{Properties: map[string]mcpToolSchema{}, Additional: true}
	for _, key := range sortedKeys(object) {
		value, keyPath := object[key], jsonPointer(path, key)
		if excludedMetadata(key) {
			continue
		}
		switch key {
		case "type":
			if text, ok := value.(string); ok {
				value = []any{text}
			}
			var err error
			schema.Types, err = normalizedStrings(value, keyPath, false)
			if err != nil {
				return schema, err
			}
		case "properties":
			properties, ok := value.(map[string]any)
			if !ok {
				return schema, schemaError(keyPath, "properties must be an object")
			}
			for _, name := range sortedKeys(properties) {
				child, err := normalizeMCPToolSchema(properties[name], jsonPointer(keyPath, name))
				if err != nil {
					return schema, err
				}
				schema.Properties[name] = child
			}
		case "required":
			var err error
			schema.Required, err = normalizedStrings(value, keyPath, true)
			if err != nil {
				return schema, err
			}
		case "items":
			child, err := normalizeMCPToolSchema(value, keyPath)
			if err != nil {
				return schema, err
			}
			schema.Items = &child
		case "enum":
			values, ok := value.([]any)
			if !ok || len(values) == 0 {
				return schema, schemaError(keyPath, "enum must be a non-empty array")
			}
			for _, value := range values {
				encoded, err := json.Marshal(value)
				if err != nil {
					return schema, schemaError(keyPath, err.Error())
				}
				schema.Enum = append(schema.Enum, string(encoded))
			}
			schema.Enum = uniqueStrings(schema.Enum)
		case "additionalProperties":
			additional, ok := value.(bool)
			if !ok {
				return schema, fmt.Errorf("%s: unsupported-keyword: only boolean values are supported", keyPath)
			}
			schema.Additional = additional
		default:
			return schema, fmt.Errorf("%s: unsupported-keyword: %s", keyPath, key)
		}
	}
	if len(schema.Types) == 0 {
		return schema, schemaError(path, "type is required")
	}
	if len(schema.Properties) > 0 && !slices.Contains(schema.Types, "object") || schema.Items != nil && !slices.Contains(schema.Types, "array") {
		return schema, schemaError(path, "incompatible recursive shape")
	}
	for _, name := range schema.Required {
		if _, ok := schema.Properties[name]; !ok {
			return schema, schemaError(jsonPointer(path, "required"), "required property is absent")
		}
	}
	return schema, nil
}

func observeMCPToolContract(tools map[string]*mcpserver.ServerTool) (map[string]mcpToolSchema, error) {
	observed := make(map[string]mcpToolSchema, len(tools))
	for name, tool := range tools {
		raw := tool.Tool.RawInputSchema
		if len(raw) == 0 {
			var err error
			raw, err = json.Marshal(tool.Tool.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("/tools/%s: marshal schema: %w", escapePointer(name), err)
			}
		}
		if strings.TrimSpace(string(raw)) == "" {
			return nil, schemaError(jsonPointer("/tools", name), "schema is empty")
		}
		schema, err := normalizeMCPToolContract(raw, jsonPointer("/tools", name))
		if err != nil {
			return nil, err
		}
		observed[name] = schema
	}
	return observed, nil
}
func normalizedStrings(value any, path string, emptyOK bool) ([]string, error) {
	values, ok := value.([]any)
	if !ok || !emptyOK && len(values) == 0 {
		return nil, schemaError(path, "must be a non-empty string array")
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok || !emptyOK && !strings.Contains(",object,array,string,number,integer,boolean,null,", ","+text+",") {
			return nil, schemaError(path, "must contain supported type strings")
		}
		out = append(out, text)
	}
	return uniqueStrings(out), nil
}

func schemaError(path, detail string) error {
	return fmt.Errorf("%s: malformed-schema: %s", path, detail)
}
func uniqueStrings(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func excludedMetadata(key string) bool {
	return strings.Contains(",description,title,default,examples,$comment,deprecated,readOnly,writeOnly,", ","+key+",")
}
func escapePointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
func jsonPointer(path, value string) string {
	if path == "/" {
		return "/" + escapePointer(value)
	}
	return path + "/" + escapePointer(value)
}

func TestFormatMCPToolContract(t *testing.T) {
	contract := map[string]mcpToolSchema{"z": {Types: []string{"string"}, Enum: []string{"900719925474099312345", "1.25"}}, "a": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"z": {Types: []string{"string"}}, "a": {Types: []string{"array"}, Items: &mcpToolSchema{Types: []string{"integer"}}}}, Required: []string{"z", "a"}, Additional: false}}
	got := formatMCPToolContract(contract)
	for _, want := range []string{"{\n  \"version\": \"engram.mcp-tool-contract/v1\",", "\n    \"a\": {", "[\"a\",\"z\"]", "900719925474099312345", "\"items\": {", "\"z\": {\"type\":[\"string\"],\"additionalProperties\":false}", "\"additionalProperties\": false}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("format missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "\"a\":") > strings.Index(got, "\"z\":") || strings.Contains(got, "\"z\": {\n") {
		t.Fatalf("format order or compact leaf is not canonical:\n%s", got)
	}
}

func TestReadMCPToolContractFixture(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{}`, "version"}, {`{"version":"v2","tools":{"x":{"type":"string"}}}`, "unsupported-version"}, {`{"version":"engram.mcp-tool-contract/v1","tools":{},"extra":true}`, "unknown envelope"}, {`{"version":"engram.mcp-tool-contract/v1","tools":{}}`, "tools"}, {`{"version":"engram.mcp-tool-contract/v1","tools":{"x":{"type":"string","pattern":"x"}}}`, "/tools/x/pattern"}, {`{"version":"engram.mcp-tool-contract/v1","tools":{"x":{"type":"string"}}}{}`, "trailing"}, {`{"version":"engram.mcp-tool-contract/v1","tools":{"x":{"type":"string","type":"number"}}}`, "/tools/x/type"},
	} {
		if _, err := readMCPToolContractFixture([]byte(tc.raw)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.want, err)
		}
	}
}

func TestCompareMCPToolContractEnumNumericEquality(t *testing.T) {
	for _, tc := range []struct {
		name, base, live, want string
	}{
		{"numeric lexemes and nested values are equal", `[1,{"nested":[900719925474099312345.0000000000000000000001,1e0]}]`, `[1.0,{"nested":[900719925474099312345.00000000000000000000010,1.0]}]`, ""},
		{"different arbitrary-precision number narrows enum", `900719925474099312345.0000000000000000000001`, `900719925474099312345.0000000000000000000002`, "enum-narrowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := map[string]mcpToolSchema{"x": {Types: []string{"number"}, Enum: []string{tc.base}}}
			live := map[string]mcpToolSchema{"x": {Types: []string{"number"}, Enum: []string{tc.live}}}
			err := compareMCPToolContract(base, live)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCompareMCPToolContractEnumJSONEquality(t *testing.T) {
	for _, tc := range []struct {
		name, base, live, want string
	}{
		{"primitive types differ", `1`, `"1"`, "enum-narrowed"},
		{"array order differs", `[1,2]`, `[2,1]`, "enum-narrowed"},
		{"object members ignore source order", `{"first":1,"second":[2,3]}`, `{"second":[2.0,3.0],"first":1e0}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := map[string]mcpToolSchema{"x": {Types: []string{"number"}, Enum: []string{tc.base}}}
			live := map[string]mcpToolSchema{"x": {Types: []string{"number"}, Enum: []string{tc.live}}}
			err := compareMCPToolContract(base, live)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCompareMCPToolContract(t *testing.T) {
	base := map[string]mcpToolSchema{"x": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"optional": {Types: []string{"integer"}}, "required": {Types: []string{"array"}, Items: &mcpToolSchema{Types: []string{"string"}, Enum: []string{`"a"`}}}}, Required: []string{"required"}, Additional: false}}
	cases := []struct {
		name string
		live map[string]mcpToolSchema
		want string
	}{
		{"compatible widening", map[string]mcpToolSchema{"x": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"optional": {Types: []string{"number", "boolean"}}, "required": {Types: []string{"array"}, Items: &mcpToolSchema{Types: []string{"string"}, Enum: []string{`"a"`, `"b"`}}}, "new": {Types: []string{"boolean"}}}, Required: []string{"required"}, Additional: true}, "new-tool": {Types: []string{"string"}}}, ""},
		{"missing tool", map[string]mcpToolSchema{}, "missing-tool"},
		{"missing property", map[string]mcpToolSchema{"x": {Types: []string{"object"}}}, "missing-property"},
		{"requiredness narrowed", map[string]mcpToolSchema{"x": {Types: []string{"object"}, Properties: base["x"].Properties, Required: []string{"optional", "required"}, Additional: false}}, "requiredness-narrowed"},
		{"new required property", map[string]mcpToolSchema{"x": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"optional": {Types: []string{"integer"}}, "required": base["x"].Properties["required"], "new": {Types: []string{"string"}}}, Required: []string{"required", "new"}, Additional: false}}, "new-required-property"},
		{"type narrowed", map[string]mcpToolSchema{"x": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"optional": {Types: []string{"integer"}}, "required": {Types: []string{"array"}, Items: &mcpToolSchema{Types: []string{"number"}}}}, Required: []string{"required"}, Additional: false}}, "type-narrowed"},
		{"enum narrowed", map[string]mcpToolSchema{"x": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"optional": {Types: []string{"integer"}}, "required": {Types: []string{"array"}, Items: &mcpToolSchema{Types: []string{"string"}, Enum: []string{`"b"`}}}}, Required: []string{"required"}, Additional: false}}, "enum-narrowed"},
		{"unproven optional addition", map[string]mcpToolSchema{"x": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"optional": {Types: []string{"integer"}}, "required": base["x"].Properties["required"], "new": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"nested": {Types: []string{"string"}}}}}, Required: []string{"required"}, Additional: false}}, "unproven-optional-addition"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := compareMCPToolContract(base, tc.live)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	additionalBase := map[string]mcpToolSchema{"x": base["x"]}
	additionalBase["x"] = mcpToolSchema{Types: []string{"object"}, Properties: base["x"].Properties, Required: []string{"required"}, Additional: true}
	if err := compareMCPToolContract(additionalBase, base); err == nil || !strings.Contains(err.Error(), "additional-properties-narrowed") {
		t.Fatalf("additionalProperties narrowing error = %v", err)
	}
	permissiveBase := map[string]mcpToolSchema{"x": {Types: []string{"object"}, Additional: true}}
	permissiveLive := map[string]mcpToolSchema{"x": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"new": {Types: []string{"boolean"}}}, Additional: true}}
	if err := compareMCPToolContract(permissiveBase, permissiveLive); err == nil || !strings.Contains(err.Error(), "/tools/x/properties/new: additional-properties-narrowed") {
		t.Fatalf("permissive additionalProperties optional addition error = %v", err)
	}
}

func TestCompareMCPToolContractDiagnostics(t *testing.T) {
	base := map[string]mcpToolSchema{"a/b": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"x~y": {Types: []string{"string"}}}, Additional: true}, "z": {Types: []string{"string"}}}
	live := map[string]mcpToolSchema{"a/b": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"x~y": {Types: []string{"number"}}}, Additional: false}}
	err := compareMCPToolContract(base, live)
	if err == nil {
		t.Fatal("expected multiple drifts")
	}
	got := err.Error()
	for _, want := range []string{"/tools/a~1b: additional-properties-narrowed: v1=", "/tools/a~1b/properties/x~0y: type-narrowed: v1=", "/tools/z: missing-tool: v1="} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic %q missing from %s", want, got)
		}
	}
	if strings.Index(got, "/tools/a~1b: additional-properties-narrowed") > strings.Index(got, "/tools/a~1b/properties/x~0y: type-narrowed") || strings.Index(got, "/tools/a~1b/properties/x~0y: type-narrowed") > strings.Index(got, "/tools/z: missing-tool") {
		t.Fatalf("diagnostics are not sorted: %s", got)
	}
}

func TestMCPToolContractV1(t *testing.T) {
	before, err := os.ReadFile("testdata/tool-contract-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := readMCPToolContractFixture(before)
	if err != nil {
		t.Fatal(err)
	}
	live, err := observeMCPToolContract(NewServer(newMCPTestStore(t)).ListTools())
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyMCPToolContract(fixture, live, before); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile("testdata/tool-contract-v1.json"); err != nil || string(before) != string(after) {
		t.Fatalf("fixture changed: %v", err)
	}
}

func TestVerifyMCPToolContract(t *testing.T) {
	fixture := map[string]mcpToolSchema{"base": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"kept": {Types: []string{"string"}}}, Additional: false}}
	canonical := []byte(formatMCPToolContract(fixture))
	live := map[string]mcpToolSchema{"base": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"kept": {Types: []string{"string"}}, "added": {Types: []string{"boolean"}}}, Additional: true}, "new": {Types: []string{"string"}}}
	if err := verifyMCPToolContract(fixture, live, canonical); err != nil {
		t.Fatalf("compatible additions: %v", err)
	}
	incompatible := map[string]mcpToolSchema{"base": {Types: []string{"object"}, Properties: map[string]mcpToolSchema{"kept": {Types: []string{"number"}}}, Additional: true}}
	if err := verifyMCPToolContract(fixture, incompatible, canonical); err == nil || !strings.Contains(err.Error(), "type-narrowed") {
		t.Fatalf("incompatible live change: %v", err)
	}
	if err := verifyMCPToolContract(fixture, fixture, []byte("not canonical")); err == nil || !strings.Contains(err.Error(), "fixture is not byte-canonical fixture formatter output") {
		t.Fatalf("noncanonical fixture: %v", err)
	}
}

func verifyMCPToolContract(fixture, live map[string]mcpToolSchema, fixtureBytes []byte) error {
	if err := compareMCPToolContract(fixture, live); err != nil {
		return err
	}
	if string(fixtureBytes) != formatMCPToolContract(fixture) {
		return fmt.Errorf("fixture is not byte-canonical fixture formatter output")
	}
	return nil
}

func formatMCPToolContract(tools map[string]mcpToolSchema) string {
	var b strings.Builder
	b.WriteString("{\n  \"version\": \"engram.mcp-tool-contract/v1\",\n  \"tools\": ")
	formatMCPToolSchemas(&b, tools, 2)
	b.WriteString("\n}\n")
	return b.String()
}

func formatMCPToolSchemas(b *strings.Builder, schemas map[string]mcpToolSchema, indent int) {
	b.WriteString("{\n")
	keys := sortedKeys(schemas)
	for i, name := range keys {
		writeIndent(b, indent+2)
		writeJSONString(b, name)
		b.WriteString(": ")
		formatMCPToolSchema(b, schemas[name], indent+2)
		if i+1 < len(keys) {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	writeIndent(b, indent)
	b.WriteByte('}')
}

func formatMCPToolSchema(b *strings.Builder, schema mcpToolSchema, indent int) {
	if len(schema.Properties) == 0 && schema.Items == nil {
		b.WriteString(`{"type":`)
		writeJSONStringSlice(b, schema.Types)
		if len(schema.Enum) > 0 {
			b.WriteString(`,"enum":[`)
			for i, value := range uniqueStrings(append([]string(nil), schema.Enum...)) {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(value)
			}
			b.WriteByte(']')
		}
		b.WriteString(`,"additionalProperties":`)
		b.WriteString(fmt.Sprint(schema.Additional))
		b.WriteByte('}')
		return
	}
	b.WriteString("{\n")
	field := func(comma bool, name string, value func()) {
		if comma {
			b.WriteString(",\n")
		}
		writeIndent(b, indent+2)
		writeJSONString(b, name)
		b.WriteString(": ")
		value()
	}
	field(false, "type", func() { writeJSONStringSlice(b, schema.Types) })
	if len(schema.Properties) > 0 {
		field(true, "properties", func() { formatMCPToolSchemas(b, schema.Properties, indent+2) })
	}
	if len(schema.Required) > 0 {
		field(true, "required", func() { writeJSONStringSlice(b, schema.Required) })
	}
	if schema.Items != nil {
		field(true, "items", func() { formatMCPToolSchema(b, *schema.Items, indent+2) })
	}
	if len(schema.Enum) > 0 {
		field(true, "enum", func() {
			b.WriteByte('[')
			for i, value := range uniqueStrings(append([]string(nil), schema.Enum...)) {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(value)
			}
			b.WriteByte(']')
		})
	}
	field(true, "additionalProperties", func() { b.WriteString(fmt.Sprint(schema.Additional)) })
	b.WriteByte('}')
}

func writeIndent(b *strings.Builder, indent int) { b.WriteString(strings.Repeat(" ", indent)) }
func writeJSONString(b *strings.Builder, value string) {
	encoded, _ := json.Marshal(value)
	b.Write(encoded)
}
func writeJSONStringSlice(b *strings.Builder, values []string) {
	encoded, _ := json.Marshal(uniqueStrings(append([]string(nil), values...)))
	b.Write(encoded)
}

func readMCPToolContractFixture(raw []byte) (map[string]mcpToolSchema, error) {
	value, err := decodeMCPToolSchema(raw, "/")
	if err != nil {
		return nil, fmt.Errorf("malformed-fixture: %w", err)
	}
	envelope, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("/: malformed-fixture: envelope must be an object")
	}
	for key := range envelope {
		if key != "version" && key != "tools" {
			return nil, fmt.Errorf("/%s: malformed-fixture: unknown envelope field", key)
		}
	}
	version, _ := envelope["version"].(string)
	if version != "engram.mcp-tool-contract/v1" {
		return nil, fmt.Errorf("/version: unsupported-version: %q", version)
	}
	tools, ok := envelope["tools"].(map[string]any)
	if !ok || len(tools) == 0 {
		return nil, fmt.Errorf("/tools: malformed-fixture: tools must be non-empty")
	}
	out := make(map[string]mcpToolSchema, len(tools))
	for _, name := range sortedKeys(tools) {
		schema, err := normalizeMCPToolSchema(tools[name], jsonPointer("/tools", name))
		if err != nil {
			return nil, fmt.Errorf("malformed-fixture: %w", err)
		}
		out[name] = schema
	}
	return out, nil
}

type mcpToolContractDrift struct{ path, code, v1, live string }

func compareMCPToolContract(v1, live map[string]mcpToolSchema) error {
	var drifts []mcpToolContractDrift
	for _, name := range sortedKeys(v1) {
		path := jsonPointer("/tools", name)
		observed, ok := live[name]
		if !ok {
			drifts = append(drifts, mcpToolContractDrift{path, "missing-tool", schemaFact(v1[name]), "<missing>"})
			continue
		}
		compareMCPToolSchema(&drifts, path, v1[name], observed)
	}
	if len(drifts) == 0 {
		return nil
	}
	sort.Slice(drifts, func(i, j int) bool {
		return drifts[i].path < drifts[j].path || drifts[i].path == drifts[j].path && drifts[i].code < drifts[j].code
	})
	var lines []string
	for _, drift := range drifts {
		lines = append(lines, fmt.Sprintf("%s: %s: v1=%s live=%s", drift.path, drift.code, drift.v1, drift.live))
	}
	return fmt.Errorf("MCP tool contract drift\n%s", strings.Join(lines, "\n"))
}

func compareMCPToolSchema(drifts *[]mcpToolContractDrift, path string, v1, live mcpToolSchema) {
	drift := func(code string) {
		*drifts = append(*drifts, mcpToolContractDrift{path, code, schemaFact(v1), schemaFact(live)})
	}
	for _, typ := range v1.Types {
		if !slices.Contains(live.Types, typ) && !(typ == "integer" && slices.Contains(live.Types, "number")) {
			drift("type-narrowed")
			break
		}
	}
	if hasExtraType(v1.Types, live.Types) && (!simplePrimitive(v1) || !simplePrimitive(live)) {
		drift("unproven-type-drift")
	}
	if v1.Additional && !live.Additional {
		drift("additional-properties-narrowed")
	}
	if len(v1.Enum) == 0 && len(live.Enum) > 0 {
		drift("enum-added")
	} else if len(v1.Enum) > 0 && len(live.Enum) > 0 {
		for _, value := range v1.Enum {
			if !containsJSONInstance(live.Enum, value) {
				drift("enum-narrowed")
				break
			}
		}
	}
	if v1.Items == nil && live.Items != nil {
		drift("shape-drift")
	} else if v1.Items != nil && live.Items != nil {
		compareMCPToolSchema(drifts, jsonPointer(path, "items"), *v1.Items, *live.Items)
	}
	for _, name := range sortedKeys(v1.Properties) {
		child, ok := live.Properties[name]
		if !ok {
			*drifts = append(*drifts, mcpToolContractDrift{jsonPointer(jsonPointer(path, "properties"), name), "missing-property", schemaFact(v1.Properties[name]), "<missing>"})
			continue
		}
		compareMCPToolSchema(drifts, jsonPointer(jsonPointer(path, "properties"), name), v1.Properties[name], child)
	}
	for _, name := range live.Required {
		if !slices.Contains(v1.Required, name) {
			if _, ok := v1.Properties[name]; ok {
				driftAt(drifts, jsonPointer(jsonPointer(path, "properties"), name), "requiredness-narrowed", v1.Properties[name], live.Properties[name])
			} else {
				driftAt(drifts, jsonPointer(jsonPointer(path, "properties"), name), "new-required-property", mcpToolSchema{}, live.Properties[name])
			}
		}
	}
	for _, name := range sortedKeys(live.Properties) {
		if _, exists := v1.Properties[name]; !exists && !slices.Contains(live.Required, name) {
			if v1.Additional {
				driftAt(drifts, jsonPointer(jsonPointer(path, "properties"), name), "additional-properties-narrowed", mcpToolSchema{}, live.Properties[name])
			} else if !simplePrimitive(live.Properties[name]) {
				driftAt(drifts, jsonPointer(jsonPointer(path, "properties"), name), "unproven-optional-addition", mcpToolSchema{}, live.Properties[name])
			}
		}
	}
}

func containsJSONInstance(values []string, target string) bool {
	targetValue, ok := decodeJSONInstance(target)
	if !ok {
		return false
	}
	for _, value := range values {
		candidate, ok := decodeJSONInstance(value)
		if ok && equalJSONInstances(targetValue, candidate) {
			return true
		}
	}
	return false
}

func decodeJSONInstance(encoded string) (any, bool) {
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var extra any
	return value, decoder.Decode(&extra) == io.EOF
}

func equalJSONInstances(left, right any) bool {
	switch left := left.(type) {
	case nil:
		return right == nil
	case bool:
		right, ok := right.(bool)
		return ok && left == right
	case string:
		right, ok := right.(string)
		return ok && left == right
	case json.Number:
		right, ok := right.(json.Number)
		return ok && equalJSONNumbers(left, right)
	case []any:
		right, ok := right.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if !equalJSONInstances(left[i], right[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		right, ok := right.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, leftValue := range left {
			rightValue, ok := right[key]
			if !ok || !equalJSONInstances(leftValue, rightValue) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func equalJSONNumbers(left, right json.Number) bool {
	var leftValue, rightValue big.Rat
	if _, ok := leftValue.SetString(string(left)); !ok {
		return false
	}
	if _, ok := rightValue.SetString(string(right)); !ok {
		return false
	}
	return leftValue.Cmp(&rightValue) == 0
}

func hasExtraType(v1, live []string) bool {
	for _, typ := range live {
		if !slices.Contains(v1, typ) && !(typ == "number" && slices.Contains(v1, "integer")) {
			return true
		}
	}
	return false
}
func simplePrimitive(schema mcpToolSchema) bool {
	if len(schema.Types) == 0 || len(schema.Properties) > 0 || schema.Items != nil || len(schema.Enum) > 0 {
		return false
	}
	for _, typ := range schema.Types {
		if !slices.Contains([]string{"string", "number", "integer", "boolean"}, typ) {
			return false
		}
	}
	return true
}
func driftAt(drifts *[]mcpToolContractDrift, path, code string, v1, live mcpToolSchema) {
	*drifts = append(*drifts, mcpToolContractDrift{path, code, schemaFact(v1), schemaFact(live)})
}
func schemaFact(schema mcpToolSchema) string {
	var b strings.Builder
	formatMCPToolSchema(&b, schema, 0)
	return b.String()
}
