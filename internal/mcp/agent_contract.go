package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/server"
)

//go:generate go run ../../tools/agent-contract -output ../../docs/contracts/agent-contract-v1.json

// AgentContractJSON exports the current Go-owned agent contract without opening
// a store, invoking handlers, or starting a write worker. The foundation records
// MCP behavior and explicit Pi differences; it does not assert runtime parity.
func AgentContractJSON() ([]byte, error) {
	srv := server.NewMCPServer("engram-contract", "metadata")
	registerToolsWithQueue(srv, nil, MCPConfig{}, ProfileAgent, nil, nil)
	tools := srv.ListTools()
	if len(tools) != len(ProfileAgent) || len(tools) != len(agentContractSemantics) {
		return nil, fmt.Errorf("agent contract: profile, registration, and semantic metadata inventories differ")
	}
	operations := make(map[string]agentContractOperation, len(tools))
	for name, tool := range tools {
		rules, ok := agentContractSemantics[name]
		if !ok || !ProfileAgent[name] || tool.Tool.Name != name {
			return nil, fmt.Errorf("agent contract: missing or inconsistent declaration for %s", name)
		}
		// Marshal the registered definition, not a second manually maintained
		// schema. This also respects RawInputSchema if a tool later uses it.
		raw, err := json.Marshal(tool.Tool)
		if err != nil {
			return nil, fmt.Errorf("agent contract: encode %s: %w", name, err)
		}
		var definition map[string]json.RawMessage
		if err := json.Unmarshal(raw, &definition); err != nil {
			return nil, err
		}
		operations[name] = agentContractOperation{
			Description: tool.Tool.Description,
			InputSchema: definition["inputSchema"],
			Annotations: definition["annotations"],
			Semantics:   rules,
		}
	}
	document := agentContractDocument{
		ContractID: "engram.agent-contract/v1",
		Revision:   1,
		Status:     "mcp-foundation",
		Profile:    "agent",
		Operations: operations,
	}
	unsigned, err := canonicalContractJSON(document)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(unsigned)
	document.Digest = hex.EncodeToString(sum[:])
	canonical, err := canonicalContractJSON(document)
	if err != nil {
		return nil, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, canonical, "", "  "); err != nil {
		return nil, err
	}
	pretty.WriteByte('\n')
	return pretty.Bytes(), nil
}

type agentContractDocument struct {
	ContractID string                            `json:"contract_id"`
	Revision   int                               `json:"revision"`
	Status     string                            `json:"status"`
	Profile    string                            `json:"profile"`
	Operations map[string]agentContractOperation `json:"operations"`
	Digest     string                            `json:"digest,omitempty"`
}

type agentContractOperation struct {
	Description string                  `json:"description"`
	InputSchema json.RawMessage         `json:"input_schema"`
	Annotations json.RawMessage         `json:"annotations"`
	Semantics   agentOperationSemantics `json:"semantics"`
}

type agentOperationSemantics struct {
	Effect          string            `json:"effect"`
	Risk            string            `json:"risk"`
	ProjectPolicy   string            `json:"project_policy"`
	SessionPolicy   string            `json:"session_policy"`
	Guarantees      []string          `json:"guarantees"`
	ErrorEffects    string            `json:"error_effects"`
	PiRuntimeFields map[string]string `json:"pi_runtime_fields,omitempty"`
	PiDifferences   []string          `json:"pi_differences,omitempty"`
	TargetRule      string            `json:"target_rule,omitempty"`
}

