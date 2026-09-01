package chunkcodec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func TestCanonicalizeForProjectPreservesMutationMetadataPayloadFields(t *testing.T) {
	raw := []byte(`{
		"mutations": [
			{
				"entity": "session",
				"entity_key": "sess-1",
				"op": "upsert",
				"project": "wrong",
				"payload": "{\"id\":\"sess-1\",\"project\":\"wrong\",\"directory\":\"/tmp/sess-1\",\"started_at\":\"2026-04-10T12:00:00Z\",\"ended_at\":\"2026-04-10T12:30:00Z\"}"
			},
			{
				"entity": "observation",
				"entity_key": "obs-1",
				"op": "upsert",
				"project": "wrong",
				"payload": "{\"sync_id\":\"obs-1\",\"session_id\":\"sess-1\",\"type\":\"note\",\"title\":\"metadata\",\"content\":\"keep fields\",\"scope\":\"project\",\"project\":\"wrong\",\"created_at\":\"2026-04-09T10:00:00Z\",\"updated_at\":\"2026-04-10T11:00:00Z\",\"last_seen_at\":\"2026-04-10T11:30:00Z\",\"revision_count\":9,\"duplicate_count\":4}"
			},
			{
				"entity": "prompt",
				"entity_key": "prompt-1",
				"op": "upsert",
				"project": "wrong",
				"payload": "{\"sync_id\":\"prompt-1\",\"session_id\":\"sess-1\",\"content\":\"prompt body\",\"project\":\"wrong\",\"created_at\":\"2026-04-08T09:00:00Z\"}"
			}
		]
	}`)

	normalized, err := CanonicalizeForProject(raw, "proj-a")
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}

	var chunk struct {
		Mutations []store.SyncMutation `json:"mutations"`
	}
	if err := json.Unmarshal(normalized, &chunk); err != nil {
		t.Fatalf("decode canonicalized chunk: %v", err)
	}
	if len(chunk.Mutations) != 3 {
		t.Fatalf("expected 3 mutations, got %d", len(chunk.Mutations))
	}

	assertPayloadField := func(index int, key string, want any) {
		t.Helper()
		var payload map[string]any
		if err := json.Unmarshal([]byte(chunk.Mutations[index].Payload), &payload); err != nil {
			t.Fatalf("decode payload[%d]: %v", index, err)
		}
		if payload[key] != want {
			t.Fatalf("mutation[%d] expected payload[%q]=%v, got %v", index, key, want, payload[key])
		}
		if payload["project"] != "proj-a" {
			t.Fatalf("mutation[%d] expected payload project rewritten to proj-a, got %v", index, payload["project"])
		}
	}

	assertPayloadField(0, "started_at", "2026-04-10T12:00:00Z")
	assertPayloadField(1, "created_at", "2026-04-09T10:00:00Z")
	assertPayloadField(1, "updated_at", "2026-04-10T11:00:00Z")
	assertPayloadField(1, "last_seen_at", "2026-04-10T11:30:00Z")
	assertPayloadField(1, "revision_count", float64(9))
	assertPayloadField(1, "duplicate_count", float64(4))
	assertPayloadField(2, "created_at", "2026-04-08T09:00:00Z")
}

