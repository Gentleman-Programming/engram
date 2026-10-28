package agentops

import (
	"errors"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"math"
)

// JudgmentBatch validates the batch envelope before any item is applied.
// Individual malformed judgments remain ordered item errors, as in MCP.
func JudgmentBatch(args map[string]any) ([]any, string, error) {
	items, ok := args["judgments"].([]any)
	if !ok || len(items) < 1 || len(items) > 32 {
		return nil, "", errors.New("judgments must be an array containing 1..32 items")
	}
	session := ""
	if raw, present := args["session_id"]; present {
		var valid bool
		session, valid = raw.(string)
		if !valid {
			return nil, "", errors.New("session_id must be a string")
		}
	}
	return items, session, nil
}

// JudgmentItemError validates batch item types without coercing them.
func JudgmentItemError(raw any) string {
	args, ok := raw.(map[string]any)
	if !ok {
		return "judgment must be an object"
	}
	for _, key := range []string{"judgment_id", "relation", "reason", "evidence"} {
		if value, exists := args[key]; exists {
			if _, ok := value.(string); !ok {
				return key + " must be a string"
			}
		}
	}
	if value, exists := args["confidence"]; exists {
		v, ok := value.(float64)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
			return "confidence must be a number between 0.0 and 1.0"
		}
	}
	return ""
}

// JudgeParams retains the existing single MCP judge's optional-field behavior.
func JudgeParams(args map[string]any) (store.JudgeRelationParams, error) {
	p := store.JudgeRelationParams{MarkedByActor: "agent", MarkedByKind: "agent"}
	p.JudgmentID, _ = args["judgment_id"].(string)
	p.Relation, _ = args["relation"].(string)
	p.SessionID, _ = args["session_id"].(string)
	if p.JudgmentID == "" {
		return p, errors.New("judgment_id is required")
	}
	if p.Relation == "" {
		return p, errors.New("relation is required")
	}
	if v, ok := args["reason"].(string); ok && v != "" {
		p.Reason = &v
	}
	if v, ok := args["evidence"].(string); ok && v != "" {
		p.Evidence = &v
	}
	if v, ok := args["confidence"].(float64); ok {
		if v < 0 || v > 1 {
			return p, errors.New("confidence must be between 0.0 and 1.0")
		}
		p.Confidence = &v
	}
	return p, nil
}
