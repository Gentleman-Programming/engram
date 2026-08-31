package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	decoder := json.NewDecoder(bytes.NewReader(raw))
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
		if len(bytes.TrimSpace(raw)) == 0 {
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