func TestCanonicalizeForProjectAcceptsRelationUpsertMutation(t *testing.T) {
	raw := []byte(`{
		"mutations": [
			{
				"entity": "relation",
				"entity_key": "rel-1",
				"op": "upsert",
				"project": "wrong",
				"payload": "{\"sync_id\":\"rel-1\",\"source_id\":\"obs-a\",\"target_id\":\"obs-b\",\"relation\":\"conflicts_with\",\"reason\":\"different decisions\",\"judgment_status\":\"judged\",\"marked_by_actor\":\"agent-a\",\"marked_by_kind\":\"agent\",\"marked_by_model\":\"model-a\",\"project\":\"wrong\",\"created_at\":\"2026-05-04T01:00:00Z\",\"updated_at\":\"2026-05-04T01:01:00Z\"}"
			}
		]
	}`)

	normalized, err := CanonicalizeForProject(raw, "proj-a")
	if err != nil {
		t.Fatalf("canonicalize relation mutation: %v", err)
	}

	var chunk struct {
		Mutations []store.SyncMutation `json:"mutations"`
	}
	if err := json.Unmarshal(normalized, &chunk); err != nil {
		t.Fatalf("decode canonicalized chunk: %v", err)
	}
	if len(chunk.Mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(chunk.Mutations))
	}
	mutation := chunk.Mutations[0]
	if mutation.Entity != store.SyncEntityRelation || mutation.Op != store.SyncOpUpsert || mutation.EntityKey != "rel-1" || mutation.Project != "proj-a" {
		t.Fatalf("expected canonical relation/upsert mutation, got %+v", mutation)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
		t.Fatalf("decode canonical relation payload: %v", err)
	}
	if payload["project"] != "proj-a" {
		t.Fatalf("expected relation payload project rewritten to proj-a, got %#v", payload["project"])
	}
	for _, field := range []string{"sync_id", "source_id", "target_id", "relation", "judgment_status", "marked_by_actor", "marked_by_kind"} {
		if payload[field] == "" || payload[field] == nil {
			t.Fatalf("expected relation payload field %q to be preserved, got %#v", field, payload)
		}
	}
}

func TestCanonicalizeForProjectRejectsInvalidRelationMutation(t *testing.T) {
	raw := []byte(`{
		"mutations": [
			{
				"entity": "relation",
				"entity_key": "rel-1",
				"op": "upsert",
				"payload": "{\"sync_id\":\"rel-1\",\"source_id\":\"obs-a\",\"target_id\":\"\",\"judgment_status\":\"judged\",\"marked_by_actor\":\"agent-a\",\"marked_by_kind\":\"agent\"}"
			}
		]
	}`)

	_, err := CanonicalizeForProject(raw, "proj-a")
	if err == nil {
		t.Fatal("expected invalid relation mutation to fail")
	}
	if got := err.Error(); got == "" || !containsAll(got, []string{"relation", "target_id"}) {
		t.Fatalf("expected relation target_id validation error, got %q", got)
	}
}

func containsAll(s string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}

func TestCanonicalizeForProjectPreservesClosureOnlyDirectSessionOwnership(t *testing.T) {
	raw := []byte(`{
		"sessions": [
			{"id":"sess-closure","project":"proj-b","directory":"/tmp/proj-b"},
			{"id":"sess-owned","project":"proj-b","directory":"/tmp/proj-b-owned"}
		],
		"mutations": [
			{
				"entity": "session",
				"entity_key": "sess-owned",
				"op": "upsert",
				"project": "proj-b",
				"payload": "{\"id\":\"sess-owned\",\"project\":\"proj-b\",\"directory\":\"/tmp/proj-b-owned\"}"
			}
		]
	}`)

	canonical, err := CanonicalizeForProject(raw, "proj-a")
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}

	var decoded struct {
		Sessions  []store.Session      `json:"sessions"`
		Mutations []store.SyncMutation `json:"mutations"`
	}
	if err := json.Unmarshal(canonical, &decoded); err != nil {
		t.Fatalf("decode canonicalized payload: %v", err)
	}

	if len(decoded.Sessions) != 2 {
		t.Fatalf("expected 2 direct sessions, got %d", len(decoded.Sessions))
	}

	projectsBySession := map[string]string{}
	for _, session := range decoded.Sessions {
		projectsBySession[session.ID] = session.Project
	}

	if projectsBySession["sess-closure"] != "proj-b" {
		t.Fatalf("expected closure-only session ownership to be preserved, got %q", projectsBySession["sess-closure"])
	}
	if projectsBySession["sess-owned"] != "proj-a" {
		t.Fatalf("expected direct session with explicit mutation to be canonicalized, got %q", projectsBySession["sess-owned"])
	}
}