// Canonicalization uses encoding/json with sorted object keys, preserved array
// order, and json.Number (no float64 conversion). The digest covers the complete
// unsigned document, including descriptions and semantics, without whitespace.
func canonicalContractJSON(document agentContractDocument) ([]byte, error) {
	raw, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

const (
	readProjectPolicy     = "explicit known project, then process override or detected current project; resolution errors stay explicit"
	writeProjectPolicy    = "validated explicit project, existing session association, process override, then repository/directory detection; normalize the resolved owner"
	implicitSessionPolicy = "explicit MCP session_id or Go-selected fallback session; session/project ownership remains Go-owned"
	piEffectiveSession    = "acknowledged effective host session (or explicit-target satellite)"
	mutationErrorEffects  = "validation guarantees are operation-specific; a later error or lost acknowledgement must not be interpreted as proof of rollback"
)

// Only semantics are declared here. Inventory, descriptions, schemas, and
// annotations are taken from ProfileAgent and the existing Go registrations.
// Pi differences describe observed adapters, not compatibility permissions.
var agentContractSemantics = map[string]agentOperationSemantics{
	"mem_judge_many": {
		Effect: "write-relations", Risk: "persistent-write",
		ProjectPolicy: "Go resolves and validates each identified judgment; not a current-project lookup or batch destination override",
		SessionPolicy: "optional shared session_id is provenance for every item, not proof of host authority; item-level session_id is not used",
		Guarantees:    []string{"require an array of 1..32 caller-authored judgments before processing any item", "each item requires judgment_id and relation; supplied reason/evidence must be strings and confidence must be finite and between 0 and 1", "process items in submitted order and return ordered outcomes with index, judgment_id, status and relation or error", "item errors do not stop later items", "retries converge verdicts but refresh existing sync mutations; not exactly-once"},
		ErrorEffects:  "invalid batch shape or shared session_id type is rejected before item writes; item failures do not roll back successful items; a later error or lost acknowledgement does not prove rollback",
		PiDifferences: []string{"Pi sends one HTTP POST and preserves ordered partial outcomes in its native envelope; shared provenance is caller-authored and omission does not inject the host ID; no client loop or uncertain-POST replay"},
	},
	"mem_session_finalize": {
		Effect: "write-summary-and-end-session", Risk: "persistent-write",
		ProjectPolicy: "optional explicit project is validated through summary project resolution and must match the exact existing session owner; ambiguous-project recovery cannot create a session",
		SessionPolicy: "required session_id selects an existing exact identity; never trimmed, implicitly created, or mapped from a host continuation",
		Guarantees:    []string{"nonempty session_id and summary strings are required", "Go atomically saves a session_summary observation, ends the session, queues normal sync mutations, and records a local retry journal", "preserve the legacy free-text session summary", "successful local retries return the original summary_id and summary_sync_id without writes even if valid retry text differs", "sessions ended separately or imported already ended without the local finalization journal reject", "the retry journal is local-only and is not synced or exported; local atomicity does not guarantee remote visibility"},
		ErrorEffects:  "validation and ownership refusals do not finalize the session; finalization transaction failures roll back its local writes; a lost acknowledgement is not proof of rollback and retry identity depends on the local journal",
		PiDifferences: []string{"Pi requires confirmed current host/effective-session ownership, injects the exact session ID and explicit owner into HTTP, and accepts no model session identity; unknown acknowledgement pins the identity and blocks lifecycle reopening, with explicit same-host retry only and no automatic POST replay or legacy-end substitution"},
	},
	"mem_consolidate": {
		Effect: "consolidate-observations", Risk: "persistent-write",
		ProjectPolicy: "expected_project explicitly asserts the existing canonical owner of every source; it is not a destination override",
		SessionPolicy: "source session and scope are inherited; model arguments cannot override them",
		Guarantees:    []string{"preview is the default and does not write", "confirmation requires the exact preview arguments and opaque fingerprint", "drift refuses without writes and returns a replacement preview that must not be automatically confirmed", "at least two distinct live observation IDs must have the asserted owner", "consolidation is locally atomic, not a guarantee of remote visibility"},
		ErrorEffects:  "validation and drift refusals do not mutate local observations; a lost or uncertain write result must not be automatically retried or treated as rollback",
		PiDifferences: []string{"Pi projects HTTP status, flat errors and optional replacement plans into native error metadata; Go owns the opaque fingerprint and confirmation validation; Pi does not auto-confirm replacement previews or replay uncertain POSTs"},
	},
	"mem_search": {
		Effect: "read", Risk: "read-only", ProjectPolicy: readProjectPolicy, SessionPolicy: "no session attribution required; MCP activity is tracked separately",
		Guarantees:    []string{"all_projects=true ignores project selection", "match_mode is all by default or any; other values are rejected", "personal scope without explicit project searches across projects", "default limit is 10"},
		ErrorEffects:  "no observation mutation; invalid MCP match_mode/response_format is rejected before project resolution",
		PiDifferences: []string{"Pi sends its resolved project to HTTP, while MCP clears the project filter for implicit personal scope", "MCP uses bounded previews by default; HTTP/Pi uses full search result rows unless response_format=compact"},
	},
	"mem_context": {
		Effect: "read", Risk: "read-only", ProjectPolicy: readProjectPolicy, SessionPolicy: "no session attribution required",
		Guarantees:    []string{"personal scope without explicit project clears the observation project filter", "MCP bounds its complete context result, including its suffix", "default byte budget is 16384; ceiling is 65536; preserve UTF-8"},
		ErrorEffects:  "no observation mutation",
		PiDifferences: []string{"MCP reserves bytes for activity/stats suffix and caps pinned rows at 20; HTTP/Pi has different presentation", "Pi sends its resolved project rather than the MCP implicit-personal filter"},
	},
	"mem_get_observation": {
		Effect: "read", Risk: "read-only", ProjectPolicy: "ID-based lookup is not project-filtered; MCP resolves response context and can fall back to the stored owner on ambiguous cwd", SessionPolicy: "id is an observation ID, not a session ID",
		Guarantees:    []string{"missing observations produce an error", "MCP and HTTP/Pi optional history is bounded to 50 prior snapshots per page, oldest first, with a truthful total version_count and continuation metadata only when older versions remain; history_cursor is a validated non-negative integer and is ignored unless include_history=true"},
		ErrorEffects:  "no observation mutation",
		PiDifferences: []string{"Pi omits the MCP project input; HTTP/Pi returns current observation fields while MCP renders current content in its response context"},
	},
	"mem_current_project": {
		Effect: "detect", Risk: "read-only", ProjectPolicy: "process override or Go detection of directory/cwd; return detection metadata instead of inventing a successful owner", SessionPolicy: "no session attribution required",
		Guarantees: []string{"MCP reports ambiguous detection as a successful informational envelope with error_hint"}, ErrorEffects: "detection metadata is not write authorization",
		PiDifferences: []string{"Pi intentionally exposes canonical cwd only, not the MCP directory alias, to select host detector input rather than duplicate transport aliases; omitted or empty cwd uses host cwd, while whitespace is forwarded and HTTP trims blank input to server cwd", "Pi can use local configuration fallback when the HTTP route is unavailable"},
	},
	"mem_list_projects": {
		Effect: "read", Risk: "read-only", ProjectPolicy: "all projects; no implicit current-project filter", SessionPolicy: "no session attribution required",
		Guarantees: []string{"return projects and count", "an empty store is a successful empty array, not an error"}, ErrorEffects: "store lookup failure is an error, not an empty listing",
	},
	"mem_doctor": {
		Effect: "diagnose", Risk: "read-only", ProjectPolicy: "explicit project or read-only current-project inspection", SessionPolicy: "diagnosis does not register or repair a caller session",
		Guarantees: []string{"optional check selects one diagnostic; omission runs all", "a report with status=error is a tool error"}, ErrorEffects: "no caller registration or binding repair",
		PiRuntimeFields: map[string]string{"caller_binding": "observed host/runtime identity, effective mapping, and host API availability; not model authority"},
		PiDifferences:   []string{"Pi adds a separate Go caller-binding diagnostic and a safe unknown fallback on unsupported servers"},
	},
	"mem_suggest_topic_key": {
		Effect: "compute", Risk: "read-only", ProjectPolicy: "not applicable", SessionPolicy: "not applicable",
		Guarantees: []string{"require nonblank title or content", "Go owns the topic-key algorithm; no records are saved"}, ErrorEffects: "invalid input does not mutate records",
		PiDifferences: []string{"Pi forwards type/title/content to the Go HTTP topic-key endpoint and returns its result through the native envelope; Go owns the algorithm and empty-input rejection, with no local slug or fallback"},
	},
	"mem_save": {
		Effect: "write-observation", Risk: "persistent-write", ProjectPolicy: writeProjectPolicy, SessionPolicy: implicitSessionPolicy,
		Guarantees:   []string{"content or the legacy observation alias is required", "invalid title/content is rejected by MCP before implicit session creation", "type defaults to manual", "topic_key can update an existing observation instead of inserting a new one", "optional current-prompt capture is nonfatal", "MCP detects conflict candidates after save; detection failure does not invalidate the saved observation"},
		ErrorEffects: mutationErrorEffects, PiRuntimeFields: map[string]string{"session_id": piEffectiveSession, "current_prompt": "host-captured prompt matching the active project and acknowledged effective session"},
		PiDifferences: []string{"HTTP/Pi save acknowledgement lacks MCP conflict candidates and judgment metadata", "Pi does not expose observation alias or MCP ambiguous-project recovery parameters", "Pi adds cwd target selection and host registration/acknowledgement recovery"},
	},
	"mem_update": {
		Effect: "update-observation", Risk: "persistent-write", ProjectPolicy: "expected_project is a required caller-authored owner assertion; do not move the observation to another project", SessionPolicy: "id identifies an existing observation",
		Guarantees: []string{"only supplied fields change", "at least one update field is required", "owner validation is enforced in Go", "literal find/replace requires a pair and cannot be combined with content"}, ErrorEffects: mutationErrorEffects,
		PiDifferences: []string{"Pi forwards supplied find and replace through existing redaction to HTTP PATCH, preserving omission and empty strings; Go owns owner assertion, pairing, content conflict and mutation validation; HTTP results/errors use the native envelope with no client replay"},
	},
	"mem_save_prompt": {
		Effect: "write-prompt", Risk: "persistent-write", ProjectPolicy: "existing MCP session association takes precedence; ambiguous-project recovery is explicitly validated", SessionPolicy: implicitSessionPolicy,
		Guarantees: []string{"persist prompt attribution through Go", "MCP records the current prompt for later automatic capture"}, ErrorEffects: mutationErrorEffects,
		PiRuntimeFields: map[string]string{"session_id": piEffectiveSession}, PiDifferences: []string{"Pi supports explicit project/cwd destinations using isolated satellites; MCP prompt project precedence differs", "Pi returns prompt_id/status while MCP returns a text/project/truncation envelope"},
	},
	"mem_session_summary": {
		Effect: "write-observation", Risk: "persistent-write", ProjectPolicy: writeProjectPolicy, SessionPolicy: implicitSessionPolicy,
		Guarantees: []string{"nonblank content is required before MCP session creation", "save a session_summary observation; do not end the session"}, ErrorEffects: mutationErrorEffects,
		PiRuntimeFields: map[string]string{"session_id": piEffectiveSession}, PiDifferences: []string{"Pi uses the HTTP observation route with a different summary title and output envelope", "Pi adds cwd selection rather than MCP recovery parameters"},
	},
	"mem_capture_passive": {
		Effect: "write-observations", Risk: "persistent-write", ProjectPolicy: "existing session association or resolved current project; no arbitrary model-selected project", SessionPolicy: implicitSessionPolicy,
		Guarantees: []string{"Go parses learnings and reports extracted/saved/duplicate counts", "an explicit MCP session is not silently replaced with another identity"}, ErrorEffects: mutationErrorEffects,
		PiRuntimeFields: map[string]string{"session_id": "acknowledged effective host session; passive capture never selects a foreign satellite"},
		PiDifferences:   []string{"default source is mcp-passive for MCP and pi-tool for Pi", "Pi requires acknowledged host attribution and never sends implicit passive capture to a foreign satellite"},
	},
	"mem_session_start": {
		Effect: "write-session", Risk: "persistent-write", ProjectPolicy: "MCP detects from an explicit directory or current process/repository; no model project override", SessionPolicy: "id is an explicit session ID; an ended identity is not reopened by start",
		Guarantees: []string{"Go validates lifecycle and ownership", "session_already_ended is explicit and includes the refused session ID"}, ErrorEffects: mutationErrorEffects,
		PiDifferences: []string{"Pi passes its current resolved project even when an explicit directory is supplied; MCP detects that directory", "this explicit tool is distinct from Pi host-owned resume registration"},
	},
	"mem_session_end": {
		Effect: "end-session", Risk: "persistent-write", ProjectPolicy: "MCP resolves response context but ends the named session; invalid repository binding/configuration remains an error", SessionPolicy: "MCP id selects a session; Pi restricts it to the current host identity and maps it to the acknowledged effective session",
		Guarantees: []string{"Go ends the identified session with an optional summary", "Pi refuses foreign IDs before dispatching an end request"}, ErrorEffects: mutationErrorEffects,
		PiRuntimeFields: map[string]string{"effective_session_id": "confirmed host continuation mapping; cannot be chosen by model parameters"},
		PiDifferences:   []string{"Pi end is deliberately limited to its current host/effective session with local ownership and persistence evidence"},
	},
	"mem_review": {
		Effect: "read-or-mark-reviewed", Risk: "persistent-write", ProjectPolicy: "list: explicit known project or all projects; current MCP mark_reviewed ignores the project argument and uses current/process resolution", SessionPolicy: "observation_id (or id alias) identifies an observation",
		Guarantees: []string{"actions are list or mark_reviewed", "omitted-project list is global", "mark_reviewed is confined when a project is resolved"}, ErrorEffects: "invalid action/ID is rejected before mutation; reload can fail after marking reviewed",
		PiDifferences: []string{"Pi accepts an explicit project for mark_reviewed; the current MCP branch only uses project for list"},
		TargetRule:    "mark_reviewed: explicit project confines the mutation; omission requires the current resolved project; never mark globally",
	},
	"mem_pin": {
		Effect: "local-pin", Risk: "local-curation", ProjectPolicy: "ID-based operation; no current-project filter", SessionPolicy: "id identifies an observation",
		Guarantees: []string{"set the local pinned state to true through Go"}, ErrorEffects: "MCP can fail to reload after changing the pin; such an error is not a rollback",
	},
	"mem_unpin": {
		Effect: "local-unpin", Risk: "local-curation", ProjectPolicy: "ID-based operation; no current-project filter", SessionPolicy: "id identifies an observation",
		Guarantees: []string{"set the local pinned state to false through Go"}, ErrorEffects: "MCP can fail to reload after changing the pin; such an error is not a rollback",
	},
	"mem_judge": {
		Effect: "write-relation", Risk: "persistent-write", ProjectPolicy: "Go resolves and validates the identified judgment; not a current-project lookup", SessionPolicy: "optional session_id is provenance, not proof of host authority",
		Guarantees: []string{"judgment_id and relation are required", "supplied confidence must be between 0 and 1", "Go persists the verdict and returns the relation"}, ErrorEffects: mutationErrorEffects,
		PiDifferences: []string{"Pi defaults omitted provenance session_id to the host ID; it also accepts an explicitly supplied provenance ID"},
	},
	"mem_compare": {
		Effect: "write-relation", Risk: "persistent-write", ProjectPolicy: "Go resolves both observation IDs to durable sync IDs", SessionPolicy: "memory_id_a and memory_id_b are observation IDs; model is optional provenance",
		Guarantees: []string{"both observations, relation, reasoning, and confidence are required", "confidence must be between 0 and 1", "Go persists an externally judged verdict and returns its sync_id"}, ErrorEffects: mutationErrorEffects,
		PiDifferences: []string{"HTTP decoding requires integer IDs; MCP handler currently converts numeric IDs to int64"},
	},
}
