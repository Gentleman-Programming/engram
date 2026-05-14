package chunkcodec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func ChunkID(payload []byte) string {
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])[:8]
}

func CanonicalizeForProject(payload []byte, project string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, fmt.Errorf("decode chunk data: %w", err)
	}

	hasMutationList := false
	sessionMutationKeys := map[string]struct{}{}
	requiredSessionKeys := map[string]struct{}{}
	if entries, exists := doc["mutations"]; exists {
		hasMutationList = true
		keys, err := collectSessionMutationKeys(entries)
		if err != nil {
			return nil, err
		}
		sessionMutationKeys = keys
		dependencies, err := collectRequiredSessionKeys(doc, entries)
		if err != nil {
			return nil, err
		}
		requiredSessionKeys = dependencies
	}

	for _, key := range []string{"sessions", "observations", "prompts"} {
		entries, exists := doc[key]
		if !exists {
			continue
		}
		if entries == nil {
			doc[key] = []any{}
			continue
		}
		items, ok := entries.([]any)
		if !ok {
			return nil, fmt.Errorf("%s must be an array", key)
		}
		for i, item := range items {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be an object", key, i)
			}
			if key == "sessions" {
				sessionID, _ := row["id"].(string)
				sessionID = strings.TrimSpace(sessionID)
				if !hasMutationList || sessionID == "" {
					row["project"] = project
				} else if _, owned := sessionMutationKeys[sessionID]; owned {
					row["project"] = project
				} else if _, required := requiredSessionKeys[sessionID]; required {
					row["project"] = project
				}
			} else {
				row["project"] = project
			}
			items[i] = row
		}
		doc[key] = items
	}

	if entries, exists := doc["mutations"]; exists {
		if entries == nil {
			doc["mutations"] = []any{}
			entries = doc["mutations"]
		}
		items, ok := entries.([]any)
		if !ok {
			return nil, fmt.Errorf("mutations must be an array")
		}
		normalizedMutations := make([]any, len(items))
		for i, item := range items {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("mutations[%d] must be an object", i)
			}
			normalized, err := normalizeChunkMutation(row, project)
			if err != nil {
				return nil, fmt.Errorf("mutations[%d]: %w", i, err)
			}
			normalizedMutations[i] = normalized
		}
		doc["mutations"] = normalizedMutations
	}

	normalized, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode chunk data: %w", err)
	}
	return normalized, nil
}

func collectSessionMutationKeys(entries any) (map[string]struct{}, error) {
	keys := map[string]struct{}{}
	if entries == nil {
		return keys, nil
	}
	items, ok := entries.([]any)
	if !ok {
		return nil, fmt.Errorf("mutations must be an array")
	}
	for i, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("mutations[%d] must be an object", i)
		}
		entity, _ := row["entity"].(string)
		op, _ := row["op"].(string)
		trimmedEntity := strings.TrimSpace(entity)
		trimmedOp := strings.TrimSpace(op)
		if trimmedEntity != store.SyncEntitySession || (trimmedOp != store.SyncOpUpsert && trimmedOp != store.SyncOpDelete) {
			continue
		}
		entityKey, _ := row["entity_key"].(string)
		entityKey = strings.TrimSpace(entityKey)
		if entityKey != "" {
			keys[entityKey] = struct{}{}
		}
		payload, _ := row["payload"].(string)
		payload = strings.TrimSpace(payload)
		if payload != "" {
			var body mutationSessionPayload
			if err := DecodeSyncMutationPayload(payload, &body); err == nil {
				body.ID = strings.TrimSpace(body.ID)
				if body.ID != "" {
					keys[body.ID] = struct{}{}
				}
			}
		}
	}
	return keys, nil
}