func TestCanonicalizeForProjectCanonicalizesDependencySessionsInMixedChunk(t *testing.T) {
	raw := []byte(`{
		"sessions": [
			{"id":"sess-dependency","project":"proj-b","directory":"/tmp/proj-b"}
		],
		"observations": [
			{"sync_id":"obs-direct","session_id":"sess-dependency","type":"note","title":"direct","content":"kept","project":"proj-b","scope":"project"}
		],
		"mutations": [
			{
				"entity": "observation",
				"entity_key": "obs-mut",
				"op": "upsert",
				"project": "proj-b",
				"payload": "{\"sync_id\":\"obs-mut\",\"session_id\":\"sess-dependency\",\"type\":\"note\",\"title\":\"dependency\",\"content\":\"retained\",\"scope\":\"project\",\"project\":\"proj-b\"}"
			}
		]
	}`)

	canonical, err := CanonicalizeForProject(raw, "proj-a")
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}

	var decoded struct {
		Sessions []store.Session `json:"sessions"`
	}
	if err := json.Unmarshal(canonical, &decoded); err != nil {
		t.Fatalf("decode canonicalized payload: %v", err)
	}

	if len(decoded.Sessions) != 1 {
		t.Fatalf("expected 1 direct session, got %d", len(decoded.Sessions))
	}
	if decoded.Sessions[0].Project != "proj-a" {
		t.Fatalf("expected dependency session to be canonicalized to proj-a, got %q", decoded.Sessions[0].Project)
	}
}

func TestCanonicalizeForProjectDerivesSessionOwnershipFromPayloadIDWhenEntityKeyMissing(t *testing.T) {
	raw := []byte(`{
		"sessions": [
			{"id":"sess-owned","project":"proj-b","directory":"/tmp/proj-b"}
		],
		"mutations": [
			{
				"entity": "session",
				"op": "upsert",
				"project": "proj-b",
				"payload": "{\"id\":\"sess-owned\",\"project\":\"proj-b\",\"directory\":\"/tmp/proj-b\"}"
			}
		]
	}`)

	canonical, err := CanonicalizeForProject(raw, "proj-a")
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}

	var decoded struct {
		Sessions  []store.Session      `json:"sessions"`
		Mutations []store.SyncMutation `json:"mutations"`
	}
	if err := json.Unmarshal(canonical, &decoded); err != nil {
		t.Fatalf("decode canonicalized payload: %v", err)
	}

	if len(decoded.Sessions) != 1 {
		t.Fatalf("expected 1 direct session, got %d", len(decoded.Sessions))
	}
	if decoded.Sessions[0].Project != "proj-a" {
		t.Fatalf("expected direct session ownership derived from payload id to be canonicalized, got %q", decoded.Sessions[0].Project)
	}
	if len(decoded.Mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(decoded.Mutations))
	}
	if decoded.Mutations[0].EntityKey != "sess-owned" {
		t.Fatalf("expected canonicalized mutation entity_key to be derived from payload id, got %q", decoded.Mutations[0].EntityKey)
	}
}

