// Package agentops contains transport-neutral compound argument projections.
// Persistence and business rules remain owned by store.
package agentops

import (
	"errors"
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"math"
	"strings"
)

// ParseConsolidate accepts the JSON/MCP argument shape, never owner overrides.
func ParseConsolidate(args map[string]any) (store.ConsolidateParams, error) {
	p := store.ConsolidateParams{}
	for key := range args {
		switch key {
		case "ids_to_delete", "new_title", "new_content", "expected_project", "type", "topic_key", "confirm", "fingerprint":
		default:
			return p, fmt.Errorf("unsupported argument %q; source project/session/scope cannot be overridden", key)
		}
	}
	required := make(map[string]string, 3)
	for _, key := range []string{"new_title", "new_content", "expected_project"} {
		v, ok := args[key].(string)
		if !ok || strings.TrimSpace(v) == "" {
			return p, fmt.Errorf("%s must be a non-empty string", key)
		}
		required[key] = v
	}
	p.Overrides.Title = required["new_title"]
	p.Overrides.Content = required["new_content"]
	owner := required["expected_project"]
	p.ExpectedProject = &owner
	ids, ok := args["ids_to_delete"].([]any)
	if !ok || len(ids) < 2 {
		return p, errors.New("ids_to_delete must be an array of at least two positive integer IDs")
	}
	for _, raw := range ids {
		n, ok := raw.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n > 9007199254740991 || n != math.Trunc(n) {
			return p, errors.New("ids_to_delete must contain positive exact JSON integers")
		}
		p.IDs = append(p.IDs, int64(n))
	}
	optional := map[string]*string{"type": &p.Overrides.Type, "topic_key": &p.Overrides.TopicKey, "fingerprint": &p.Fingerprint}
	for key, target := range optional {
		if raw, exists := args[key]; exists {
			v, ok := raw.(string)
			if !ok {
				return p, fmt.Errorf("%s must be a string", key)
			}
			*target = v
		}
	}
	if raw, exists := args["confirm"]; exists {
		v, ok := raw.(bool)
		if !ok {
			return p, errors.New("confirm must be a boolean")
		}
		p.Confirm = v
	}
	if p.Confirm && strings.TrimSpace(p.Fingerprint) == "" {
		return p, errors.New("fingerprint is required when confirm is true")
	}
	return p, nil
}

// ConsolidationPlanJSON preserves the agent plan wire shape across transports.
func ConsolidationPlanJSON(plan *store.ConsolidatePlan) map[string]any {
	if plan == nil {
		return nil
	}
	s := plan.Survivor
	return map[string]any{"source_ids": plan.IDs, "sources": plan.Sources, "project": plan.Project, "fingerprint": plan.Fingerprint, "survivor": map[string]any{"session_id": s.SessionID, "title": s.Title, "content": s.Content, "type": s.Type, "scope": s.Scope, "project": s.Project, "topic_key": s.TopicKey}}
}