func collectRequiredSessionKeys(doc map[string]any, mutationEntries any) (map[string]struct{}, error) {
	keys := map[string]struct{}{}
	for _, entityKey := range []string{"observations", "prompts"} {
		entries, exists := doc[entityKey]
		if !exists || entries == nil {
			continue
		}
		items, ok := entries.([]any)
		if !ok {
			return nil, fmt.Errorf("%s must be an array", entityKey)
		}
		for i, item := range items {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be an object", entityKey, i)
			}
			sessionID, _ := row["session_id"].(string)
			sessionID = strings.TrimSpace(sessionID)
			if sessionID != "" {
				keys[sessionID] = struct{}{}
			}
		}
	}

	if mutationEntries == nil {
		return keys, nil
	}
	items, ok := mutationEntries.([]any)
	if !ok {
		return nil, fmt.Errorf("mutations must be an array")
	}
	for i, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("mutations[%d] must be an object", i)
		}
		entity, _ := row["entity"].(string)
		op, _ := row["op"].(string)
		entity = strings.TrimSpace(entity)
		op = strings.TrimSpace(op)
		if op != store.SyncOpUpsert {
			continue
		}
		payload, _ := row["payload"].(string)
		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}
		switch entity {
		case store.SyncEntityObservation:
			var body mutationObservationPayload
			if err := DecodeSyncMutationPayload(payload, &body); err == nil {
				body.SessionID = strings.TrimSpace(body.SessionID)
				if body.SessionID != "" {
					keys[body.SessionID] = struct{}{}
				}
			}
		case store.SyncEntityPrompt:
			var body mutationPromptPayload
			if err := DecodeSyncMutationPayload(payload, &body); err == nil {
				body.SessionID = strings.TrimSpace(body.SessionID)
				if body.SessionID != "" {
					keys[body.SessionID] = struct{}{}
				}
			}
		}
	}

	return keys, nil
}

type mutationSessionPayload struct {
	ID         string  `json:"id"`
	Project    string  `json:"project"`
	Directory  string  `json:"directory,omitempty"`
	StartedAt  string  `json:"started_at,omitempty"`
	EndedAt    *string `json:"ended_at,omitempty"`
	Summary    *string `json:"summary,omitempty"`
	Deleted    bool    `json:"deleted,omitempty"`
	DeletedAt  *string `json:"deleted_at,omitempty"`
	HardDelete bool    `json:"hard_delete,omitempty"`
}

type mutationObservationPayload struct {
	SyncID         string  `json:"sync_id"`
	SessionID      string  `json:"session_id"`
	Type           string  `json:"type"`
	Title          string  `json:"title"`
	Content        string  `json:"content"`
	ToolName       *string `json:"tool_name,omitempty"`
	Project        *string `json:"project,omitempty"`
	Scope          string  `json:"scope"`
	TopicKey       *string `json:"topic_key,omitempty"`
	RevisionCount  int     `json:"revision_count,omitempty"`
	DuplicateCount int     `json:"duplicate_count,omitempty"`
	LastSeenAt     *string `json:"last_seen_at,omitempty"`
	CreatedAt      string  `json:"created_at,omitempty"`
	UpdatedAt      string  `json:"updated_at,omitempty"`
	Deleted        bool    `json:"deleted,omitempty"`
	DeletedAt      *string `json:"deleted_at,omitempty"`
	HardDelete     bool    `json:"hard_delete,omitempty"`
}

type mutationPromptPayload struct {
	SyncID     string  `json:"sync_id"`
	SessionID  string  `json:"session_id"`
	Content    string  `json:"content"`
	Project    *string `json:"project,omitempty"`
	CreatedAt  string  `json:"created_at,omitempty"`
	Deleted    bool    `json:"deleted,omitempty"`
	DeletedAt  *string `json:"deleted_at,omitempty"`
	HardDelete bool    `json:"hard_delete,omitempty"`
}

type mutationRelationPayload struct {
	SyncID         string   `json:"sync_id"`
	SourceID       string   `json:"source_id"`
	TargetID       string   `json:"target_id"`
	Relation       string   `json:"relation"`
	Reason         *string  `json:"reason,omitempty"`
	Evidence       *string  `json:"evidence,omitempty"`
	Confidence     *float64 `json:"confidence,omitempty"`
	JudgmentStatus string   `json:"judgment_status"`
	MarkedByActor  *string  `json:"marked_by_actor,omitempty"`
	MarkedByKind   *string  `json:"marked_by_kind,omitempty"`
	MarkedByModel  *string  `json:"marked_by_model,omitempty"`
	SessionID      *string  `json:"session_id,omitempty"`
	Project        string   `json:"project"`
	CreatedAt      string   `json:"created_at,omitempty"`
	UpdatedAt      string   `json:"updated_at,omitempty"`
}