func TestCanonicalizeForProjectAcceptsSessionDeleteMutation(t *testing.T) {
	raw := []byte(`{
		"mutations": [
			{
				"entity": "session",
				"op": "delete",
				"project": "wrong",
				"payload": "{\"id\":\"sess-delete\",\"project\":\"wrong\",\"deleted_at\":\"2026-04-26T12:00:00Z\"}"
			}
		]
	}`)

	normalized, err := CanonicalizeForProject(raw, "proj-a")
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}

	var chunk struct {
		Mutations []store.SyncMutation `json:"mutations"`
	}
	if err := json.Unmarshal(normalized, &chunk); err != nil {
		t.Fatalf("decode canonicalized chunk: %v", err)
	}
	if len(chunk.Mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(chunk.Mutations))
	}
	mutation := chunk.Mutations[0]
	if mutation.Entity != store.SyncEntitySession || mutation.Op != store.SyncOpDelete || mutation.EntityKey != "sess-delete" {
		t.Fatalf("expected canonical session/delete mutation, got %+v", mutation)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
		t.Fatalf("decode canonical payload: %v", err)
	}
	if payload["id"] != "sess-delete" {
		t.Fatalf("expected payload id sess-delete, got %#v", payload["id"])
	}
	if payload["project"] != "proj-a" {
		t.Fatalf("expected payload project rewritten to proj-a, got %#v", payload["project"])
	}
	if payload["deleted_at"] != "2026-04-26T12:00:00Z" {
		t.Fatalf("expected deleted_at preserved, got %#v", payload["deleted_at"])
	}
	if _, ok := payload["directory"]; ok {
		t.Fatalf("expected canonical session delete payload without directory, got %#v", payload)
	}
}

