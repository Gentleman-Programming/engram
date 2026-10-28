package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"
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

func TestAgentContractRejectsInconsistentSemanticInventory(t *testing.T) {
	for _, tc := range []struct {
		name       string
		replaceKey bool
		wantError  string
	}{
		{"missing entry", false, "agent contract: profile, registration, and semantic metadata inventories differ"},
		{"unknown entry with matching count", true, "agent contract: missing or inconsistent declaration for mem_pin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// These tests mutate package metadata and must remain non-parallel.
			before, err := AgentContractJSON()
			if err != nil {
				t.Fatal(err)
			}
			saved, ok := agentContractSemantics["mem_pin"]
			if !ok {
				t.Fatal("missing baseline mem_pin semantics")
			}
			const unknown = "unknown_test_operation"
			if _, exists := agentContractSemantics[unknown]; exists {
				t.Fatal("test operation already declared")
			}
			t.Cleanup(func() {
				delete(agentContractSemantics, unknown)
				agentContractSemantics["mem_pin"] = saved
				after, err := AgentContractJSON()
				if err != nil || !bytes.Equal(after, before) {
					t.Errorf("cleanup changed contract metadata or digest: %v", err)
				}
			})
			delete(agentContractSemantics, "mem_pin")
			if tc.replaceKey {
				agentContractSemantics[unknown] = saved
			}
			out, err := AgentContractJSON()
			if err == nil || err.Error() != tc.wantError || out != nil {
				t.Fatalf("inventory refusal: output=%q error=%v, want nil output and %q", out, err, tc.wantError)
			}
		})
	}
}

func TestAgentContractInventoryAndMCPParity(t *testing.T) {
	document, _ := readAgentContract(t)
	if document["contract_id"] != "engram.agent-contract/v1" || document["revision"] != json.Number("1") || document["status"] != "mcp-foundation" {
		t.Fatalf("unexpected identity or parity status: %#v", document)
	}
	operations := document["operations"].(map[string]any)
	want := []string{"mem_capture_passive", "mem_compare", "mem_consolidate", "mem_context", "mem_current_project", "mem_doctor", "mem_get_observation", "mem_judge", "mem_judge_many", "mem_list_projects", "mem_pin", "mem_review", "mem_save", "mem_save_prompt", "mem_search", "mem_search_prompts", "mem_session_end", "mem_session_finalize", "mem_session_start", "mem_session_summary", "mem_suggest_topic_key", "mem_unpin", "mem_update"}
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
	if len(all) != 27 || ResolveTools("") != nil || ResolveTools("all") != nil {
		t.Fatalf("legacy all-tools inventory changed: %d tools", len(all))
	}
	wantShared := []string{"mem_judge_many", "mem_session_finalize"}
	var shared []string
	for name := range ProfileAdmin {
		if _, ok := all[name]; !ok {
			t.Errorf("legacy admin tool %s disappeared", name)
		}
		_, exposed := operations[name]
		if slices.Contains(wantShared, name) {
			if !exposed || !ProfileAgent[name] {
				t.Errorf("shared tool %s missing from agent contract or profile", name)
			}
		} else if exposed || ProfileAgent[name] {
			t.Errorf("admin-only tool %s exposed in agent contract or profile", name)
		}
		if ProfileAgent[name] {
			shared = append(shared, name)
		}
	}
	slices.Sort(shared)
	if !slices.Equal(shared, wantShared) {
		t.Fatalf("shared agent/admin inventory = %v, want %v", shared, wantShared)
	}
}

func TestAgentContractSharedOperationsMetadataOnly(t *testing.T) {
	// Metadata registration needs neither a store nor a queue and must not
	// invoke the write handlers while exporting the contract.
	srv := server.NewMCPServer("engram-contract-test", "metadata")
	registerToolsWithQueue(srv, nil, MCPConfig{}, ProfileAgent, nil, nil)
	registered := srv.ListTools()
	if len(registered) != 23 {
		t.Fatalf("metadata-only agent inventory = %d, want 23", len(registered))
	}
	document, _ := readAgentContract(t)
	operations := document["operations"].(map[string]any)
	for _, tc := range []struct {
		name       string
		required   []string
		idempotent bool
	}{
		{"mem_judge_many", []string{"judgments"}, false},
		{"mem_session_finalize", []string{"session_id", "summary"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !ProfileAgent[tc.name] || !ProfileAdmin[tc.name] {
				t.Fatal("operation must belong to both agent and admin profiles")
			}
			if _, ok := registered[tc.name]; !ok {
				t.Fatal("operation missing from metadata-only Go registration")
			}
			operation, ok := operations[tc.name].(map[string]any)
			if !ok {
				t.Fatal("operation missing from Go agent contract")
			}
			schema := operation["input_schema"].(map[string]any)
			required := schema["required"].([]any)
			for _, field := range tc.required {
				if !slices.Contains(required, any(field)) {
					t.Errorf("missing upstream required field %s", field)
				}
			}
			annotations := operation["annotations"].(map[string]any)
			if annotations["readOnlyHint"] != false || annotations["destructiveHint"] != false || annotations["idempotentHint"] != tc.idempotent || annotations["openWorldHint"] != false {
				t.Fatalf("upstream safety annotations drifted: %#v", annotations)
			}
			rules := operation["semantics"].(map[string]any)
			if rules["risk"] != "persistent-write" {
				t.Errorf("write operation risk = %v", rules["risk"])
			}
			if len(rules["pi_differences"].([]any)) == 0 {
				t.Error("Go-only operation silently claims Pi parity")
			}
		})
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
	differences := semantics["pi_differences"].([]any)
	for _, fact := range []string{
		"Pi projects HTTP status, flat errors and optional replacement plans into native error metadata",
		"Go owns the opaque fingerprint and confirmation validation",
		"Pi does not auto-confirm replacement previews or replay uncertain POSTs",
	} {
		if !slices.ContainsFunc(differences, func(value any) bool {
			difference, ok := value.(string)
			return ok && strings.Contains(difference, fact)
		}) {
			t.Errorf("consolidation projection missing %q: %#v", fact, differences)
		}
	}
}

func TestAgentContractCanonicalEncodingVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/agent-contract-canonical-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Format  string `json:"format"`
		Vectors []struct {
			Name      string `json:"name"`
			InputJSON string `json:"input_json"`
			Canonical string `json:"canonical_utf8"`
			SHA256    string `json:"sha256"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Format != "engram.agent-contract/go-json-vectors/v1" || len(fixture.Vectors) != 2 {
		t.Fatal("unexpected canonical encoding vector inventory")
	}
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			var document agentContractDocument
			if err := json.Unmarshal([]byte(vector.InputJSON), &document); err != nil {
				t.Fatal(err)
			}
			// AgentContractJSON hashes the unsigned document, not its digest field.
			document.Digest = ""
			got, err := canonicalContractJSON(document)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != vector.Canonical {
				t.Fatalf("Go canonical bytes differ from vector: got %q, want %q", got, vector.Canonical)
			}
			sum := sha256.Sum256(got)
			if hex.EncodeToString(sum[:]) != vector.SHA256 {
				t.Fatalf("canonical vector SHA-256 mismatch: %x", sum)
			}
		})
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