func normalizeChunkMutation(raw map[string]any, project string) (map[string]any, error) {
	mutationJSON, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("decode mutation: %w", err)
	}

	var mutation store.SyncMutation
	if err := json.Unmarshal(mutationJSON, &mutation); err != nil {
		return nil, fmt.Errorf("decode mutation: %w", err)
	}

	mutation.Entity = strings.TrimSpace(mutation.Entity)
	mutation.EntityKey = strings.TrimSpace(mutation.EntityKey)
	mutation.Op = strings.TrimSpace(mutation.Op)
	mutation.Payload = strings.TrimSpace(mutation.Payload)

	if err := validateSupportedMutation(mutation.Entity, mutation.Op); err != nil {
		return nil, err
	}
	if mutation.Payload == "" {
		return nil, fmt.Errorf("payload is required")
	}

	normalizedPayload, expectedEntityKey, err := normalizeMutationPayload(mutation.Entity, mutation.Op, mutation.Payload, project)
	if err != nil {
		return nil, err
	}
	if mutation.EntityKey == "" {
		mutation.EntityKey = expectedEntityKey
	}
	if mutation.EntityKey != expectedEntityKey {
		return nil, fmt.Errorf("entity_key %q does not match payload key %q", mutation.EntityKey, expectedEntityKey)
	}

	mutation.Project = project
	mutation.Payload = normalizedPayload

	normalizedJSON, err := json.Marshal(mutation)
	if err != nil {
		return nil, fmt.Errorf("encode mutation: %w", err)
	}

	var normalized map[string]any
	if err := json.Unmarshal(normalizedJSON, &normalized); err != nil {
		return nil, fmt.Errorf("encode mutation: %w", err)
	}
	return normalized, nil
}

func validateSupportedMutation(entity, op string) error {
	switch entity {
	case store.SyncEntitySession:
		if op != store.SyncOpUpsert && op != store.SyncOpDelete {
			return fmt.Errorf("unsupported mutation %q/%q", entity, op)
		}
		return nil
	case store.SyncEntityObservation, store.SyncEntityPrompt:
		if op != store.SyncOpUpsert && op != store.SyncOpDelete {
			return fmt.Errorf("unsupported mutation %q/%q", entity, op)
		}
		return nil
	case store.SyncEntityRelation:
		if op != store.SyncOpUpsert {
			return fmt.Errorf("unsupported mutation %q/%q", entity, op)
		}
		return nil
	default:
		return fmt.Errorf("unsupported mutation %q/%q", entity, op)
	}
}