func TestValidateMutationEntryCanonicalContract(t *testing.T) {
	validPayloads := map[string]map[string]any{
		store.SyncEntitySession: {
			"id":        "session-1",
			"directory": "/work/project",
		},
		store.SyncEntityObservation: {
			"sync_id":    "observation-1",
			"session_id": "session-1",
			"type":       "decision",
			"title":      "A decision",
			"content":    "The decision body",
			"scope":      "project",
		},
		store.SyncEntityPrompt: {
			"sync_id":    "prompt-1",
			"session_id": "session-1",
			"content":    "The prompt body",
		},
		store.SyncEntityRelation: {
			"sync_id":         "relation-1",
			"source_id":       "observation-1",
			"target_id":       "observation-2",
			"relation":        "related",
			"judgment_status": "judged",
			"marked_by_actor": "agent-1",
			"marked_by_kind":  "agent",
		},
	}
	wrongTypeObservation := cloneMutationPayload(validPayloads[store.SyncEntityObservation])
	wrongTypeObservation["content"] = 42

	tests := []struct {
		name      string
		entity    string
		op        string
		entityKey string
		payload   json.RawMessage
		wantField string
		wantValid bool
	}{
		{
			name:      "valid session upsert",
			entity:    store.SyncEntitySession,
			op:        store.SyncOpUpsert,
			entityKey: "session-1",
			payload:   rawMutationPayload(t, validPayloads[store.SyncEntitySession]),
			wantValid: true,
		},
		{
			name:      "valid observation upsert",
			entity:    store.SyncEntityObservation,
			op:        store.SyncOpUpsert,
			entityKey: "observation-1",
			payload:   rawMutationPayload(t, validPayloads[store.SyncEntityObservation]),
			wantValid: true,
		},
		{
			name:      "valid prompt upsert",
			entity:    store.SyncEntityPrompt,
			op:        store.SyncOpUpsert,
			entityKey: "prompt-1",
			payload:   rawMutationPayload(t, validPayloads[store.SyncEntityPrompt]),
			wantValid: true,
		},
		{
			name:      "valid relation upsert",
			entity:    store.SyncEntityRelation,
			op:        store.SyncOpUpsert,
			entityKey: "relation-1",
			payload:   rawMutationPayload(t, validPayloads[store.SyncEntityRelation]),
			wantValid: true,
		},
		{
			name:      "session delete requires only identity",
			entity:    store.SyncEntitySession,
			op:        store.SyncOpDelete,
			entityKey: "session-delete",
			payload:   json.RawMessage(`{"id":"session-delete"}`),
			wantValid: true,
		},
		{
			name:      "observation delete requires only identity",
			entity:    store.SyncEntityObservation,
			op:        store.SyncOpDelete,
			entityKey: "observation-delete",
			payload:   json.RawMessage(`{"sync_id":"observation-delete"}`),
			wantValid: true,
		},
		{
			name:      "prompt delete requires only identity",
			entity:    store.SyncEntityPrompt,
			op:        store.SyncOpDelete,
			entityKey: "prompt-delete",
			payload:   json.RawMessage(`{"sync_id":"prompt-delete"}`),
			wantValid: true,
		},
		{
			name:      "relation delete is unsupported",
			entity:    store.SyncEntityRelation,
			op:        store.SyncOpDelete,
			entityKey: "relation-1",
			payload:   rawMutationPayload(t, validPayloads[store.SyncEntityRelation]),
			wantField: "op",
		},
		{
			name:      "non-object array payload",
			entity:    store.SyncEntitySession,
			op:        store.SyncOpUpsert,
			payload:   json.RawMessage(`[]`),
			wantField: "payload",
		},
		{
			name:      "non-object scalar payload",
			entity:    store.SyncEntitySession,
			op:        store.SyncOpUpsert,
			payload:   json.RawMessage(`42`),
			wantField: "payload",
		},
		{
			name:      "null payload",
			entity:    store.SyncEntitySession,
			op:        store.SyncOpUpsert,
			payload:   json.RawMessage(`null`),
			wantField: "payload",
		},
		{
			name:      "encoded object payload",
			entity:    store.SyncEntitySession,
			op:        store.SyncOpUpsert,
			payload:   encodedMutationPayload(t, validPayloads[store.SyncEntitySession]),
			wantValid: true,
		},
		{
			name:      "encoded array payload",
			entity:    store.SyncEntitySession,
			op:        store.SyncOpUpsert,
			entityKey: "session-1",
			payload:   encodedRawMutationPayload(t, `[]`),
			wantField: "payload",
		},
		{
			name:      "encoded scalar payload",
			entity:    store.SyncEntitySession,
			op:        store.SyncOpUpsert,
			entityKey: "session-1",
			payload:   encodedRawMutationPayload(t, `42`),
			wantField: "payload",
		},
		{
			name:      "encoded malformed object payload",
			entity:    store.SyncEntitySession,
			op:        store.SyncOpUpsert,
			entityKey: "session-1",
			payload:   encodedRawMutationPayload(t, `{"id":"session-1"`),
			wantField: "payload",
		},
		{
			name:      "non-string required field",
			entity:    store.SyncEntityObservation,
			op:        store.SyncOpUpsert,
			entityKey: "observation-1",
			payload:   rawMutationPayload(t, wrongTypeObservation),
			wantField: "content",
		},
		{
			name:      "entity key mismatch",
			entity:    store.SyncEntityObservation,
			op:        store.SyncOpUpsert,
			entityKey: "observation-other",
			payload:   rawMutationPayload(t, validPayloads[store.SyncEntityObservation]),
			wantField: "entity_key",
		},
		{
			name:      "unsupported entity",
			entity:    "unknown",
			op:        store.SyncOpUpsert,
			payload:   json.RawMessage(`{}`),
			wantField: "entity",
		},
		{
			name:      "unsupported operation",
			entity:    store.SyncEntitySession,
			op:        "replace",
			payload:   rawMutationPayload(t, validPayloads[store.SyncEntitySession]),
			wantField: "op",
		},
	}

	canonicalRequirements := []struct {
		entity string
		fields []string
	}{
		{store.SyncEntitySession, []string{"id", "directory"}},
		{store.SyncEntityObservation, []string{"sync_id", "session_id", "type", "title", "content", "scope"}},
		{store.SyncEntityPrompt, []string{"sync_id", "session_id", "content"}},
		{store.SyncEntityRelation, []string{"sync_id", "source_id", "target_id", "relation", "judgment_status", "marked_by_actor", "marked_by_kind"}},
	}
	for _, requirement := range canonicalRequirements {
		for _, field := range requirement.fields {
			missing := cloneMutationPayload(validPayloads[requirement.entity])
			delete(missing, field)
			tests = append(tests, struct {
				name      string
				entity    string
				op        string
				entityKey string
				payload   json.RawMessage
				wantField string
				wantValid bool
			}{
				name:      requirement.entity + " upsert missing " + field,
				entity:    requirement.entity,
				op:        store.SyncOpUpsert,
				entityKey: canonicalMutationEntityKey(requirement.entity),
				payload:   rawMutationPayload(t, missing),
				wantField: field,
			})

			blank := cloneMutationPayload(validPayloads[requirement.entity])
			blank[field] = " \t"
			tests = append(tests, struct {
				name      string
				entity    string
				op        string
				entityKey string
				payload   json.RawMessage
				wantField string
				wantValid bool
			}{
				name:      requirement.entity + " upsert blank " + field,
				entity:    requirement.entity,
				op:        store.SyncOpUpsert,
				entityKey: canonicalMutationEntityKey(requirement.entity),
				payload:   rawMutationPayload(t, blank),
				wantField: field,
			})
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue, valid := ValidateMutationEntry(tt.entity, tt.op, tt.entityKey, tt.payload)
			if valid != tt.wantValid {
				t.Fatalf("valid=%v, want %v; issue=%+v", valid, tt.wantValid, issue)
			}
			if issue.Field != tt.wantField {
				t.Fatalf("issue field=%q, want %q; issue=%+v", issue.Field, tt.wantField, issue)
			}
			if !valid && strings.TrimSpace(issue.Message) == "" {
				t.Fatalf("invalid mutation must include a message: %+v", issue)
			}
		})
	}
}

