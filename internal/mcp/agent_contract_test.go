package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func readAgentContract(t *testing.T) (map[string]any, []byte) {
	t.Helper()
	raw, err := AgentContractJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document, raw
}

func TestAgentContractArtifactCurrent(t *testing.T) {
	_, want := readAgentContract(t)
	got, err := os.ReadFile("../../docs/contracts/agent-contract-v1.json")
	if err != nil {
		t.Fatalf("read generated agent contract: %v; run go generate ./internal/mcp", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("generated agent contract is stale; run go generate ./internal/mcp")
	}
}

func TestAgentContractInventoryAndMCPParity(t *testing.T) {
	document, _ := readAgentContract(t)
	if document["contract_id"] != "engram.agent-contract/v1" || document["revision"] != json.Number("1") || document["status"] != "mcp-foundation" {
		t.Fatalf("unexpected identity or parity status: %#v", document)
	}
	operations := document["operations"].(map[string]any)
	want := []string{"mem_capture_passive", "mem_compare", "mem_consolidate", "mem_context", "mem_current_project", "mem_doctor", "mem_get_observation", "mem_judge", "mem_list_projects", "mem_pin", "mem_review", "mem_save", "mem_save_prompt", "mem_search", "mem_session_end", "mem_session_start", "mem_session_summary", "mem_suggest_topic_key", "mem_unpin", "mem_update"}
	var got []string
	for name := range operations {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("agent inventory = %v, want %v", got, want)
	}
	registered := NewServerWithTools(newMCPTestStore(t), ProfileAgent).ListTools()
	if len(registered) != len(operations) {
		t.Fatalf("registered %d tools, contract has %d", len(registered), len(operations))
	}
	for name, tool := range registered {
		op := operations[name].(map[string]any)
		if op["description"] != tool.Tool.Description {
			t.Errorf("%s description drift", name)
		}
		wantSchema, err := json.Marshal(tool.Tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema any
		decoder := json.NewDecoder(bytes.NewReader(wantSchema))
		decoder.UseNumber()
		if err := decoder.Decode(&schema); err != nil {
			t.Fatal(err)
		}
		gotSchema, _ := json.Marshal(op["input_schema"])
		normalizedSchema, _ := json.Marshal(schema)
		if !bytes.Equal(gotSchema, normalizedSchema) {
			t.Errorf("%s input schema drift", name)
		}
		registeredJSON, err := json.Marshal(tool.Tool)
		if err != nil {
			t.Fatal(err)
		}
		var definition map[string]any
		if err := json.Unmarshal(registeredJSON, &definition); err != nil {
			t.Fatal(err)
		}
		wantAnnotations, _ := json.Marshal(definition["annotations"])
		gotAnnotations, _ := json.Marshal(op["annotations"])
		if !bytes.Equal(gotAnnotations, wantAnnotations) {
			t.Errorf("%s annotation drift", name)
		}
		rules := op["semantics"].(map[string]any)
		for _, field := range []string{"effect", "risk", "project_policy", "session_policy", "error_effects"} {
			if value, ok := rules[field].(string); !ok || value == "" {
				t.Errorf("%s missing semantic field %s", name, field)
			}
		}
		if len(rules["guarantees"].([]any)) == 0 {
			t.Errorf("%s has no declared guarantees", name)
		}
	}
	// Exporting the agent profile must not narrow the existing all-tools server.
	all := NewServer(newMCPTestStore(t)).ListTools()
	if len(all) != 24 || ResolveTools("") != nil || ResolveTools("all") != nil {
		t.Fatalf("legacy all-tools inventory changed: %d tools", len(all))
	}
	for name := range ProfileAdmin {
		if _, ok := all[name]; !ok {
			t.Errorf("legacy admin tool %s disappeared", name)
		}
		if _, ok := operations[name]; ok {
			t.Errorf("admin tool %s exposed in agent contract", name)
		}
	}
}

func TestAgentContractConsolidationPreservesUpstreamRegistration(t *testing.T) {
	document, _ := readAgentContract(t)
	operation := document["operations"].(map[string]any)["mem_consolidate"].(map[string]any)
	schema := operation["input_schema"].(map[string]any)
	required := schema["required"].([]any)
	for _, field := range []string{"ids_to_delete", "new_title", "new_content", "expected_project"} {
		if !slices.Contains(required, any(field)) {
			t.Errorf("consolidation omitted upstream required field %s", field)
		}
	}
	annotations := operation["annotations"].(map[string]any)
	if annotations["destructiveHint"] != true || annotations["readOnlyHint"] != false || annotations["idempotentHint"] != false {
		t.Fatalf("consolidation safety annotations drifted: %#v", annotations)
	}
	semantics := operation["semantics"].(map[string]any)
	wantDifference := "mem_consolidate is Go/MCP-only; Pi has no recipe or dispatch for this operation"
	differences := semantics["pi_differences"].([]any)
	if !slices.Contains(differences, any(wantDifference)) {
		t.Fatalf("consolidation silently claims Pi support: %#v", differences)
	}
}

func TestAgentContractDeterministicDigestAndFreshSnapshots(t *testing.T) {
	document, raw := readAgentContract(t)
	_, second := readAgentContract(t)
	if !bytes.Equal(raw, second) || !bytes.HasSuffix(raw, []byte("\n")) {
		t.Fatal("contract encoding is not deterministic newline-terminated JSON")
	}
	digest, ok := document["digest"].(string)
	if !ok || len(digest) != 64 {
		t.Fatalf("invalid SHA-256 digest: %v", document["digest"])
	}
	delete(document, "digest")
	canonical, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest does not cover the canonical unsigned document: %s", digest)
	}
	// A caller cannot mutate a subsequent snapshot or the source registrations.
	document["operations"].(map[string]any)["mem_save"].(map[string]any)["description"] = "changed by consumer"
	_, third := readAgentContract(t)
	if !bytes.Equal(raw, third) {
		t.Fatal("consumer mutation leaked into the contract authority")
	}
}

func TestAgentContractDeclaresProjectionGapsAndTargetReviewRule(t *testing.T) {
	document, _ := readAgentContract(t)
	operations := document["operations"].(map[string]any)
	for _, name := range []string{"mem_save", "mem_update", "mem_get_observation", "mem_search", "mem_review", "mem_suggest_topic_key", "mem_session_end"} {
		rules := operations[name].(map[string]any)["semantics"].(map[string]any)
		if len(rules["pi_differences"].([]any)) == 0 {
			t.Errorf("%s silently claims parity despite an observed difference", name)
		}
	}
	review := operations["mem_review"].(map[string]any)["semantics"].(map[string]any)
	want := "mark_reviewed: explicit project confines the mutation; omission requires the current resolved project; never mark globally"
	if review["target_rule"] != want {
		t.Fatalf("approved review target rule = %v", review["target_rule"])
	}
	for _, name := range []string{"mem_save", "mem_save_prompt", "mem_session_summary", "mem_capture_passive"} {
		rules := operations[name].(map[string]any)["semantics"].(map[string]any)
		fields := rules["pi_runtime_fields"].(map[string]any)
		want := "acknowledged effective host session (or explicit-target satellite)"
		if name == "mem_capture_passive" {
			want = "acknowledged effective host session; passive capture never selects a foreign satellite"
		}
		if fields["session_id"] != want {
			t.Errorf("%s has no explicit Pi session projection", name)
		}
	}
}
