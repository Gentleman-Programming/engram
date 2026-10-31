package store

import (
	"context"
	"errors"
)

// SearchPreviewRelationsContext batch-loads optional search annotations. Ordinary
// relation errors do not hide search hits; caller cancellation always propagates.
func (s *Store) SearchPreviewRelationsContext(ctx context.Context, results []SearchPreviewResult) (map[string]ObservationRelations, error) {
	ids := make([]string, 0, len(results))
	for _, r := range results {
		if r.SyncID != "" {
			ids = append(ids, r.SyncID)
		}
	}
	relations := map[string]ObservationRelations{}
	if len(ids) == 0 {
		return relations, nil
	}
	loaded, err := s.GetRelationsForObservationsContext(ctx, ids)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if err == nil {
		relations = loaded
	}
	return relations, nil
}

// CompactSearchProjection is the shared bounded wire projection, not hydration.
// Empty results are deliberately a non-nil slice for HTTP's stable array contract.
func CompactSearchProjection(results []SearchPreviewResult, relations map[string]ObservationRelations) []map[string]any {
	entries := make([]map[string]any, 0, len(results))
	for _, r := range results {
		entry := map[string]any{"id": r.ID, "sync_id": r.SyncID, "title": r.Title, "type": r.Type, "state": r.State(), "scope": r.Scope, "pinned": r.Pinned, "preview": r.Preview, "truncated": r.Truncated}
		if r.RetrievalMode == "semantic" {
			entry["retrieval_mode"] = r.RetrievalMode
			if r.Similarity != nil {
				entry["similarity"] = *r.Similarity
			}
		}
		if r.Project != nil {
			entry["project"] = *r.Project
		}
		if r.TopicKey != nil && *r.TopicKey != "" {
			entry["topic_key"] = *r.TopicKey
		}
		if r.ReviewAfter != nil {
			entry["review_after"] = *r.ReviewAfter
		}
		if rels, ok := relations[r.SyncID]; ok {
			entry["relations"] = map[string]any{"as_source": rels.AsSource, "as_target": rels.AsTarget}
		}
		entries = append(entries, entry)
	}
	return entries
}
