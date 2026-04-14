package cloudstore

import (
	"context"
	"fmt"
	"time"
)

// Mutation represents a single change from a client push.
type Mutation struct {
	Seq       int64          `json:"seq"`
	Entity    string         `json:"entity"`    // observation, session, prompt
	EntityKey string         `json:"entity_key"` // sync_id
	Op        string         `json:"op"`         // upsert, delete
	Payload   map[string]any `json:"payload"`
	OccurredAt string        `json:"occurred_at"`
}

// PushResult is the response to a push request.
type PushResult struct {
	AckedSeq  int64      `json:"acked_seq"`
	ServerSeq int64      `json:"server_seq"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
}

// Conflict describes a topic_key collision resolved by LWW.
type Conflict struct {
	TopicKey      string `json:"topic_key"`
	Winner        string `json:"winner"` // sync_id of the winning observation
	RevisionSaved bool   `json:"revision_saved"`
}

// ProcessPush processes a batch of mutations from a client push.
// Each mutation runs in its own transaction with advisory lock for seq assignment.
func (s *Store) ProcessPush(ctx context.Context, mutations []Mutation, userID, project string) (*PushResult, error) {
	result := &PushResult{}
	var maxClientSeq int64
	var maxServerSeq int64

	for _, m := range mutations {
		if m.Seq > maxClientSeq {
			maxClientSeq = m.Seq
		}

		switch m.Entity {
		case "observation":
			seq, conflict, err := s.processObservationMutation(ctx, m, userID, project)
			if err != nil {
				return nil, fmt.Errorf("push obs %s: %w", m.EntityKey, err)
			}
			if conflict != nil {
				result.Conflicts = append(result.Conflicts, *conflict)
			}
			if seq > maxServerSeq {
				maxServerSeq = seq
			}
		case "session":
			seq, err := s.processSessionMutation(ctx, m, userID, project)
			if err != nil {
				return nil, fmt.Errorf("push session %s: %w", m.EntityKey, err)
			}
			if seq > maxServerSeq {
				maxServerSeq = seq
			}
		case "prompt":
			seq, err := s.processPromptMutation(ctx, m, userID, project)
			if err != nil {
				return nil, fmt.Errorf("push prompt %s: %w", m.EntityKey, err)
			}
			if seq > maxServerSeq {
				maxServerSeq = seq
			}
		default:
			return nil, fmt.Errorf("unknown entity type: %s", m.Entity)
		}
	}

	result.AckedSeq = maxClientSeq
	result.ServerSeq = maxServerSeq
	return result, nil
}

func (s *Store) processObservationMutation(ctx context.Context, m Mutation, userID, project string) (int64, *Conflict, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)

	seq, err := NextSeq(ctx, tx, project)
	if err != nil {
		return 0, nil, err
	}

	p := m.Payload
	syncID := strVal(p, "sync_id")
	topicKey := strVal(p, "topic_key")
	scope := strVal(p, "scope")
	if scope == "" {
		scope = "project"
	}

	if m.Op == "delete" {
		_, err := tx.Exec(ctx, `
			UPDATE observations SET deleted_at = now(), server_seq = $1, updated_by = $2
			WHERE sync_id = $3 AND project = $4
		`, seq, userID, syncID, project)
		if err != nil {
			return 0, nil, err
		}
		return seq, nil, tx.Commit(ctx)
	}

	// Upsert: check for topic_key conflict
	var conflict *Conflict
	if topicKey != "" {
		var existingID, existingSyncID string
		err := tx.QueryRow(ctx, `
			SELECT id, sync_id FROM observations
			WHERE topic_key = $1 AND project = $2 AND scope = $3 AND deleted_at IS NULL
			FOR UPDATE
		`, topicKey, project, scope).Scan(&existingID, &existingSyncID)

		if err == nil && existingSyncID != syncID {
			// CONFLICT: different obs, same topic_key → save revision, then overwrite
			_, err = tx.Exec(ctx, `
				INSERT INTO observation_revisions (observation_id, project, title, content, type, topic_key, updated_by, revision_number)
				SELECT id, project, title, content, type, topic_key, updated_by, revision_count
				FROM observations WHERE id = $1
			`, existingID)
			if err != nil {
				return 0, nil, fmt.Errorf("save revision: %w", err)
			}

			_, err = tx.Exec(ctx, `
				UPDATE observations SET
					sync_id = $1, title = $2, content = $3, type = $4,
					tool_name = $5, scope = $6, topic_key = $7,
					updated_by = $8, updated_at = now(), server_seq = $9,
					revision_count = revision_count + 1
				WHERE id = $10
			`, syncID, strVal(p, "title"), strVal(p, "content"), strVal(p, "type"),
				nullStr(strVal(p, "tool_name")), scope, topicKey,
				userID, seq, existingID)
			if err != nil {
				return 0, nil, fmt.Errorf("overwrite obs: %w", err)
			}

			conflict = &Conflict{TopicKey: topicKey, Winner: syncID, RevisionSaved: true}
			return seq, conflict, tx.Commit(ctx)
		}
	}

	// No conflict or no topic_key — upsert by sync_id
	_, err = tx.Exec(ctx, `
		INSERT INTO observations (sync_id, session_id, type, title, content, tool_name, project, scope, topic_key, created_by, updated_by, server_seq)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10, $11)
		ON CONFLICT (sync_id, project) DO UPDATE SET
			title = EXCLUDED.title, content = EXCLUDED.content, type = EXCLUDED.type,
			tool_name = EXCLUDED.tool_name, scope = EXCLUDED.scope, topic_key = EXCLUDED.topic_key,
			updated_by = EXCLUDED.updated_by, updated_at = now(), server_seq = EXCLUDED.server_seq,
			revision_count = observations.revision_count + 1
	`, syncID, strVal(p, "session_id"), strVal(p, "type"), strVal(p, "title"),
		strVal(p, "content"), nullStr(strVal(p, "tool_name")), project, scope,
		nullStr(topicKey), userID, seq)
	if err != nil {
		return 0, nil, err
	}

	return seq, conflict, tx.Commit(ctx)
}

func (s *Store) processSessionMutation(ctx context.Context, m Mutation, userID, project string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	seq, err := NextSeq(ctx, tx, project)
	if err != nil {
		return 0, err
	}

	p := m.Payload
	syncID := strVal(p, "id")
	if syncID == "" {
		syncID = m.EntityKey
	}

	startedAt := strVal(p, "started_at")
	if startedAt == "" {
		startedAt = time.Now().UTC().Format(time.RFC3339)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO sessions (sync_id, project, directory, user_id, started_at, ended_at, summary, server_seq)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (sync_id, project) DO UPDATE SET
			ended_at = EXCLUDED.ended_at, summary = EXCLUDED.summary, server_seq = EXCLUDED.server_seq
	`, syncID, project, strVal(p, "directory"), userID, startedAt,
		nullStr(strVal(p, "ended_at")), nullStr(strVal(p, "summary")), seq)
	if err != nil {
		return 0, err
	}

	return seq, tx.Commit(ctx)
}

func (s *Store) processPromptMutation(ctx context.Context, m Mutation, userID, project string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	seq, err := NextSeq(ctx, tx, project)
	if err != nil {
		return 0, err
	}

	p := m.Payload
	syncID := strVal(p, "sync_id")
	if syncID == "" {
		syncID = m.EntityKey
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO prompts (sync_id, session_id, content, project, user_id, server_seq)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (sync_id, project) DO UPDATE SET
			content = EXCLUDED.content, server_seq = EXCLUDED.server_seq
	`, syncID, strVal(p, "session_id"), strVal(p, "content"), project, userID, seq)
	if err != nil {
		return 0, err
	}

	return seq, tx.Commit(ctx)
}

// helpers

func strVal(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