func normalizeMutationPayload(entity, op, payload, project string) (normalizedPayload string, expectedEntityKey string, err error) {
	switch entity {
	case store.SyncEntitySession:
		var body mutationSessionPayload
		if err := DecodeSyncMutationPayload(payload, &body); err != nil {
			return "", "", fmt.Errorf("decode mutation payload: %w", err)
		}
		body.ID = strings.TrimSpace(body.ID)
		body.Directory = strings.TrimSpace(body.Directory)
		if body.ID == "" {
			return "", "", fmt.Errorf("session payload id is required")
		}
		if op == store.SyncOpUpsert && body.Directory == "" {
			return "", "", fmt.Errorf("session payload directory is required for upsert")
		}
		if op == store.SyncOpDelete {
			body.Directory = ""
			body.StartedAt = ""
			body.EndedAt = nil
			body.Summary = nil
			if body.DeletedAt != nil {
				trimmed := strings.TrimSpace(*body.DeletedAt)
				if trimmed == "" {
					body.DeletedAt = nil
				} else {
					body.DeletedAt = &trimmed
				}
			}
		}
		body.Project = project
		encoded, err := json.Marshal(body)
		if err != nil {
			return "", "", fmt.Errorf("encode mutation payload: %w", err)
		}
		return string(encoded), body.ID, nil
	case store.SyncEntityObservation:
		var body mutationObservationPayload
		if err := DecodeSyncMutationPayload(payload, &body); err != nil {
			return "", "", fmt.Errorf("decode mutation payload: %w", err)
		}
		body.SyncID = strings.TrimSpace(body.SyncID)
		body.SessionID = strings.TrimSpace(body.SessionID)
		if body.SyncID == "" {
			return "", "", fmt.Errorf("observation payload sync_id is required")
		}
		if op == store.SyncOpUpsert && body.SessionID == "" {
			return "", "", fmt.Errorf("observation payload session_id is required for upsert")
		}
		if op == store.SyncOpUpsert {
			body.Type = strings.TrimSpace(body.Type)
			body.Title = strings.TrimSpace(body.Title)
			body.Content = strings.TrimSpace(body.Content)
			body.Scope = strings.TrimSpace(body.Scope)
			if body.Type == "" {
				return "", "", fmt.Errorf("observation payload type is required for upsert")
			}
			if body.Title == "" {
				return "", "", fmt.Errorf("observation payload title is required for upsert")
			}
			if body.Content == "" {
				return "", "", fmt.Errorf("observation payload content is required for upsert")
			}
			if body.Scope == "" {
				return "", "", fmt.Errorf("observation payload scope is required for upsert")
			}
		}
		projectValue := project
		body.Project = &projectValue
		encoded, err := json.Marshal(body)
		if err != nil {
			return "", "", fmt.Errorf("encode mutation payload: %w", err)
		}
		return string(encoded), body.SyncID, nil
	case store.SyncEntityPrompt:
		var body mutationPromptPayload
		if err := DecodeSyncMutationPayload(payload, &body); err != nil {
			return "", "", fmt.Errorf("decode mutation payload: %w", err)
		}
		body.SyncID = strings.TrimSpace(body.SyncID)
		body.SessionID = strings.TrimSpace(body.SessionID)
		if body.SyncID == "" {
			return "", "", fmt.Errorf("prompt payload sync_id is required")
		}
		if op == store.SyncOpUpsert && body.SessionID == "" {
			return "", "", fmt.Errorf("prompt payload session_id is required for upsert")
		}
		if op == store.SyncOpUpsert {
			body.Content = strings.TrimSpace(body.Content)
			if body.Content == "" {
				return "", "", fmt.Errorf("prompt payload content is required for upsert")
			}
		}
		projectValue := project
		body.Project = &projectValue
		encoded, err := json.Marshal(body)
		if err != nil {
			return "", "", fmt.Errorf("encode mutation payload: %w", err)
		}
		return string(encoded), body.SyncID, nil
	case store.SyncEntityRelation:
		var body mutationRelationPayload
		if err := DecodeSyncMutationPayload(payload, &body); err != nil {
			return "", "", fmt.Errorf("decode mutation payload: %w", err)
		}
		body.SyncID = strings.TrimSpace(body.SyncID)
		body.SourceID = strings.TrimSpace(body.SourceID)
		body.TargetID = strings.TrimSpace(body.TargetID)
		body.Relation = strings.TrimSpace(body.Relation)
		body.JudgmentStatus = strings.TrimSpace(body.JudgmentStatus)
		if body.MarkedByActor != nil {
			trimmed := strings.TrimSpace(*body.MarkedByActor)
			body.MarkedByActor = &trimmed
		}
		if body.MarkedByKind != nil {
			trimmed := strings.TrimSpace(*body.MarkedByKind)
			body.MarkedByKind = &trimmed
		}
		if body.SyncID == "" {
			return "", "", fmt.Errorf("relation payload sync_id is required for upsert")
		}
		if body.SourceID == "" {
			return "", "", fmt.Errorf("relation payload source_id is required for upsert")
		}
		if body.TargetID == "" {
			return "", "", fmt.Errorf("relation payload target_id is required for upsert")
		}
		if body.Relation == "" {
			return "", "", fmt.Errorf("relation payload relation is required for upsert")
		}
		if body.JudgmentStatus == "" {
			return "", "", fmt.Errorf("relation payload judgment_status is required for upsert")
		}
		if body.MarkedByActor == nil || *body.MarkedByActor == "" {
			return "", "", fmt.Errorf("relation payload marked_by_actor is required for upsert")
		}
		if body.MarkedByKind == nil || *body.MarkedByKind == "" {
			return "", "", fmt.Errorf("relation payload marked_by_kind is required for upsert")
		}
		body.Project = project
		encoded, err := json.Marshal(body)
		if err != nil {
			return "", "", fmt.Errorf("encode mutation payload: %w", err)
		}
		return string(encoded), body.SyncID, nil
	default:
		return "", "", fmt.Errorf("unsupported mutation %q/%q", entity, op)
	}
}

func DecodeSyncMutationPayload(payload string, dest any) error {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return fmt.Errorf("empty payload")
	}
	if trimmed[0] != '"' {
		return json.Unmarshal([]byte(trimmed), dest)
	}
	var encoded string
	if err := json.Unmarshal([]byte(trimmed), &encoded); err != nil {
		return err
	}
	return json.Unmarshal([]byte(encoded), dest)
}
