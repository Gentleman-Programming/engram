package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAgentContractObservationHistoryProjection(t *testing.T) {
	raw, err := AgentContractJSON()
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Operations map[string]struct {
			InputSchema struct {
				Properties map[string]map[string]any `json:"properties"`
				Required   []string                  `json:"required"`
			} `json:"input_schema"`
			Semantics struct {
				PiDifferences []string `json:"pi_differences"`
			} `json:"semantics"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	operation := document.Operations["mem_get_observation"]
	if !reflect.DeepEqual(operation.InputSchema.Required, []string{"id"}) {
		t.Fatalf("required inputs changed: %q", operation.InputSchema.Required)
	}
	for key, typ := range map[string]string{"id": "number", "include_history": "boolean", "history_cursor": "number", "project": "string"} {
		property := operation.InputSchema.Properties[key]
		if property["type"] != typ {
			t.Fatalf("%s schema: %#v", key, property)
		}
		if key == "include_history" || key == "history_cursor" {
			if _, ok := property["default"]; ok {
				t.Fatalf("%s acquired a schema default", key)
			}
		}
	}
	for _, difference := range operation.Semantics.PiDifferences {
		if strings.Contains(difference, "include_history") || strings.Contains(difference, "history_cursor") || strings.Contains(difference, "only the observation") {
			t.Fatalf("obsolete Pi history limitation: %q", difference)
		}
	}
	wantSearch := "Pi sends its resolved project to HTTP, while MCP clears the project filter for implicit personal scope"
	if !strings.Contains(strings.Join(document.Operations["mem_search"].Semantics.PiDifferences, "\n"), wantSearch) {
		t.Fatal("implicit-personal search caveat removed")
	}
	wantContext := "Pi sends its resolved project rather than the MCP implicit-personal filter"
	if !strings.Contains(strings.Join(document.Operations["mem_context"].Semantics.PiDifferences, "\n"), wantContext) {
		t.Fatal("implicit-personal context caveat removed")
	}
}