func TestValidateMutationEntryAllowsDerivedEntityKey(t *testing.T) {
	payload := json.RawMessage(`{"sync_id":"observation-1","session_id":"session-1","type":"decision","title":"A decision","content":"The decision body","scope":"project"}`)

	issue, valid := ValidateMutationEntry(store.SyncEntityObservation, store.SyncOpUpsert, "", payload)
	if !valid || issue != (MutationValidationIssue{}) {
		t.Fatalf("expected omitted entity key to be derivable, valid=%v issue=%+v", valid, issue)
	}
}

func TestCanonicalizeForProjectRetainsEncodedMutationPayloadCompatibility(t *testing.T) {
	raw := []byte(`{
		"mutations": [
			{
				"entity": "session",
				"op": "upsert",
				"payload": "{\"id\":\"session-encoded\",\"directory\":\"/work/project\"}"
			}
		]
	}`)

	canonical, err := CanonicalizeForProject(raw, "proj-a")
	if err != nil {
		t.Fatalf("canonicalize encoded mutation payload: %v", err)
	}

	var chunk struct {
		Mutations []store.SyncMutation `json:"mutations"`
	}
	if err := json.Unmarshal(canonical, &chunk); err != nil {
		t.Fatalf("decode canonicalized chunk: %v", err)
	}
	if len(chunk.Mutations) != 1 || chunk.Mutations[0].EntityKey != "session-encoded" {
		t.Fatalf("expected encoded payload to retain canonical session identity, got %+v", chunk.Mutations)
	}
}

func rawMutationPayload(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal mutation payload: %v", err)
	}
	return payload
}

func encodedMutationPayload(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	payload := rawMutationPayload(t, fields)
	return encodedRawMutationPayload(t, string(payload))
}

func encodedRawMutationPayload(t *testing.T, payload string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(string(payload))
	if err != nil {
		t.Fatalf("marshal encoded mutation payload: %v", err)
	}
	return encoded
}

func cloneMutationPayload(fields map[string]any) map[string]any {
	clone := make(map[string]any, len(fields))
	for key, value := range fields {
		clone[key] = value
	}
	return clone
}

func canonicalMutationEntityKey(entity string) string {
	switch entity {
	case store.SyncEntitySession:
		return "session-1"
	case store.SyncEntityObservation:
		return "observation-1"
	case store.SyncEntityPrompt:
		return "prompt-1"
	case store.SyncEntityRelation:
		return "relation-1"
	default:
		return ""
	}
}
