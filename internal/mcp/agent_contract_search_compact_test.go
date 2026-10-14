package mcp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAgentContractCompactSearchPiProjection(t *testing.T) {
	raw, err := AgentContractJSON()
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Operations map[string]struct {
			Semantics struct {
				PiDifferences []string `json:"pi_differences"`
			} `json:"semantics"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Pi sends its resolved project to HTTP, while MCP clears the project filter for implicit personal scope",
		"MCP uses bounded previews by default; HTTP/Pi uses full search result rows unless response_format=compact",
	}
	if got := document.Operations["mem_search"].Semantics.PiDifferences; !reflect.DeepEqual(got, want) {
		t.Fatalf("compact search projection metadata: got %q, want %q", got, want)
	}
}
