package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// buildRelationMutation builds a SyncMutation for entity='relation' from a
// syncRelationPayload.
func buildRelationMutation(t *testing.T, p syncRelationPayload) SyncMutation {
	t.Helper()
	if p.MarkedByActor == nil {
		actor := "test-actor"
		p.MarkedByActor = &actor
	}
	if p.MarkedByKind == nil {
		kind := "test"
		p.MarkedByKind = &kind
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("buildRelationMutation: marshal: %v", err)
	}
	return SyncMutation{
		Entity:    SyncEntityRelation,
		EntityKey: p.SyncID,
		Op:        SyncOpUpsert,
		Payload:   string(raw),
		Source:    SyncSourceRemote,
		Project:   p.Project,
	}
}

// applyRelationMutation calls applyPulledMutationTx inside a transaction.
func applyRelationMutation(t *testing.T, s *Store, m SyncMutation) error {
	t.Helper()
	return s.withTx(func(tx *sql.Tx) error {
		return s.applyPulledMutationTx(tx, m)
	})
}

func TestApplyPulledMutationMissingParentSession(t *testing.T) {
	const (
		parentSessionID = "missing-parent-session"
		project         = "missing-parent-project"
	)

	tests := []struct {
		name, entity, entityKey string
		payload                 func(t *testing.T) string
		countQuery              string
	}{
		{
			name:      "observation",
			entity:    SyncEntityObservation,
			entityKey: "missing-parent-observation",
			payload: func(t *testing.T) string {
				t.Helper()
				projectValue := project
				raw, err := json.Marshal(syncObservationPayload{
					SyncID: "missing-parent-observation", SessionID: parentSessionID,
					Type: "decision", Title: "Missing parent", Content: "defer until the session arrives",
					Project: &projectValue, Scope: "project",
				})
				if err != nil {
					t.Fatalf("marshal observation payload: %v", err)
				}
				return string(raw)
			},
			countQuery: `SELECT count(*) FROM observations WHERE sync_id = ?`,
		},
		{
			name:      "prompt",
			entity:    SyncEntityPrompt,
			entityKey: "missing-parent-prompt",
			payload: func(t *testing.T) string {
				t.Helper()
				projectValue := project
				raw, err := json.Marshal(syncPromptPayload{
					SyncID: "missing-parent-prompt", SessionID: parentSessionID,
					Content: "defer until the session arrives", Project: &projectValue,
				})
				if err != nil {
					t.Fatalf("marshal prompt payload: %v", err)
				}
				return string(raw)
			},
			countQuery: `SELECT count(*) FROM user_prompts WHERE sync_id = ?`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			mutation := SyncMutation{
				Seq: 7, Entity: tt.entity, EntityKey: tt.entityKey, Op: SyncOpUpsert,
				Payload: tt.payload(t), Project: project,
			}
			if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
				t.Fatalf("apply missing-parent mutation: %v", err)
			}

			state, err := s.GetSyncState(DefaultSyncTargetKey)
			if err != nil {
				t.Fatalf("get sync state: %v", err)
			}
			if state.LastPulledSeq != mutation.Seq {
				t.Fatalf("last pulled sequence = %d, want %d", state.LastPulledSeq, mutation.Seq)
			}

			deferred, err := s.ListDeferred(ListDeferredOptions{Status: "deferred"})
			if err != nil || len(deferred) != 1 {
				t.Fatalf("deferred evidence = %+v, err=%v", deferred, err)
			}
			evidence := deferred[0]
			if evidence.Entity != mutation.Entity || evidence.PayloadRaw != mutation.Payload || evidence.TargetKey != DefaultSyncTargetKey || evidence.RemoteSeq != mutation.Seq || evidence.EntityKey != mutation.EntityKey || evidence.Op != mutation.Op || evidence.ReasonCode != "pulled_parent_session_missing" {
				t.Fatalf("deferred evidence = %+v", evidence)
			}

			if err := s.CreateSession(parentSessionID, project, "/tmp/missing-parent"); err != nil {
				t.Fatalf("create parent session: %v", err)
			}
			result, err := s.ReplayDeferredForScope(DefaultSyncTargetKey, project)
			if err != nil {
				t.Fatalf("replay deferred: %v", err)
			}
			if result.Retried != 1 || result.Succeeded != 1 {
				t.Fatalf("replay result = %+v, want one successful retry", result)
			}
			if got := scalarInt(t, s, tt.countQuery, mutation.EntityKey); got != 1 {
				t.Fatalf("applied records = %d, want 1", got)
			}
			if got := countDeferredRows(t, s, evidence.SyncID); got != 0 {
				t.Fatalf("deferred evidence remains = %d, want 0", got)
			}
		})
	}
}

func TestApplyPulledChunkMissingParentSession(t *testing.T) {
	const (
		parentSessionID = "missing-parent-chunk-session"
		project         = "missing-parent-chunk-project"
	)

	tests := []struct {
		name, entity, entityKey string
		payload                 func(t *testing.T) string
		countQuery              string
	}{
		{
			name:      "observation",
			entity:    SyncEntityObservation,
			entityKey: "missing-parent-chunk-observation",
			payload: func(t *testing.T) string {
				t.Helper()
				projectValue := project
				raw, err := json.Marshal(syncObservationPayload{
					SyncID: "missing-parent-chunk-observation", SessionID: parentSessionID,
					Type: "decision", Title: "Missing chunk parent", Content: "defer until the chunk parent arrives",
					Project: &projectValue, Scope: "project",
				})
				if err != nil {
					t.Fatalf("marshal observation payload: %v", err)
				}
				return string(raw)
			},
			countQuery: `SELECT count(*) FROM observations WHERE sync_id = ?`,
		},
		{
			name:      "prompt",
			entity:    SyncEntityPrompt,
			entityKey: "missing-parent-chunk-prompt",
			payload: func(t *testing.T) string {
				t.Helper()
				projectValue := project
				raw, err := json.Marshal(syncPromptPayload{
					SyncID: "missing-parent-chunk-prompt", SessionID: parentSessionID,
					Content: "defer until the chunk parent arrives", Project: &projectValue,
				})
				if err != nil {
					t.Fatalf("marshal prompt payload: %v", err)
				}
				return string(raw)
			},
			countQuery: `SELECT count(*) FROM user_prompts WHERE sync_id = ?`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			chunkID := "missing-parent-chunk-" + tt.name
			mutation := SyncMutation{
				Entity: tt.entity, EntityKey: tt.entityKey, Op: SyncOpUpsert,
				Payload: tt.payload(t), Project: project,
			}
			if err := s.ApplyPulledChunk(DefaultSyncTargetKey, chunkID, []SyncMutation{mutation}); err != nil {
				t.Fatalf("apply missing-parent chunk: %v", err)
			}

			state, err := s.GetSyncState(DefaultSyncTargetKey)
			if err != nil {
				t.Fatalf("get sync state: %v", err)
			}
			if state.LastPulledSeq != 1 {
				t.Fatalf("last pulled sequence = %d, want 1", state.LastPulledSeq)
			}
			chunks, err := s.GetSyncedChunksForTarget(DefaultSyncTargetKey)
			if err != nil || !chunks[chunkID] {
				t.Fatalf("synced chunks = %v, err=%v", chunks, err)
			}

			deferred, err := s.ListDeferred(ListDeferredOptions{Status: "deferred"})
			if err != nil || len(deferred) != 1 {
				t.Fatalf("deferred evidence = %+v, err=%v", deferred, err)
			}
			evidence := deferred[0]
			if evidence.Entity != mutation.Entity || evidence.PayloadRaw != mutation.Payload || evidence.TargetKey != DefaultSyncTargetKey || evidence.RemoteSeq != 1 || evidence.EntityKey != mutation.EntityKey || evidence.Op != mutation.Op || evidence.ReasonCode != "pulled_parent_session_missing" {
				t.Fatalf("deferred evidence = %+v", evidence)
			}

			if err := s.CreateSession(parentSessionID, project, "/tmp/missing-parent-chunk"); err != nil {
				t.Fatalf("create parent session: %v", err)
			}
			result, err := s.ReplayDeferredForScope(DefaultSyncTargetKey, project)
			if err != nil {
				t.Fatalf("replay deferred: %v", err)
			}
			if result.Retried != 1 || result.Succeeded != 1 {
				t.Fatalf("replay result = %+v, want one successful retry", result)
			}
			if got := scalarInt(t, s, tt.countQuery, mutation.EntityKey); got != 1 {
				t.Fatalf("applied records = %d, want 1", got)
			}
			if got := countDeferredRows(t, s, evidence.SyncID); got != 0 {
				t.Fatalf("deferred evidence remains = %d, want 0", got)
			}
		})
	}
}

func TestApplyPulledMutation_CloudUpsertRespectsRemoteTombstoneFloor(t *testing.T) {
	floor := int64(10)
	tests := []struct {
		name, entity string
		remoteFloor  *int64
		seq          int64
		wantApplied  bool
		inactive     bool
	}{
		{name: "session unknown floor", entity: SyncEntitySession, seq: 1},
		{name: "session below floor", entity: SyncEntitySession, remoteFloor: &floor, seq: floor - 1},
		{name: "session inactive equal floor", entity: SyncEntitySession, remoteFloor: &floor, seq: floor, inactive: true},
		{name: "session above floor", entity: SyncEntitySession, remoteFloor: &floor, seq: floor + 1, wantApplied: true},
		{name: "observation unknown floor", entity: SyncEntityObservation, seq: 1},
		{name: "observation below floor", entity: SyncEntityObservation, remoteFloor: &floor, seq: floor - 1},
		{name: "observation equal floor", entity: SyncEntityObservation, remoteFloor: &floor, seq: floor},
		{name: "observation above floor", entity: SyncEntityObservation, remoteFloor: &floor, seq: floor + 1, wantApplied: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			key := tt.entity + "-remote-floor"
			if tt.entity == SyncEntityObservation {
				if err := s.CreateSession("remote-floor-parent", "remote-floor", "/tmp/remote-floor"); err != nil {
					t.Fatalf("create observation parent: %v", err)
				}
			}
			tombstoneActive := 1
			if tt.inactive {
				tombstoneActive = 0
			}
			if _, err := s.db.Exec(`
				INSERT INTO sync_delete_tombstones (entity, entity_key, project, active, last_remote_mutation_seq)
				VALUES (?, ?, 'remote-floor', ?, ?)
			`, tt.entity, key, tombstoneActive, tt.remoteFloor); err != nil {
				t.Fatalf("insert tombstone: %v", err)
			}
			project := "remote-floor"
			var raw []byte
			var err error
			if tt.entity == SyncEntitySession {
				raw, err = json.Marshal(syncSessionPayload{ID: key, Project: project, Directory: "/tmp/remote-floor"})
			} else {
				raw, err = json.Marshal(syncObservationPayload{SyncID: key, SessionID: "remote-floor-parent", Type: "decision", Title: "remote floor", Content: "cloud sequence", Project: &project, Scope: "project"})
			}
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{Seq: tt.seq, Entity: tt.entity, EntityKey: key, Op: SyncOpUpsert, Payload: string(raw)}); err != nil {
				t.Fatalf("apply pulled mutation: %v", err)
			}
			var count, active int
			query := `SELECT COUNT(*) FROM sessions WHERE id = ?`
			if tt.entity == SyncEntityObservation {
				query = `SELECT COUNT(*) FROM observations WHERE sync_id = ?`
			}
			if err := s.db.QueryRow(query, key).Scan(&count); err != nil {
				t.Fatalf("count entity: %v", err)
			}
			if (count == 1) != tt.wantApplied {
				t.Fatalf("entity applied = %t, want %t", count == 1, tt.wantApplied)
			}
			if err := s.db.QueryRow(`SELECT active FROM sync_delete_tombstones WHERE entity = ? AND entity_key = ?`, tt.entity, key).Scan(&active); err != nil {
				t.Fatalf("read tombstone: %v", err)
			}
			wantActive := tombstoneActive
			if tt.wantApplied {
				wantActive = 0
			}
			if active != wantActive {
				t.Fatalf("tombstone active = %d, want %d", active, wantActive)
			}
		})
	}
}

func TestApplyPulledMutation_MissingTargetFloor(t *testing.T) {
	tests := []struct {
		name                    string
		otherTargetHasFloor     bool
		wantTargetBUpsert       bool
		wantTargetAStillBlocked bool
	}{
		{
			name:                    "target B is admitted when only target A has a floor",
			otherTargetHasFloor:     true,
			wantTargetBUpsert:       true,
			wantTargetAStillBlocked: true,
		},
		{
			name:              "shared active blocks when no target floors exist",
			wantTargetBUpsert: false,
		},
	}
	entities := []struct {
		name, entity string
	}{
		{name: "session", entity: SyncEntitySession},
		{name: "observation", entity: SyncEntityObservation},
	}
	for _, tt := range tests {
		for _, entity := range entities {
			t.Run(tt.name+"/"+entity.name, func(t *testing.T) {
				s := newTestStore(t)
				const (
					entityKey = "missing-target-floor"
					project   = "missing-target-floor"
					parentID  = "missing-target-floor-parent"
				)
				if entity.entity == SyncEntityObservation {
					if err := s.CreateSession(parentID, project, "/tmp/missing-target-floor"); err != nil {
						t.Fatalf("create observation parent: %v", err)
					}
				}
				if _, err := s.db.Exec(`INSERT INTO sync_delete_tombstones (entity, entity_key, project, active) VALUES (?, ?, ?, 1)`, entity.entity, entityKey, project); err != nil {
					t.Fatalf("insert tombstone: %v", err)
				}
				if tt.otherTargetHasFloor {
					if _, err := s.db.Exec(`INSERT INTO sync_delete_tombstone_remote_floors (target_key, entity, entity_key, last_mutation_seq) VALUES ('cloud:target-a', ?, ?, 100)`, entity.entity, entityKey); err != nil {
						t.Fatalf("insert target A floor: %v", err)
					}
				}
				projectValue := project
				mutation := func(marker string, seq int64) SyncMutation {
					var payload any
					if entity.entity == SyncEntitySession {
						payload = syncSessionPayload{ID: entityKey, Project: project, Directory: "/tmp/" + marker}
					} else {
						payload = syncObservationPayload{SyncID: entityKey, SessionID: parentID, Type: "decision", Title: marker, Content: "target floor", Project: &projectValue, Scope: "project"}
					}
					raw, err := json.Marshal(payload)
					if err != nil {
						t.Fatalf("marshal %s payload: %v", entity.entity, err)
					}
					return SyncMutation{Seq: seq, Entity: entity.entity, EntityKey: entityKey, Op: SyncOpUpsert, Payload: string(raw)}
				}

				if err := s.ApplyPulledMutation("cloud:target-b", mutation("target-b", 1)); err != nil {
					t.Fatalf("apply target B upsert: %v", err)
				}
				query := `SELECT COUNT(*) FROM sessions WHERE id = ?`
				if entity.entity == SyncEntityObservation {
					query = `SELECT COUNT(*) FROM observations WHERE sync_id = ?`
				}
				if got := scalarInt(t, s, query, entityKey); (got == 1) != tt.wantTargetBUpsert {
					t.Fatalf("target B applied = %t, want %t", got == 1, tt.wantTargetBUpsert)
				}
				if !tt.wantTargetAStillBlocked {
					return
				}

				if err := s.ApplyPulledMutation("cloud:target-a", mutation("target-a-stale", 100)); err != nil {
					t.Fatalf("apply stale target A upsert: %v", err)
				}
				valueQuery := `SELECT directory FROM sessions WHERE id = ?`
				if entity.entity == SyncEntityObservation {
					valueQuery = `SELECT title FROM observations WHERE sync_id = ?`
				}
				var value string
				if err := s.db.QueryRow(valueQuery, entityKey).Scan(&value); err != nil {
					t.Fatalf("read entity after stale target A upsert: %v", err)
				}
				wantValue := "target-b"
				if entity.entity == SyncEntitySession {
					wantValue = "/tmp/target-b"
				}
				if value != wantValue {
					t.Fatalf("entity value after stale target A upsert = %q, want %q", value, wantValue)
				}
			})
		}
	}
}

func TestApplyPulledObservationNormalizesAndValidatesIdentity(t *testing.T) {
	tests := []struct {
		name, operation string
		payloadSyncID   string
		wantVisible     int
	}{
		{name: "blank upsert falls back to mutation key", operation: SyncOpUpsert, wantVisible: 1},
		{name: "blank delete falls back to mutation key", operation: SyncOpDelete, wantVisible: 0},
		{name: "mismatched upsert is quarantined", operation: SyncOpUpsert, payloadSyncID: "different-observation"},
		{name: "mismatched delete is quarantined", operation: SyncOpDelete, payloadSyncID: "different-observation", wantVisible: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			const sessionID = "identity-parent"
			const syncID = "identity-observation"
			if err := s.CreateSession(sessionID, "identity", "/tmp/identity"); err != nil {
				t.Fatal(err)
			}
			if tt.operation == SyncOpDelete {
				if _, err := s.db.Exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope) VALUES (?, ?, 'decision', 'identity', 'content', 'identity', 'project')`, syncID, sessionID); err != nil {
					t.Fatal(err)
				}
			}
			payload := fmt.Sprintf(`{"sync_id":%q,"session_id":%q,"type":"decision","title":"identity","content":"content","scope":"project"}`, tt.payloadSyncID, sessionID)
			if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{Seq: 1, Entity: SyncEntityObservation, EntityKey: syncID, Op: tt.operation, Payload: payload}); err != nil {
				t.Fatalf("apply mutation: %v", err)
			}
			if got := scalarInt(t, s, `SELECT COUNT(*) FROM observations WHERE sync_id = ? AND deleted_at IS NULL`, syncID); got != tt.wantVisible {
				t.Fatalf("visible observations = %d, want %d", got, tt.wantVisible)
			}
		})
	}
}

func TestApplyPulledMutation_CloudDeleteRecordsRemoteTombstoneFloor(t *testing.T) {
	tests := []struct {
		name, entity string
		targetKey    string
		hardDelete   bool
	}{
		{name: "session", entity: SyncEntitySession, hardDelete: true},
		{name: "observation soft", entity: SyncEntityObservation},
		{name: "observation hard", entity: SyncEntityObservation, hardDelete: true},
		{name: "observation non-default target", entity: SyncEntityObservation, targetKey: "cloud:target-delete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			key := tt.entity + "-remote-delete-" + tt.name
			project := "remote-delete"
			if err := s.CreateSession("remote-delete-parent", project, "/tmp/remote-delete"); err != nil {
				t.Fatalf("create parent session: %v", err)
			}
			if tt.entity == SyncEntitySession {
				key = "remote-delete-parent"
			} else if _, err := s.db.Exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope) VALUES (?, ?, 'decision', 'delete', 'content', ?, 'project')`, key, "remote-delete-parent", project); err != nil {
				t.Fatalf("insert observation: %v", err)
			}
			var raw []byte
			var err error
			if tt.entity == SyncEntitySession {
				raw, err = json.Marshal(syncSessionPayload{ID: key, Project: project, Deleted: true, HardDelete: true})
			} else {
				raw, err = json.Marshal(syncObservationPayload{SyncID: key, SessionID: "remote-delete-parent", Project: &project, Deleted: true, HardDelete: tt.hardDelete})
			}
			if err != nil {
				t.Fatalf("marshal delete payload: %v", err)
			}
			targetKey := tt.targetKey
			if targetKey == "" {
				targetKey = DefaultSyncTargetKey
			}
			for _, seq := range []int64{5, 7} {
				if err := s.ApplyPulledMutation(targetKey, SyncMutation{Seq: seq, Entity: tt.entity, EntityKey: key, Op: SyncOpDelete, Payload: string(raw)}); err != nil {
					t.Fatalf("apply delete seq %d: %v", seq, err)
				}
			}
			activeEntityQuery := `SELECT COUNT(*) FROM sessions WHERE id = ?`
			if tt.entity == SyncEntityObservation {
				activeEntityQuery = `SELECT COUNT(*) FROM observations WHERE sync_id = ? AND deleted_at IS NULL`
			}
			var activeEntities int
			if err := s.db.QueryRow(activeEntityQuery, key).Scan(&activeEntities); err != nil || activeEntities != 0 {
				t.Fatalf("active entities after delete = %d, err=%v", activeEntities, err)
			}
			var active int
			if err := s.db.QueryRow(`SELECT active FROM sync_delete_tombstones WHERE entity = ? AND entity_key = ?`, tt.entity, key).Scan(&active); err != nil || active != 1 {
				t.Fatalf("tombstone active=%d, want 1 (err=%v)", active, err)
			}
			if targetKey == DefaultSyncTargetKey {
				var floor int64
				if err := s.db.QueryRow(`SELECT last_remote_mutation_seq FROM sync_delete_tombstones WHERE entity = ? AND entity_key = ?`, tt.entity, key).Scan(&floor); err != nil || floor != 7 {
					t.Fatalf("default floor=%d, want 7 (err=%v)", floor, err)
				}
			} else {
				var floor int64
				if err := s.db.QueryRow(`SELECT last_mutation_seq FROM sync_delete_tombstone_remote_floors WHERE target_key = ? AND entity = ? AND entity_key = ?`, targetKey, tt.entity, key).Scan(&floor); err != nil || floor != 7 {
					t.Fatalf("target floor=%d, want 7 (err=%v)", floor, err)
				}
				var defaultFloor sql.NullInt64
				if err := s.db.QueryRow(`SELECT last_remote_mutation_seq FROM sync_delete_tombstones WHERE entity = ? AND entity_key = ?`, tt.entity, key).Scan(&defaultFloor); err != nil || defaultFloor.Valid {
					t.Fatalf("default floor=%+v, want NULL (err=%v)", defaultFloor, err)
				}
			}
		})
	}
}

func TestApplyPulledChunk_DoesNotUseCloudTombstoneFloor(t *testing.T) {
	s := newTestStore(t)
	const sessionID = "chunk-ignores-cloud-floor"
	if _, err := s.db.Exec(`INSERT INTO sync_delete_tombstones (entity, entity_key, project, active) VALUES (?, ?, 'chunk', 1)`, SyncEntitySession, sessionID); err != nil {
		t.Fatalf("insert tombstone: %v", err)
	}
	payload, err := json.Marshal(syncSessionPayload{ID: sessionID, Project: "chunk", Directory: "/tmp/chunk"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-ignores-floor", []SyncMutation{{Entity: SyncEntitySession, EntityKey: sessionID, Op: SyncOpUpsert, Payload: string(payload)}}); err != nil {
		t.Fatalf("apply chunk: %v", err)
	}
	var sessions, active int
	var floor sql.NullInt64
	if err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM sessions WHERE id = ?), active, last_remote_mutation_seq FROM sync_delete_tombstones WHERE entity = ? AND entity_key = ?`, sessionID, SyncEntitySession, sessionID).Scan(&sessions, &active, &floor); err != nil {
		t.Fatalf("read chunk result: %v", err)
	}
	if sessions != 1 || active != 0 || floor.Valid {
		t.Fatalf("chunk result sessions=%d active=%d remote_floor=%+v, want 1, 0, NULL", sessions, active, floor)
	}
}

func TestApplyPulledMutation_NonDefaultTombstoneFloorsStayIsolated(t *testing.T) {
	s := newTestStore(t)
	const sessionID = "target-floor"
	payload := fmt.Sprintf(`{"id":%q,"project":"target-floor","directory":"/tmp/target-floor"}`, sessionID)
	if _, err := s.db.Exec(`INSERT INTO sync_delete_tombstones (entity, entity_key, project, active) VALUES (?, ?, 'target-floor', 1)`, SyncEntitySession, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO sync_delete_tombstone_remote_floors (target_key, entity, entity_key, last_mutation_seq) VALUES ('cloud:target-a', ?, ?, 100), ('cloud:target-b', ?, ?, 1)`, SyncEntitySession, sessionID, SyncEntitySession, sessionID); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		target string
		seq    int64
	}{{"cloud:target-a", 99}, {"cloud:target-a", 100}, {"cloud:target-c", 1}, {"cloud:target-b", 2}} {
		if err := s.ApplyPulledMutation(mutation.target, SyncMutation{Seq: mutation.seq, Entity: SyncEntitySession, EntityKey: sessionID, Op: SyncOpUpsert, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	if got := scalarInt(t, s, `SELECT COUNT(*) FROM sessions WHERE id = ?`, sessionID); got != 1 {
		t.Fatalf("target B session rows = %d, want 1", got)
	}
	if _, err := s.CleanupForeignSyncTargets(true); err != nil {
		t.Fatal(err)
	}
	stalePayload := fmt.Sprintf(`{"id":%q,"project":"target-floor","directory":"/tmp/stale-target-a"}`, sessionID)
	if err := s.ApplyPulledMutation("cloud:target-a", SyncMutation{Seq: 100, Entity: SyncEntitySession, EntityKey: sessionID, Op: SyncOpUpsert, Payload: stalePayload}); err != nil {
		t.Fatal(err)
	}
	var directory string
	if err := s.db.QueryRow(`SELECT directory FROM sessions WHERE id = ?`, sessionID).Scan(&directory); err != nil || directory != "/tmp/target-floor" {
		t.Fatalf("directory after stale target A upsert = %q, want %q (err=%v)", directory, "/tmp/target-floor", err)
	}
	if err := s.withTx(func(tx *sql.Tx) error {
		return s.recordSyncDeleteTombstoneTx(tx, SyncEntitySession, sessionID, "", "target-floor", Now())
	}); err != nil {
		t.Fatal(err)
	}
	if got := scalarInt(t, s, `SELECT COUNT(*) FROM sync_delete_tombstone_remote_floors WHERE entity = ? AND entity_key = ?`, SyncEntitySession, sessionID); got != 0 {
		t.Fatalf("auxiliary floors after local generation = %d, want 0", got)
	}
}

func TestApplyPulledObservationStoresProjectAsText(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-pulled-project-storage", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	project := "engram"
	payload, err := json.Marshal(syncObservationPayload{
		SyncID: "obs-pulled-project-storage", SessionID: "s-pulled-project-storage", Type: "bugfix", Title: "Pulled project as text", Content: "Sync apply boundary stores text", Project: &project, Scope: "project",
	})
	if err != nil {
		t.Fatalf("marshal observation payload: %v", err)
	}
	if err := s.withTx(func(tx *sql.Tx) error {
		return s.applyPulledMutationTx(tx, SyncMutation{Entity: SyncEntityObservation, EntityKey: "obs-pulled-project-storage", Op: SyncOpUpsert, Payload: string(payload), Source: SyncSourceRemote, Project: project})
	}); err != nil {
		t.Fatalf("apply pulled observation: %v", err)
	}
	updatedPayload, err := json.Marshal(syncObservationPayload{
		SyncID: "obs-pulled-project-storage", SessionID: "s-pulled-project-storage", Type: "bugfix", Title: "Updated pulled project as text", Content: "Sync apply update stores text", Project: &project, Scope: "project",
	})
	if err != nil {
		t.Fatalf("marshal updated observation payload: %v", err)
	}
	if err := s.withTx(func(tx *sql.Tx) error {
		return s.applyPulledMutationTx(tx, SyncMutation{Entity: SyncEntityObservation, EntityKey: "obs-pulled-project-storage", Op: SyncOpUpsert, Payload: string(updatedPayload), Source: SyncSourceRemote, Project: project})
	}); err != nil {
		t.Fatalf("apply updated pulled observation: %v", err)
	}

	var title, content, storedProject, storageClass string
	if err := s.db.QueryRow(`SELECT title, content, project, typeof(project) FROM observations WHERE sync_id = ?`, "obs-pulled-project-storage").Scan(&title, &content, &storedProject, &storageClass); err != nil {
		t.Fatalf("read updated observation: %v", err)
	}
	if title != "Updated pulled project as text" || content != "Sync apply update stores text" {
		t.Fatalf("updated observation = title %q, content %q", title, content)
	}
	if storedProject != project {
		t.Fatalf("project = %q, want %q", storedProject, project)
	}
	if storageClass != "text" {
		t.Fatalf("project storage class = %q, want text", storageClass)
	}
}

func TestApplyPulledObservationDeletePreservesTombstoneVersion(t *testing.T) {
	suppliedDeletedAt := "2025-03-04 05:00:00"
	tests := []struct {
		name                          string
		payloadUpdatedAt              string
		payloadDeletedAt              *string
		wantUpdatedAt                 string
		wantDeletedAt                 string
		wantGeneratedDeletedAt        bool
		wantDeletedAtMatchesUpdatedAt bool
	}{
		{
			name:             "uses trimmed payload updated at",
			payloadUpdatedAt: " 2025-03-04 05:06:07 ",
			payloadDeletedAt: &suppliedDeletedAt,
			wantUpdatedAt:    "2025-03-04 05:06:07",
			wantDeletedAt:    suppliedDeletedAt,
		},
		{
			name:             "uses deleted at when updated at is absent",
			payloadDeletedAt: &suppliedDeletedAt,
			wantUpdatedAt:    suppliedDeletedAt,
			wantDeletedAt:    suppliedDeletedAt,
		},
		{
			name:                   "generates deleted at and preserves supplied updated at",
			payloadUpdatedAt:       " 2025-03-04 05:06:07 ",
			wantUpdatedAt:          "2025-03-04 05:06:07",
			wantGeneratedDeletedAt: true,
		},
		{
			name:                          "generates matching deleted and updated at timestamps",
			wantGeneratedDeletedAt:        true,
			wantDeletedAtMatchesUpdatedAt: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("s-pulled-delete", "engram", "/tmp/engram"); err != nil {
				t.Fatalf("create session: %v", err)
			}
			observationID, err := s.AddObservation(AddObservationParams{
				SessionID: "s-pulled-delete",
				Type:      "bugfix",
				Title:     "Pulled tombstone",
				Content:   "Sync apply preserves the replicated version",
				Project:   "engram",
				Scope:     "project",
			})
			if err != nil {
				t.Fatalf("add observation: %v", err)
			}
			observation, err := s.GetObservation(observationID)
			if err != nil {
				t.Fatalf("get observation: %v", err)
			}

			payload, err := json.Marshal(syncObservationPayload{
				SyncID:    observation.SyncID,
				UpdatedAt: tt.payloadUpdatedAt,
				DeletedAt: tt.payloadDeletedAt,
			})
			if err != nil {
				t.Fatalf("marshal delete payload: %v", err)
			}
			if err := s.withTx(func(tx *sql.Tx) error {
				return s.applyPulledMutationTx(tx, SyncMutation{
					Entity:    SyncEntityObservation,
					EntityKey: observation.SyncID,
					Op:        SyncOpDelete,
					Payload:   string(payload),
					Source:    SyncSourceRemote,
				})
			}); err != nil {
				t.Fatalf("apply pulled delete: %v", err)
			}

			var deletedAt, updatedAt string
			if err := s.db.QueryRow(`SELECT deleted_at, updated_at FROM observations WHERE id = ?`, observationID).Scan(&deletedAt, &updatedAt); err != nil {
				t.Fatalf("read tombstone timestamps: %v", err)
			}
			if tt.wantGeneratedDeletedAt {
				if deletedAt == "" {
					t.Fatal("deleted_at is empty, want generated timestamp")
				}
			} else if deletedAt != tt.wantDeletedAt {
				t.Fatalf("deleted_at = %q, want %q", deletedAt, tt.wantDeletedAt)
			}
			if tt.wantUpdatedAt != "" && updatedAt != tt.wantUpdatedAt {
				t.Fatalf("updated_at = %q, want %q", updatedAt, tt.wantUpdatedAt)
			}
			if tt.wantDeletedAtMatchesUpdatedAt && deletedAt != updatedAt {
				t.Fatalf("deleted_at = %q, want it to match updated_at = %q", deletedAt, updatedAt)
			}
		})
	}
}

// countRelationRows returns the count of rows in memory_relations with the
// given sync_id.
func countRelationRows(t *testing.T, s *Store, syncID string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM memory_relations WHERE sync_id = ?`, syncID,
	).Scan(&n); err != nil {
		t.Fatalf("countRelationRows: %v", err)
	}
	return n
}

// countDeferredRows returns the count of rows in sync_apply_deferred for the
// given sync_id.
func countDeferredRows(t *testing.T, s *Store, syncID string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM sync_apply_deferred WHERE sync_id = ?`, syncID,
	).Scan(&n); err != nil {
		t.Fatalf("countDeferredRows: %v", err)
	}
	return n
}

// setupSyncApplyStore creates a fresh store with two sessions and two
// observations suitable for relation apply tests.
func setupSyncApplyStore(t *testing.T) (s *Store, syncObsA, syncObsB string) {
	t.Helper()
	s = newTestStore(t)
	if err := s.CreateSession("ses-apply-test", "proj-apply", "/tmp/apply"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, syncObsA = addTestObsSession(t, s, "ses-apply-test", "Obs A for apply tests", "decision", "proj-apply", "project")
	_, syncObsB = addTestObsSession(t, s, "ses-apply-test", "Obs B for apply tests", "decision", "proj-apply", "project")
	return
}

func TestApplyPulledRelation_UsesOuterMutationProjectForEndpointValidation(t *testing.T) {
	const projectA = "project-a"
	const projectB = "project-b"
	const rawOuterProject = " Project__A "
	normalizedOuterProject, _ := NormalizeProject(rawOuterProject)

	tests := []struct {
		name                         string
		outerProject                 string
		payloadProject               string
		sourceProject, targetProject string
		sourceScope, targetScope     string
		wantApplied                  bool
	}{
		{
			name:          "outer project scopes blank payload project",
			outerProject:  projectA,
			sourceProject: projectB, targetProject: projectB,
			sourceScope: "project", targetScope: "project",
		},
		{
			name:           "outer project wins over conflicting payload project",
			outerProject:   projectA,
			payloadProject: projectB,
			sourceProject:  projectB, targetProject: projectB,
			sourceScope: "project", targetScope: "project",
		},
		{
			name:           "cross project endpoint does not satisfy relation",
			outerProject:   projectA,
			payloadProject: projectA,
			sourceProject:  projectA, targetProject: projectB,
			sourceScope: "project", targetScope: "project",
		},
		{
			name:           "same project personal endpoints satisfy project relation",
			outerProject:   projectA,
			payloadProject: projectA,
			sourceProject:  projectA, targetProject: projectA,
			sourceScope: "personal", targetScope: "personal",
			wantApplied: true,
		},
		{
			name: "same project global and personal endpoints satisfy project relation",
			outerProject: projectA,
			sourceProject: projectA, targetProject: projectA,
			sourceScope: "global", targetScope: "personal",
			wantApplied: true,
		},
		{
			name: "unknown endpoint project does not satisfy project relation",
			outerProject: projectA,
			sourceProject: projectB, targetProject: projectA,
			sourceScope: "global", targetScope: "personal",
		},
		{
			name:          "normalized outer project accepts project scoped endpoints",
			outerProject:  rawOuterProject,
			sourceProject: normalizedOuterProject, targetProject: normalizedOuterProject,
			sourceScope: "project", targetScope: "project",
			wantApplied: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			addEndpoint := func(name, project, scope string) string {
				sessionID := "session-" + name
				if err := s.CreateSession(sessionID, project, "/tmp/"+name); err != nil {
					t.Fatalf("CreateSession %s: %v", name, err)
				}
				_, syncID := addTestObsSession(t, s, sessionID, "Observation "+name, "decision", project, scope)
				return syncID
			}

			sourceID := addEndpoint("source", tt.sourceProject, tt.sourceScope)
			targetID := addEndpoint("target", tt.targetProject, tt.targetScope)
			if tt.name == "unknown endpoint project does not satisfy project relation" {
				if _, err := s.db.Exec(`UPDATE observations SET project = '' WHERE sync_id = ?`, sourceID); err != nil {
					t.Fatalf("clear source observation project: %v", err)
				}
				if _, err := s.db.Exec(`UPDATE sessions SET project = '' WHERE id = ?`, "session-source"); err != nil {
					t.Fatalf("clear source session project: %v", err)
				}
			}
			relationSyncID := newSyncID("rel-outer-project")
			mutation := buildRelationMutation(t, syncRelationPayload{
				SyncID:         relationSyncID,
				SourceID:       sourceID,
				TargetID:       targetID,
				Relation:       RelationRelated,
				JudgmentStatus: JudgmentStatusJudged,
				Project:        tt.payloadProject,
				CreatedAt:      "2026-04-26T10:00:00Z",
				UpdatedAt:      "2026-04-26T10:00:00Z",
			})
			mutation.Project = tt.outerProject
			mutation.Seq = 1

			if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
				t.Fatalf("ApplyPulledMutation: %v", err)
			}
			if got := countRelationRows(t, s, relationSyncID); (got == 1) != tt.wantApplied {
				t.Fatalf("applied relation rows = %d, want applied=%t", got, tt.wantApplied)
			}
			if got := countDeferredRows(t, s, relationSyncID); (got == 0) != tt.wantApplied {
				t.Fatalf("deferred relation rows = %d, want applied=%t", got, tt.wantApplied)
			}
		})
	}
}

func TestReplayDeferredRelation_PreservesOriginalOuterProjectAuthority(t *testing.T) {
	const projectA = "project-a"
	const projectB = "project-b"

	tests := []struct {
		name                 string
		outerProject         string
		payloadProject       string
		clearProvenance      bool
		enqueueBlankOuterDup bool
		wantDeferredProject  string
		wantReplayAttempt    bool
		wantApplied          bool
	}{
		{
			name:            "unmarked legacy row replays payload scoped personal endpoints",
			payloadProject:  projectA,
			clearProvenance: true,
			wantApplied:     true,
		},
		{
			name:           "authoritative outer project remains enforced after replay",
			outerProject:   projectA,
			payloadProject: projectB,
		},
		{
			name:                 "blank-project duplicate preserves authoritative replay scope",
			outerProject:         projectA,
			payloadProject:       projectB,
			enqueueBlankOuterDup: true,
			wantDeferredProject:  projectA,
			wantReplayAttempt:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			endpointProject := tt.payloadProject
			if err := s.CreateSession("session-replay", endpointProject, "/tmp/replay"); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			_, sourceID := addTestObsSession(t, s, "session-replay", "Personal source", "decision", endpointProject, "personal")
			targetID := "obs-replay-target-" + newSyncID("personal")
			relationSyncID := newSyncID("rel-replay-authority")
			mutation := buildRelationMutation(t, syncRelationPayload{
				SyncID:         relationSyncID,
				SourceID:       sourceID,
				TargetID:       targetID,
				Relation:       RelationRelated,
				JudgmentStatus: JudgmentStatusJudged,
				Project:        tt.payloadProject,
				CreatedAt:      "2026-04-26T10:00:00Z",
				UpdatedAt:      "2026-04-26T10:00:00Z",
			})
			mutation.Project = tt.outerProject
			mutation.Seq = 1

			if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
				t.Fatalf("ApplyPulledMutation: %v", err)
			}
			if got := countDeferredRows(t, s, relationSyncID); got != 1 {
				t.Fatalf("initial deferred rows = %d, want 1", got)
			}
			if tt.clearProvenance {
				if _, err := s.db.Exec(`UPDATE sync_apply_deferred SET reason_code = '' WHERE sync_id = ?`, relationSyncID); err != nil {
					t.Fatalf("clear deferred provenance: %v", err)
				}
			}
			if tt.enqueueBlankOuterDup {
				duplicate := mutation
				duplicate.Project = ""
				if err := s.EnqueueDeferredRelation(DefaultSyncTargetKey, duplicate); err != nil {
					t.Fatalf("EnqueueDeferredRelation blank-project duplicate: %v", err)
				}
			}
			if tt.wantDeferredProject != "" {
				var gotProject string
				if err := s.db.QueryRow(`SELECT project FROM sync_apply_deferred WHERE sync_id = ?`, relationSyncID).Scan(&gotProject); err != nil {
					t.Fatalf("read deferred project: %v", err)
				}
				if gotProject != tt.wantDeferredProject {
					t.Fatalf("deferred project = %q, want %q", gotProject, tt.wantDeferredProject)
				}
			}

			targetObservationID, _ := addTestObsSession(t, s, "session-replay", "Personal target", "decision", endpointProject, "personal")
			if _, err := s.db.Exec(`UPDATE observations SET sync_id = ? WHERE id = ?`, targetID, targetObservationID); err != nil {
				t.Fatalf("set target sync ID: %v", err)
			}

			result, err := s.ReplayDeferredForScope(DefaultSyncTargetKey, projectA)
			if err != nil {
				t.Fatalf("ReplayDeferredForScope: %v", err)
			}
			if tt.wantReplayAttempt && result.Retried != 1 {
				t.Fatalf("replayed deferred rows = %d, want 1 (result=%+v)", result.Retried, result)
			}
			if got := countRelationRows(t, s, relationSyncID); (got == 1) != tt.wantApplied {
				t.Fatalf("replayed relation rows = %d, want applied=%t (result=%+v)", got, tt.wantApplied, result)
			}
			if got := countDeferredRows(t, s, relationSyncID); (got == 0) != tt.wantApplied {
				t.Fatalf("replayed deferred rows = %d, want applied=%t (result=%+v)", got, tt.wantApplied, result)
			}
		})
	}
}

// ─── Phase C.3 — Pull-side RED tests (REQ-002, REQ-009) ──────────────────────

// C.3a — historical relation payloads without newer metadata still apply when
// their relation identity and endpoints are valid.
func TestApplyPulledRelation_AcceptsLegacyPayloadWithoutProvenance(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	relSyncID := newSyncID("rel")
	m := SyncMutation{
		Seq:       1,
		Entity:    SyncEntityRelation,
		EntityKey: relSyncID,
		Op:        SyncOpUpsert,
		Payload: fmt.Sprintf(`{"sync_id":%q,"source_id":%q,"target_id":%q,"relation":"conflicts_with","judgment_status":"judged","created_at":"2026-04-26T10:00:00Z","updated_at":"2026-04-26T10:00:00Z"}`,
			relSyncID, syncA, syncB),
		Source: SyncSourceRemote,
	}

	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, m); err != nil {
		t.Fatalf("ApplyPulledMutation: %v", err)
	}

	n := countRelationRows(t, s, relSyncID)
	if n != 1 {
		t.Errorf("expected 1 row in memory_relations for sync_id=%q; got %d", relSyncID, n)
	}

	// Verify the deferred table is empty for this sync_id.
	d := countDeferredRows(t, s, relSyncID)
	if d != 0 {
		t.Errorf("expected 0 deferred rows after successful apply; got %d", d)
	}
}

func TestApplyPulledRelation_AllowsSelfReference(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)

	relSyncID := newSyncID("rel-self")
	m := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       syncA,
		Relation:       RelationCompatible,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})

	if err := applyRelationMutation(t, s, m); err != nil {
		t.Fatalf("applyPulledMutationTx: %v", err)
	}
	if got := countRelationRows(t, s, relSyncID); got != 1 {
		t.Fatalf("expected 1 self-referential relation, got %d", got)
	}
}

func TestApplyPulledChunk_DefersMissingRelationAndContinues(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)
	missingTarget := "obs-ghost-" + newSyncID("x")

	validID := newSyncID("rel-valid")
	missingID := newSyncID("rel-missing")
	mutations := []SyncMutation{
		buildRelationMutation(t, syncRelationPayload{
			SyncID: missingID, SourceID: syncA, TargetID: missingTarget,
			Relation: RelationRelated, JudgmentStatus: JudgmentStatusJudged,
			Project: "proj-apply", CreatedAt: "2026-04-26T10:00:00Z", UpdatedAt: "2026-04-26T10:00:00Z",
		}),
		buildRelationMutation(t, syncRelationPayload{
			SyncID: validID, SourceID: syncA, TargetID: syncB,
			Relation: RelationCompatible, JudgmentStatus: JudgmentStatusJudged,
			Project: "proj-apply", CreatedAt: "2026-04-26T10:00:00Z", UpdatedAt: "2026-04-26T10:00:00Z",
		}),
	}

	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-local-relations", mutations); err != nil {
		t.Fatalf("ApplyPulledChunk: %v", err)
	}
	if got := countRelationRows(t, s, validID); got != 1 {
		t.Fatalf("expected valid relation to apply, got %d rows", got)
	}
	if got := countDeferredRows(t, s, missingID); got != 1 {
		t.Fatalf("expected missing relation to defer, got %d rows", got)
	}
	status, _ := getDeferredRow(t, s, missingID)
	if status != "deferred" {
		t.Fatalf("apply_status: want deferred, got %q", status)
	}
	assertDeferredScope(t, s, missingID, DefaultSyncTargetKey, "proj-apply", "scoped")
	if got := countRelationRows(t, s, missingID); got != 0 {
		t.Fatalf("expected missing relation to remain unapplied, got %d rows", got)
	}
	var lastPulled int64
	if err := s.db.QueryRow(`SELECT last_pulled_seq FROM sync_state WHERE target_key = ?`, DefaultSyncTargetKey).Scan(&lastPulled); err != nil {
		t.Fatalf("read last_pulled_seq: %v", err)
	}
	if lastPulled != int64(len(mutations)) {
		t.Fatalf("last_pulled_seq: want %d, got %d", len(mutations), lastPulled)
	}
	chunks, err := s.GetSyncedChunksForTarget(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("read synced chunks: %v", err)
	}
	if !chunks["chunk-local-relations"] {
		t.Fatal("expected chunk with deferred relation to be marked as synced")
	}
}

func TestApplyPulledRelation_DefersForeignProjectEndpointsUntilLocalEndpointsArrive(t *testing.T) {
	const projectA = "project-a"
	const projectB = "project-b"
	const sourceSyncID = "obs-shared-source"
	const targetSyncID = "obs-shared-target"

	s := newTestStore(t)
	if err := s.CreateSession("ses-project-b", projectB, "/tmp/project-b"); err != nil {
		t.Fatalf("CreateSession project B: %v", err)
	}
	foreignSourceID, _ := addTestObsSession(t, s, "ses-project-b", "Project B source", "decision", projectB, "project")
	foreignTargetID, _ := addTestObsSession(t, s, "ses-project-b", "Project B target", "decision", projectB, "project")
	if _, err := s.db.Exec(`UPDATE observations SET sync_id = ? WHERE id = ?`, sourceSyncID, foreignSourceID); err != nil {
		t.Fatalf("set project B source sync ID: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE observations SET sync_id = ? WHERE id = ?`, targetSyncID, foreignTargetID); err != nil {
		t.Fatalf("set project B target sync ID: %v", err)
	}

	relationSyncID := newSyncID("rel-project-scope")
	mutation := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relationSyncID,
		SourceID:       sourceSyncID,
		TargetID:       targetSyncID,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        projectA,
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})
	mutation.Seq = 1

	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("ApplyPulledMutation: %v", err)
	}
	if got := countRelationRows(t, s, relationSyncID); got != 0 {
		t.Fatalf("relation applied using project B endpoints: got %d rows", got)
	}
	if got := countDeferredRows(t, s, relationSyncID); got != 1 {
		t.Fatalf("deferred rows after foreign endpoint match: got %d, want 1", got)
	}
	assertDeferredScope(t, s, relationSyncID, DefaultSyncTargetKey, projectA, "scoped")

	if err := s.CreateSession("ses-project-a", projectA, "/tmp/project-a"); err != nil {
		t.Fatalf("CreateSession project A: %v", err)
	}
	localSourceID, _ := addTestObsSession(t, s, "ses-project-a", "Project A source", "decision", projectA, "project")
	localTargetID, _ := addTestObsSession(t, s, "ses-project-a", "Project A target", "decision", projectA, "project")
	if _, err := s.db.Exec(`UPDATE observations SET sync_id = ? WHERE id = ?`, sourceSyncID, localSourceID); err != nil {
		t.Fatalf("set project A source sync ID: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE observations SET sync_id = ? WHERE id = ?`, targetSyncID, localTargetID); err != nil {
		t.Fatalf("set project A target sync ID: %v", err)
	}
	// Exercise session-project fallback and keep tombstoned endpoints eligible.
	if _, err := s.db.Exec(`UPDATE observations SET project = '' WHERE id = ?`, localSourceID); err != nil {
		t.Fatalf("clear project A source observation project: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE observations SET project = '', deleted_at = datetime('now') WHERE id = ?`, localTargetID); err != nil {
		t.Fatalf("tombstone project A target observation: %v", err)
	}

	result, err := s.ReplayDeferredForScope(DefaultSyncTargetKey, projectA)
	if err != nil {
		t.Fatalf("ReplayDeferredForScope: %v", err)
	}
	if result.Retried != 1 || result.Succeeded != 1 {
		t.Fatalf("ReplayDeferredForScope result = %+v, want one successful retry", result)
	}
	if got := countRelationRows(t, s, relationSyncID); got != 1 {
		t.Fatalf("relation rows after local endpoints arrive: got %d, want 1", got)
	}
	if got := countDeferredRows(t, s, relationSyncID); got != 0 {
		t.Fatalf("deferred rows after successful replay: got %d, want 0", got)
	}
}

func TestApplyPulledRelation_DefersWhenDuplicateRowsNameOnlyOneEndpoint(t *testing.T) {
	const project = "project-duplicates"
	const sourceSyncID = "obs-duplicate-source"
	const targetSyncID = "obs-missing-target"

	for _, tt := range []struct {
		name    string
		project string
	}{
		{name: "project scoped", project: project},
		{name: "legacy blank project", project: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("ses-duplicates", project, "/tmp/duplicates"); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			firstID, _ := addTestObsSession(t, s, "ses-duplicates", "Duplicate source one", "decision", project, "project")
			secondID, _ := addTestObsSession(t, s, "ses-duplicates", "Duplicate source two", "decision", project, "project")
			for _, id := range []int64{firstID, secondID} {
				if _, err := s.db.Exec(`UPDATE observations SET sync_id = ? WHERE id = ?`, sourceSyncID, id); err != nil {
					t.Fatalf("set duplicate source sync ID: %v", err)
				}
			}

			relationSyncID := newSyncID("rel-duplicate-endpoint")
			mutation := buildRelationMutation(t, syncRelationPayload{
				SyncID:         relationSyncID,
				SourceID:       sourceSyncID,
				TargetID:       targetSyncID,
				Relation:       RelationRelated,
				JudgmentStatus: JudgmentStatusJudged,
				Project:        tt.project,
				CreatedAt:      "2026-04-26T10:00:00Z",
				UpdatedAt:      "2026-04-26T10:00:00Z",
			})
			mutation.Seq = 1

			if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
				t.Fatalf("ApplyPulledMutation: %v", err)
			}
			if got := countRelationRows(t, s, relationSyncID); got != 0 {
				t.Fatalf("relation applied with duplicate source rows and no target: got %d rows", got)
			}
			if got := countDeferredRows(t, s, relationSyncID); got != 1 {
				t.Fatalf("deferred rows with missing target: got %d, want 1", got)
			}
		})
	}
}

func TestApplyPulledRelation_NormalizesDeferredProjectForScopedReplay(t *testing.T) {
	const payloadProject = " Project__A "
	canonicalProject, _ := NormalizeProject(payloadProject)
	if canonicalProject == "" || canonicalProject == payloadProject {
		t.Fatalf("NormalizeProject(%q) = %q, want a canonical project", payloadProject, canonicalProject)
	}

	s := newTestStore(t)
	relationSyncID := newSyncID("rel-canonical-project")
	sourceSyncID := "obs-canonical-source"
	targetSyncID := "obs-canonical-target"
	mutation := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relationSyncID,
		SourceID:       sourceSyncID,
		TargetID:       targetSyncID,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        payloadProject,
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})
	mutation.Seq = 1

	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("ApplyPulledMutation: %v", err)
	}
	if got := countRelationRows(t, s, relationSyncID); got != 0 {
		t.Fatalf("relation applied before canonical-project endpoints arrived: got %d rows", got)
	}
	assertDeferredScope(t, s, relationSyncID, DefaultSyncTargetKey, canonicalProject, "scoped")

	if err := s.CreateSession("ses-canonical-project", canonicalProject, "/tmp/canonical-project"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sourceID, _ := addTestObsSession(t, s, "ses-canonical-project", "Canonical source", "decision", canonicalProject, "project")
	targetID, _ := addTestObsSession(t, s, "ses-canonical-project", "Canonical target", "decision", canonicalProject, "project")
	if _, err := s.db.Exec(`UPDATE observations SET sync_id = ? WHERE id = ?`, sourceSyncID, sourceID); err != nil {
		t.Fatalf("set source sync ID: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE observations SET sync_id = ? WHERE id = ?`, targetSyncID, targetID); err != nil {
		t.Fatalf("set target sync ID: %v", err)
	}

	result, err := s.ReplayDeferredForScope(DefaultSyncTargetKey, canonicalProject)
	if err != nil {
		t.Fatalf("ReplayDeferredForScope: %v", err)
	}
	if result.Retried != 1 || result.Succeeded != 1 {
		t.Fatalf("ReplayDeferredForScope result = %+v, want one successful retry", result)
	}
	if got := countRelationRows(t, s, relationSyncID); got != 1 {
		t.Fatalf("relation rows after canonical-project replay: got %d, want 1", got)
	}
	if got := countDeferredRows(t, s, relationSyncID); got != 0 {
		t.Fatalf("deferred rows after canonical-project replay: got %d, want 0", got)
	}
}

func TestApplyPulledMutation_DefersMissingRelationAndAdvancesCursor(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)
	relSyncID := newSyncID("rel-missing")
	m := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       "obs-ghost-" + newSyncID("x"),
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})
	m.Seq = 1

	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, m); err != nil {
		t.Fatalf("ApplyPulledMutation: %v", err)
	}
	if got := countDeferredRows(t, s, relSyncID); got != 1 {
		t.Fatalf("expected missing relation to defer, got %d rows", got)
	}
	status, _ := getDeferredRow(t, s, relSyncID)
	if status != "deferred" {
		t.Fatalf("apply_status: want deferred, got %q", status)
	}
	assertDeferredScope(t, s, relSyncID, DefaultSyncTargetKey, "proj-apply", "scoped")
	if got := countRelationRows(t, s, relSyncID); got != 0 {
		t.Fatalf("expected missing relation to remain unapplied, got %d rows", got)
	}
	var lastPulled int64
	if err := s.db.QueryRow(`SELECT last_pulled_seq FROM sync_state WHERE target_key = ?`, DefaultSyncTargetKey).Scan(&lastPulled); err != nil {
		t.Fatalf("read last_pulled_seq: %v", err)
	}
	if lastPulled != m.Seq {
		t.Fatalf("last_pulled_seq: want %d, got %d", m.Seq, lastPulled)
	}
}

func TestApplyPulledChunk_MarksMalformedRelationDeadAndContinues(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)
	validID := newSyncID("rel-valid")
	deadID := newSyncID("rel-dead")
	mutations := []SyncMutation{
		{Entity: SyncEntityRelation, EntityKey: deadID, Op: SyncOpUpsert, Payload: "not json"},
		buildRelationMutation(t, syncRelationPayload{
			SyncID: validID, SourceID: syncA, TargetID: syncB,
			Relation: RelationCompatible, JudgmentStatus: JudgmentStatusJudged,
			Project: "proj-apply", CreatedAt: "2026-04-26T10:00:00Z", UpdatedAt: "2026-04-26T10:00:00Z",
		}),
	}

	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-dead-relation", mutations); err != nil {
		t.Fatalf("ApplyPulledChunk: %v", err)
	}
	if got := countRelationRows(t, s, validID); got != 1 {
		t.Fatalf("expected valid relation to apply, got %d rows", got)
	}
	var status string
	if err := s.db.QueryRow(
		`SELECT apply_status FROM sync_apply_deferred WHERE sync_id = ?`,
		deadRelationRowKey(DefaultSyncTargetKey, mutations[0]),
	).Scan(&status); err != nil {
		t.Fatalf("read dead relation status: %v", err)
	}
	if status != "dead" {
		t.Fatalf("apply_status: want dead, got %q", status)
	}
	var lastPulled int64
	if err := s.db.QueryRow(`SELECT last_pulled_seq FROM sync_state WHERE target_key = ?`, DefaultSyncTargetKey).Scan(&lastPulled); err != nil {
		t.Fatalf("read last_pulled_seq: %v", err)
	}
	if lastPulled != int64(len(mutations)) {
		t.Fatalf("last_pulled_seq: want %d, got %d", len(mutations), lastPulled)
	}
}

func TestApplyPulledChunk_MarksInvalidRelationContractsDead(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)
	validPayload, err := json.Marshal(syncRelationPayload{
		SyncID:         "rel-invalid-contract",
		SourceID:       syncA,
		TargetID:       syncB,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-08-25T00:00:00Z",
		UpdatedAt:      "2026-08-25T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal relation payload: %v", err)
	}

	tests := []struct {
		name     string
		mutation SyncMutation
	}{
		{
			name: "unsupported operation",
			mutation: SyncMutation{
				Entity: SyncEntityRelation, EntityKey: "rel-invalid-contract", Op: SyncOpDelete, Payload: string(validPayload),
			},
		},
		{
			name: "missing endpoint",
			mutation: SyncMutation{
				Entity:    SyncEntityRelation,
				EntityKey: "rel-missing-endpoint",
				Op:        SyncOpUpsert,
				Payload:   fmt.Sprintf(`{"sync_id":"rel-missing-endpoint","source_id":%q,"relation":"related","judgment_status":"judged"}`, syncA),
			},
		},
		{
			name: "entity key does not match payload identity",
			mutation: SyncMutation{
				Entity:    SyncEntityRelation,
				EntityKey: "rel-mismatched-identity",
				Op:        SyncOpUpsert,
				Payload:   string(validPayload),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-"+tt.mutation.EntityKey, []SyncMutation{tt.mutation}); err != nil {
				t.Fatalf("ApplyPulledChunk: %v", err)
			}
			status, _ := getDeferredRow(t, s, deadRelationRowKey(DefaultSyncTargetKey, tt.mutation))
			if status != "dead" {
				t.Fatalf("apply_status: want dead, got %q", status)
			}
			if got := countRelationRows(t, s, tt.mutation.EntityKey); got != 0 {
				t.Fatalf("expected invalid relation to remain unapplied, got %d rows", got)
			}
		})
	}
}

// C.3b — ApplyPulledRelation_DefersOnFKMiss: target observation absent →
// row written to sync_apply_deferred; no halt; seq is ACK-able.
func TestApplyPulledRelation_DefersOnFKMiss(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)

	// Target does NOT exist locally.
	missingTarget := "obs-ghost-" + newSyncID("x")

	relSyncID := newSyncID("rel")
	m := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTarget,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})

	// applyPulledMutationTx must return ErrRelationFKMissing.
	err := applyRelationMutation(t, s, m)
	if err == nil {
		t.Fatal("expected ErrRelationFKMissing when target is absent; got nil")
	}
	if !errors.Is(err, ErrRelationFKMissing) {
		t.Errorf("expected ErrRelationFKMissing; got %v", err)
	}

	// Caller writes to sync_apply_deferred on ErrRelationFKMissing.
	// The test simulates the caller: write deferred row and verify.
	if _, werr := s.db.Exec(`
		INSERT INTO sync_apply_deferred (sync_id, entity, payload, apply_status, retry_count, first_seen_at)
		VALUES (?, 'relation', ?, 'deferred', 0, datetime('now'))
		ON CONFLICT(sync_id) DO UPDATE SET payload=excluded.payload, last_attempted_at=datetime('now')
	`, relSyncID, m.Payload); werr != nil {
		t.Fatalf("write deferred row: %v", werr)
	}

	d := countDeferredRows(t, s, relSyncID)
	if d != 1 {
		t.Errorf("expected 1 deferred row; got %d", d)
	}

	// Verify apply_status and retry_count.
	var applyStatus string
	var retryCount int
	if err2 := s.db.QueryRow(
		`SELECT apply_status, retry_count FROM sync_apply_deferred WHERE sync_id = ?`, relSyncID,
	).Scan(&applyStatus, &retryCount); err2 != nil {
		t.Fatalf("scan deferred row: %v", err2)
	}
	if applyStatus != "deferred" {
		t.Errorf("apply_status: want %q, got %q", "deferred", applyStatus)
	}
	if retryCount != 0 {
		t.Errorf("retry_count: want 0, got %d", retryCount)
	}

	// memory_relations must NOT have a row for this sync_id.
	r := countRelationRows(t, s, relSyncID)
	if r != 0 {
		t.Errorf("expected 0 rows in memory_relations for deferred relation; got %d", r)
	}
}

// C.3c — ApplyPulledRelation_IdempotentOnSyncID: pulling the same relation
// twice yields exactly one row (REQ-009, INSERT OR REPLACE on sync_id).
func TestApplyPulledRelation_IdempotentOnSyncID(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	relSyncID := newSyncID("rel")
	p := syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       syncB,
		Relation:       RelationCompatible,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	}
	m := buildRelationMutation(t, p)

	// First apply.
	if err := applyRelationMutation(t, s, m); err != nil {
		t.Fatalf("first applyPulledMutationTx: %v", err)
	}

	// Second apply with same sync_id.
	if err := applyRelationMutation(t, s, m); err != nil {
		t.Fatalf("second applyPulledMutationTx: %v", err)
	}

	n := countRelationRows(t, s, relSyncID)
	if n != 1 {
		t.Errorf("expected exactly 1 row after two pulls with same sync_id; got %d", n)
	}
}

// ─── Phase E store-layer helpers (REQ-007) ────────────────────────────────────

// insertDeferredRow inserts a row directly into sync_apply_deferred for test setup.
func insertDeferredRow(t *testing.T, s *Store, syncID, entity, payload string, retryCount int, applyStatus string) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred (sync_id, entity, payload, retry_count, apply_status, first_seen_at)
		VALUES (?, ?, ?, ?, ?, datetime('now'))
	`, syncID, entity, payload, retryCount, applyStatus); err != nil {
		t.Fatalf("insertDeferredRow: %v", err)
	}
}

// deadRelationRowKey returns the sync_apply_deferred key a discarded relation
// mutation is recorded under. Dead rows are keyed on the mutation's own material
// rather than on its entity_key, so tests resolve the key the same way the store
// does instead of restating the derivation.
func deadRelationRowKey(targetKey string, mutation SyncMutation) string {
	return relationApplyFailureSyncID("dead", normalizeSyncTargetKey(targetKey), mutation)
}

// getDeferredRow fetches a single deferred row's status fields.
func getDeferredRow(t *testing.T, s *Store, syncID string) (applyStatus string, retryCount int) {
	t.Helper()
	if err := s.db.QueryRow(
		`SELECT apply_status, retry_count FROM sync_apply_deferred WHERE sync_id = ?`, syncID,
	).Scan(&applyStatus, &retryCount); err != nil {
		t.Fatalf("getDeferredRow sync_id=%s: %v", syncID, err)
	}
	return applyStatus, retryCount
}

func assertDeferredScope(t *testing.T, s *Store, syncID, targetKey, project, scopeClass string) {
	t.Helper()
	var gotTargetKey, gotProject, gotScopeClass string
	if err := s.db.QueryRow(`
		SELECT target_key, project, scope_class
		FROM sync_apply_deferred
		WHERE sync_id = ?
	`, syncID).Scan(&gotTargetKey, &gotProject, &gotScopeClass); err != nil {
		t.Fatalf("read deferred scope for %q: %v", syncID, err)
	}
	if gotTargetKey != targetKey || gotProject != project || gotScopeClass != scopeClass {
		t.Fatalf("deferred scope: got target=%q project=%q class=%q, want target=%q project=%q class=%q", gotTargetKey, gotProject, gotScopeClass, targetKey, project, scopeClass)
	}
}

func insertScopedDeferredRow(t *testing.T, s *Store, syncID, payload, targetKey, project string, retryCount int) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, project, scope_class, retry_count, apply_status, first_seen_at)
		VALUES (?, 'relation', ?, ?, ?, 'scoped', ?, 'deferred', datetime('now'))
	`, syncID, payload, targetKey, project, retryCount); err != nil {
		t.Fatalf("insertScopedDeferredRow %q: %v", syncID, err)
	}
}

// TestReplayDeferred_RetrySucceeds: A deferred row; after the missing obs arrives
// and ReplayDeferred runs, the row is applied and removed.
func TestReplayDeferred_RetrySucceeds(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)
	actor := "test-actor"
	kind := "test"

	// Missing target obs.
	missingTarget := "obs-missing-" + newSyncID("x")

	relSyncID := newSyncID("rel")
	payload, _ := json.Marshal(syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTarget,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		MarkedByActor:  &actor,
		MarkedByKind:   &kind,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})

	// Insert deferred row.
	insertDeferredRow(t, s, relSyncID, SyncEntityRelation, string(payload), 0, "deferred")

	// ReplayDeferred with missing obs → still FK miss → still deferred.
	res, err := s.ReplayDeferred()
	if err != nil {
		t.Fatalf("ReplayDeferred (first): %v", err)
	}
	if res.Retried != 1 {
		t.Errorf("retried: want 1, got %d", res.Retried)
	}
	if res.Succeeded != 0 {
		t.Errorf("succeeded: want 0 (obs still missing), got %d", res.Succeeded)
	}

	// Now add the missing observation so the FK precondition is met.
	if err := s.CreateSession("ses-b", "proj-apply", "/tmp/b"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, missingTargetSync := addTestObsSession(t, s, "ses-b", "Now Present Obs", "decision", "proj-apply", "project")
	// Update the deferred row to use the now-present sync_id.
	newPayload, _ := json.Marshal(syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTargetSync,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		MarkedByActor:  &actor,
		MarkedByKind:   &kind,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})
	// Update the payload in the deferred row (simulates the obs arriving).
	if _, err := s.db.Exec(
		`UPDATE sync_apply_deferred SET payload = ?, apply_status = 'deferred' WHERE sync_id = ?`,
		string(newPayload), relSyncID,
	); err != nil {
		t.Fatalf("update deferred payload: %v", err)
	}

	// ReplayDeferred now — should succeed.
	res2, err := s.ReplayDeferred()
	if err != nil {
		t.Fatalf("ReplayDeferred (second): %v", err)
	}
	if res2.Succeeded != 1 {
		t.Errorf("succeeded: want 1, got %d", res2.Succeeded)
	}

	// Row must be gone from sync_apply_deferred.
	d := countDeferredRows(t, s, relSyncID)
	if d != 0 {
		t.Errorf("deferred row still exists after successful replay; got %d rows", d)
	}
	// Row must be in memory_relations.
	r := countRelationRows(t, s, relSyncID)
	if r != 1 {
		t.Errorf("expected 1 row in memory_relations after replay; got %d", r)
	}
}

// TestReplayDeferred_DeadAtFiveRetries: retry_count=4; FK still missing → row becomes dead.
func TestReplayDeferred_DeadAtFiveRetries(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)
	actor := "test-actor"
	kind := "test"

	missingTarget := "obs-ghost-" + newSyncID("x")
	relSyncID := newSyncID("rel-dead")
	payload, _ := json.Marshal(syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTarget,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		MarkedByActor:  &actor,
		MarkedByKind:   &kind,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})

	// Insert at retry_count=4 (one away from dead threshold of 5).
	insertDeferredRow(t, s, relSyncID, SyncEntityRelation, string(payload), 4, "deferred")

	res, err := s.ReplayDeferred()
	if err != nil {
		t.Fatalf("ReplayDeferred: %v", err)
	}
	if res.Dead != 1 {
		t.Errorf("dead: want 1, got %d", res.Dead)
	}

	applyStatus, retryCount := getDeferredRow(t, s, relSyncID)
	if applyStatus != "dead" {
		t.Errorf("apply_status: want 'dead', got %q", applyStatus)
	}
	if retryCount != 5 {
		t.Errorf("retry_count: want 5, got %d", retryCount)
	}
}

// TestRearmEligibleDeadRelationsForScope only re-arms retry-cap FK-missing
// relation rows whose validated endpoints now exist in the requested scope.
func TestRearmEligibleDeadRelationsForScope(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)
	const targetKey = DefaultSyncTargetKey
	const project = "proj-apply"
	payload := func(syncID string) string {
		raw, err := json.Marshal(syncRelationPayload{
			SyncID: syncID, SourceID: syncA, TargetID: syncB,
			Relation: RelationRelated, JudgmentStatus: JudgmentStatusJudged, Project: project,
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		return string(raw)
	}
	insertDead := func(syncID, entity, body, entityKey, op, payloadSyncID, lastErr string) {
		t.Helper()
		if _, err := s.db.Exec(`
			INSERT INTO sync_apply_deferred
				(sync_id, entity, payload, target_key, entity_key, op, payload_sync_id, project, scope_class, apply_status, retry_count, last_error, first_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'scoped', 'dead', 5, ?, datetime('now'))
		`, syncID, entity, body, targetKey, entityKey, op, payloadSyncID, project, lastErr); err != nil {
			t.Fatalf("insert dead row %q: %v", syncID, err)
		}
	}

	eligibleID := "rel-rearm-eligible"
	insertDead(eligibleID, SyncEntityRelation, payload(eligibleID), eligibleID, SyncOpUpsert, eligibleID, ErrRelationFKMissing.Error())
	insertDead("rel-rearm-malformed", SyncEntityRelation, "not-json", "rel-rearm-malformed", SyncOpUpsert, "rel-rearm-malformed", ErrRelationFKMissing.Error())
	insertDead("rel-rearm-mismatch", SyncEntityRelation, payload("rel-rearm-foreign"), "rel-rearm-mismatch", SyncOpUpsert, "rel-rearm-mismatch", ErrRelationFKMissing.Error())
	insertDead("rel-rearm-unsupported", SyncEntityRelation, payload("rel-rearm-unsupported"), "rel-rearm-unsupported", SyncOpDelete, "rel-rearm-unsupported", ErrRelationFKMissing.Error())
	insertDead("relation-dead-hash", SyncEntityRelation, payload("rel-rearm-hash-payload"), "relation-dead-hash", SyncOpUpsert, "relation-dead-hash", ErrRelationFKMissing.Error())
	insertDead("session-dead", SyncEntitySession, `{}`, "session-dead", SyncOpUpsert, "session-dead", ErrRelationFKMissing.Error())
	insertDead("rel-rearm-terminal", SyncEntityRelation, payload("rel-rearm-terminal"), "rel-rearm-terminal", SyncOpUpsert, "rel-rearm-terminal", ErrApplyDead.Error())

	rearmed, err := s.RearmEligibleDeadRelationsForScope(targetKey, project)
	if err != nil {
		t.Fatalf("RearmEligibleDeadRelationsForScope: %v", err)
	}
	if rearmed != 1 {
		t.Fatalf("rearmed = %d, want exactly the FK-missing eligible relation", rearmed)
	}
	if status, retries := getDeferredRow(t, s, eligibleID); status != "deferred" || retries != 0 {
		t.Fatalf("eligible row = (%q, %d), want (deferred, 0)", status, retries)
	}
	for _, syncID := range []string{"rel-rearm-malformed", "rel-rearm-mismatch", "rel-rearm-unsupported", "relation-dead-hash", "session-dead", "rel-rearm-terminal"} {
		if status, retries := getDeferredRow(t, s, syncID); status != "dead" || retries != 5 {
			t.Fatalf("terminal row %q = (%q, %d), want (dead, 5)", syncID, status, retries)
		}
	}
}

// TestRearmEligibleDeadRelationsForScopePreservesApplyEndpointPredicate accepts
// authoritative same-project personal endpoints, including duplicate sync IDs.
func TestRearmEligibleDeadRelationsForScopePreservesApplyEndpointPredicate(t *testing.T) {
	const targetKey = DefaultSyncTargetKey
	const project = "proj-rearm-predicate"

	s := newTestStore(t)
	if err := s.CreateSession("session-rearm-predicate", project, "/tmp/rearm-predicate"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sourceObservationID, _ := addTestObsSession(t, s, "session-rearm-predicate", "Personal source", "decision", project, "personal")
	duplicateSourceObservationID, _ := addTestObsSession(t, s, "session-rearm-predicate", "Duplicate personal source", "decision", project, "personal")
	targetObservationID, _ := addTestObsSession(t, s, "session-rearm-predicate", "Personal target", "decision", project, "personal")
	const sourceSyncID = "obs-rearm-predicate-source"
	const targetSyncID = "obs-rearm-predicate-target"
	for _, id := range []int64{sourceObservationID, duplicateSourceObservationID} {
		if _, err := s.db.Exec(`UPDATE observations SET sync_id = ? WHERE id = ?`, sourceSyncID, id); err != nil {
			t.Fatalf("set source sync ID: %v", err)
		}
	}
	if _, err := s.db.Exec(`UPDATE observations SET sync_id = ? WHERE id = ?`, targetSyncID, targetObservationID); err != nil {
		t.Fatalf("set target sync ID: %v", err)
	}

	insertDead := func(syncID, reasonCode string) {
		t.Helper()
		payload, err := json.Marshal(syncRelationPayload{
			SyncID: syncID, SourceID: sourceSyncID, TargetID: targetSyncID,
			Relation: RelationRelated, JudgmentStatus: JudgmentStatusJudged, Project: project,
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if _, err := s.db.Exec(`
			INSERT INTO sync_apply_deferred
				(sync_id, entity, payload, target_key, entity_key, op, reason_code, payload_sync_id, project, scope_class, apply_status, retry_count, last_error, first_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'scoped', 'dead', 5, ?, datetime('now'))
		`, syncID, SyncEntityRelation, string(payload), targetKey, syncID, SyncOpUpsert, reasonCode, syncID, project, ErrRelationFKMissing.Error()); err != nil {
			t.Fatalf("insert dead row %q: %v", syncID, err)
		}
	}

	const authoritativeID = "rel-rearm-authoritative-personal"
	const legacyID = "rel-rearm-legacy-duplicate"
	insertDead(authoritativeID, relationDeferredOuterProjectAuthoritativeReasonCode)
	insertDead(legacyID, "")

	rearmed, err := s.RearmEligibleDeadRelationsForScope(targetKey, project)
	if err != nil {
		t.Fatalf("RearmEligibleDeadRelationsForScope: %v", err)
	}
	if rearmed != 2 {
		t.Fatalf("rearmed = %d, want both same-project relations", rearmed)
	}
	if status, retries := getDeferredRow(t, s, authoritativeID); status != "deferred" || retries != 0 {
		t.Fatalf("authoritative personal-endpoint row = (%q, %d), want (deferred, 0)", status, retries)
	}
	if status, retries := getDeferredRow(t, s, legacyID); status != "deferred" || retries != 0 {
		t.Fatalf("legacy duplicate-endpoint row = (%q, %d), want (deferred, 0)", status, retries)
	}
	if _, err := s.ReplayDeferred(); err != nil {
		t.Fatalf("ReplayDeferred: %v", err)
	}
	for _, syncID := range []string{authoritativeID, legacyID} {
		if got := countRelationRows(t, s, syncID); got != 1 {
			t.Fatalf("replayed relation %q rows = %d, want 1", syncID, got)
		}
	}
}

// TestRearmEligibleDeadRelationsForScopeDrainsAllEligibleRows pins that one call
// re-arms every eligible satisfiable relation, not only the first query batch:
// finalizeImport invokes this once per import, so a batch limit would strand
// rows 51+ as dead until an unrelated later import.
func TestRearmEligibleDeadRelationsForScopeDrainsAllEligibleRows(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)
	const targetKey = DefaultSyncTargetKey
	const project = "proj-apply"
	const eligibleCount = 55 // deliberately above the historical batch size of 50

	for i := 0; i < eligibleCount; i++ {
		syncID := fmt.Sprintf("rel-rearm-drain-%02d", i)
		raw, err := json.Marshal(syncRelationPayload{
			SyncID: syncID, SourceID: syncA, TargetID: syncB,
			Relation: RelationRelated, JudgmentStatus: JudgmentStatusJudged, Project: project,
		})
		if err != nil {
			t.Fatalf("marshal payload %s: %v", syncID, err)
		}
		if _, err := s.db.Exec(`
			INSERT INTO sync_apply_deferred
				(sync_id, entity, payload, target_key, entity_key, op, payload_sync_id, project, scope_class, apply_status, retry_count, last_error, first_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'scoped', 'dead', 5, ?, datetime('now', '+' || ? || ' seconds'))
		`, syncID, SyncEntityRelation, string(raw), targetKey, syncID, SyncOpUpsert, syncID, project, ErrRelationFKMissing.Error(), i); err != nil {
			t.Fatalf("insert dead row %q: %v", syncID, err)
		}
	}

	rearmed, err := s.RearmEligibleDeadRelationsForScope(targetKey, project)
	if err != nil {
		t.Fatalf("RearmEligibleDeadRelationsForScope: %v", err)
	}
	if rearmed != eligibleCount {
		t.Fatalf("rearmed = %d, want %d (a single call must drain every eligible row)", rearmed, eligibleCount)
	}
	for i := 0; i < eligibleCount; i++ {
		syncID := fmt.Sprintf("rel-rearm-drain-%02d", i)
		if status, retries := getDeferredRow(t, s, syncID); status != "deferred" || retries != 0 {
			t.Fatalf("row %q = (%q, %d), want (deferred, 0)", syncID, status, retries)
		}
	}
}

// TestReplayDeferred_DeadRowSkipped: a dead row must not be retried.
func TestReplayDeferred_DeadRowSkipped(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)

	missingTarget := "obs-ghost-dead-" + newSyncID("x")
	relSyncID := newSyncID("rel-already-dead")
	payload, _ := json.Marshal(syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTarget,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})

	insertDeferredRow(t, s, relSyncID, SyncEntityRelation, string(payload), 5, "dead")

	res, err := s.ReplayDeferred()
	if err != nil {
		t.Fatalf("ReplayDeferred: %v", err)
	}
	// Dead row must not be retried at all.
	if res.Retried != 0 {
		t.Errorf("retried: want 0 (dead row skipped), got %d", res.Retried)
	}

	// Row must still be dead.
	applyStatus, _ := getDeferredRow(t, s, relSyncID)
	if applyStatus != "dead" {
		t.Errorf("apply_status changed unexpectedly; got %q", applyStatus)
	}
}

// TestCountDeferredAndDead: 3 deferred + 1 dead → counts correct.
func TestCountDeferredAndDead(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)
	missingTarget := "obs-missing-count-" + newSyncID("x")
	makePayload := func(id string) string {
		p, _ := json.Marshal(syncRelationPayload{
			SyncID:         id,
			SourceID:       syncA,
			TargetID:       missingTarget,
			Relation:       RelationRelated,
			JudgmentStatus: JudgmentStatusJudged,
			Project:        "proj-apply",
			CreatedAt:      "2026-04-26T10:00:00Z",
			UpdatedAt:      "2026-04-26T10:00:00Z",
		})
		return string(p)
	}

	insertDeferredRow(t, s, newSyncID("r1"), SyncEntityRelation, makePayload("r1"), 0, "deferred")
	insertDeferredRow(t, s, newSyncID("r2"), SyncEntityRelation, makePayload("r2"), 1, "deferred")
	insertDeferredRow(t, s, newSyncID("r3"), SyncEntityRelation, makePayload("r3"), 2, "deferred")
	insertDeferredRow(t, s, newSyncID("r4"), SyncEntityRelation, makePayload("r4"), 5, "dead")

	deferred, dead, err := s.CountDeferredAndDead()
	if err != nil {
		t.Fatalf("CountDeferredAndDead: %v", err)
	}
	if deferred != 3 {
		t.Errorf("deferred: want 3, got %d", deferred)
	}
	if dead != 1 {
		t.Errorf("dead: want 1, got %d", dead)
	}
}

func TestReplayDeferredForScope_IsolatesTargetAndProject(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)
	actor := "test-actor"
	kind := "test"
	missingPayload := func(syncID, project string) string {
		payload, err := json.Marshal(syncRelationPayload{
			SyncID: syncID, SourceID: syncA, TargetID: "obs-missing-" + syncID,
			Relation: RelationRelated, JudgmentStatus: JudgmentStatusJudged,
			MarkedByActor: &actor, MarkedByKind: &kind, Project: project,
		})
		if err != nil {
			t.Fatalf("marshal deferred payload: %v", err)
		}
		return string(payload)
	}

	cloudA := "rel-cloud-a"
	cloudB := "rel-cloud-b"
	localA := "rel-local-a"
	insertScopedDeferredRow(t, s, cloudA, missingPayload(cloudA, "project-a"), "cloud", "project-a", 4)
	insertScopedDeferredRow(t, s, cloudB, missingPayload(cloudB, "project-b"), "cloud", "project-b", 4)
	insertScopedDeferredRow(t, s, localA, missingPayload(localA, "project-a"), LocalChunkTargetKey, "project-a", 4)

	result, err := s.ReplayDeferredForScope("cloud", "project-b")
	if err != nil {
		t.Fatalf("ReplayDeferredForScope cloud project-b: %v", err)
	}
	if result.Retried != 1 || result.Dead != 1 {
		t.Fatalf("cloud project-b replay = %+v, want one dead retry", result)
	}
	statusA, retriesA := getDeferredRow(t, s, cloudA)
	if statusA != "deferred" || retriesA != 4 {
		t.Fatalf("cloud project-a row changed by project-b replay: status=%q retries=%d", statusA, retriesA)
	}
	statusLocal, retriesLocal := getDeferredRow(t, s, localA)
	if statusLocal != "deferred" || retriesLocal != 4 {
		t.Fatalf("local row changed by cloud replay: status=%q retries=%d", statusLocal, retriesLocal)
	}
	deferred, dead, err := s.CountDeferredAndDeadForScope("cloud", "project-b")
	if err != nil || deferred != 0 || dead != 1 {
		t.Fatalf("cloud project-b counts = deferred=%d dead=%d err=%v", deferred, dead, err)
	}

	result, err = s.ReplayDeferredForScope(LocalChunkTargetKey, "project-a")
	if err != nil {
		t.Fatalf("ReplayDeferredForScope local project-a: %v", err)
	}
	if result.Retried != 1 || result.Dead != 1 {
		t.Fatalf("local project-a replay = %+v, want one dead retry", result)
	}
	statusA, retriesA = getDeferredRow(t, s, cloudA)
	if statusA != "deferred" || retriesA != 4 {
		t.Fatalf("cloud row changed by local replay: status=%q retries=%d", statusA, retriesA)
	}
}

func TestListDeferredProjectsForTargetScopesAndOrders(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)
	payload := func(syncID, project string) string {
		encoded, err := json.Marshal(syncRelationPayload{
			SyncID: syncID, SourceID: syncA, TargetID: "obs-missing-" + syncID,
			Relation: RelationRelated, JudgmentStatus: JudgmentStatusJudged, Project: project,
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		return string(encoded)
	}
	insertScopedDeferredRow(t, s, "rel-project-b-1", payload("rel-project-b-1", "project-b"), "cloud", "project-b", 0)
	insertScopedDeferredRow(t, s, "rel-project-a", payload("rel-project-a", "project-a"), "cloud", "project-a", 0)
	insertScopedDeferredRow(t, s, "rel-project-b-2", payload("rel-project-b-2", "project-b"), "cloud", "project-b", 1)
	insertScopedDeferredRow(t, s, "rel-other-target", payload("rel-other-target", "project-c"), "cloud:project-c", "project-c", 0)
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, project, scope_class, apply_status, first_seen_at)
		VALUES ('rel-dead', 'relation', '{}', 'cloud', 'project-dead', 'scoped', 'dead', datetime('now'))
	`); err != nil {
		t.Fatalf("insert dead scoped deferred: %v", err)
	}
	insertDeferredRow(t, s, "rel-legacy", SyncEntityRelation, "{}", 0, "deferred")

	projects, err := s.ListDeferredProjectsForTarget("CLOUD")
	if err != nil {
		t.Fatalf("ListDeferredProjectsForTarget: %v", err)
	}
	if got, want := fmt.Sprint(projects), "[project-a project-b]"; got != want {
		t.Fatalf("projects = %s, want %s", got, want)
	}
}

// TestApplyPulledMutation_DeferredOnFKMiss: ApplyPulledMutation for relation FK miss
// writes to sync_apply_deferred and returns nil (cursor can advance).
func TestApplyPulledMutation_DeferredOnFKMiss(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)

	missingTarget := "obs-missing-apply-" + newSyncID("x")
	relSyncID := newSyncID("rel-deferred-apply")

	m := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTarget,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})
	m.Seq = 42
	m.TargetKey = DefaultSyncTargetKey

	// Ensure sync_state exists so ApplyPulledMutation can advance the cursor.
	if err := s.ensureSyncState(DefaultSyncTargetKey); err != nil {
		t.Fatalf("ensureSyncState: %v", err)
	}

	// ApplyPulledMutation must return nil (deferred internally).
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, m); err != nil {
		t.Fatalf("ApplyPulledMutation: expected nil for FK miss (deferred), got %v", err)
	}

	// Deferred row must exist.
	d := countDeferredRows(t, s, relSyncID)
	if d != 1 {
		t.Errorf("expected 1 deferred row; got %d", d)
	}

	// memory_relations must NOT have the row.
	r := countRelationRows(t, s, relSyncID)
	if r != 0 {
		t.Errorf("expected 0 rows in memory_relations for deferred; got %d", r)
	}
}

// F.1 — TestApplyPulledRelation_MalformedPayload_StraightToDead: a relation
// mutation with a malformed (or incomplete) payload must go directly to
// apply_status='dead' without retries. Decode errors are not retryable.
//
// Case (a): payload is not valid JSON.
// Case (b): payload is valid JSON but missing required source_id / target_id.
func TestApplyPulledRelation_MalformedPayload_StraightToDead(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "invalid JSON",
			payload: "not valid json",
		},
		{
			name:    "missing source_id and target_id",
			payload: `{"relation_type":"conflicts"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.ensureSyncState(DefaultSyncTargetKey); err != nil {
				t.Fatalf("ensureSyncState: %v", err)
			}

			relSyncID := newSyncID("rel-malformed")
			m := SyncMutation{
				Entity:    SyncEntityRelation,
				EntityKey: relSyncID,
				Op:        SyncOpUpsert,
				Payload:   tc.payload,
				Source:    SyncSourceRemote,
				Seq:       1,
				TargetKey: DefaultSyncTargetKey,
			}

			// ApplyPulledMutation must return nil — malformed payloads are
			// ACK-ed (cursor advances) but written as dead to sync_apply_deferred.
			if err := s.ApplyPulledMutation(DefaultSyncTargetKey, m); err != nil {
				t.Fatalf("ApplyPulledMutation: expected nil for dead payload, got %v", err)
			}

			// The row must be in sync_apply_deferred with apply_status='dead'.
			var applyStatus string
			var retryCount int
			if err := s.db.QueryRow(
				`SELECT apply_status, retry_count FROM sync_apply_deferred WHERE sync_id = ?`,
				deadRelationRowKey(DefaultSyncTargetKey, m),
			).Scan(&applyStatus, &retryCount); err != nil {
				t.Fatalf("scan deferred row: %v", err)
			}
			if applyStatus != "dead" {
				t.Errorf("apply_status: want %q, got %q", "dead", applyStatus)
			}
			if retryCount != 0 {
				t.Errorf("retry_count: want 0, got %d", retryCount)
			}

			// memory_relations must NOT have a row.
			r := countRelationRows(t, s, relSyncID)
			if r != 0 {
				t.Errorf("expected 0 rows in memory_relations for dead payload; got %d", r)
			}
		})
	}
}

// C.3d — ApplyPulledRelation_MultiActorSamePair: two mutations, same
// (source, target) pair but different sync_id → two distinct rows (REQ-009).
func TestApplyPulledRelation_MultiActorSamePair(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	relSyncID1 := newSyncID("rel")
	relSyncID2 := newSyncID("rel")

	m1 := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID1,
		SourceID:       syncA,
		TargetID:       syncB,
		Relation:       RelationConflictsWith,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})
	m2 := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID2,
		SourceID:       syncA,
		TargetID:       syncB,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:05:00Z",
		UpdatedAt:      "2026-04-26T10:05:00Z",
	})

	if err := applyRelationMutation(t, s, m1); err != nil {
		t.Fatalf("apply actor-1 mutation: %v", err)
	}
	if err := applyRelationMutation(t, s, m2); err != nil {
		t.Fatalf("apply actor-2 mutation: %v", err)
	}

	n1 := countRelationRows(t, s, relSyncID1)
	n2 := countRelationRows(t, s, relSyncID2)
	if n1 != 1 {
		t.Errorf("actor-1 sync_id: expected 1 row, got %d", n1)
	}
	if n2 != 1 {
		t.Errorf("actor-2 sync_id: expected 1 row, got %d", n2)
	}
}

// ─── EnqueueDeferredRelation — issue #1135 pre-apply skip queue ──────────────

// TestEnqueueDeferredRelationMirrorsApplyFailureRow pins the store contract of
// the pre-apply skip queue: EnqueueDeferredRelation must write exactly the row
// recordRelationApplyFailureTx writes for a deferred FK miss — same identity,
// scope, and conflict behavior — so a skipped edge and a later redelivery of
// the same edge collapse onto one replayable row, and re-enqueueing never
// resets existing retry state.
func TestEnqueueDeferredRelationMirrorsApplyFailureRow(t *testing.T) {
	newMutation := func(t *testing.T, syncID string, blankKey bool) SyncMutation {
		t.Helper()
		mut := buildRelationMutation(t, syncRelationPayload{
			SyncID:         syncID,
			SourceID:       "obs-enqueue-src",
			TargetID:       "obs-enqueue-gone",
			Relation:       RelationRelated,
			JudgmentStatus: JudgmentStatusJudged,
			Project:        "proj-enqueue",
			CreatedAt:      "2026-09-12T10:00:00Z",
			UpdatedAt:      "2026-09-12T10:00:00Z",
		})
		if blankKey {
			mut.EntityKey = ""
		}
		return mut
	}

	t.Run("writes the deferred row the apply-failure path would write", func(t *testing.T) {
		s := newTestStore(t)
		mut := newMutation(t, "rel-enqueue-1", false)
		if err := s.EnqueueDeferredRelation(DefaultSyncTargetKey, mut); err != nil {
			t.Fatalf("EnqueueDeferredRelation: %v", err)
		}
		rows, err := s.ListDeferred(ListDeferredOptions{Status: "deferred"})
		if err != nil {
			t.Fatalf("list deferred rows: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("expected exactly one deferred row, got %+v", rows)
		}
		row := rows[0]
		if row.SyncID != "rel-enqueue-1" || row.Entity != SyncEntityRelation || row.Op != SyncOpUpsert {
			t.Fatalf("unexpected row identity: %+v", row)
		}
		if row.TargetKey != DefaultSyncTargetKey || row.Project != "proj-enqueue" || row.ScopeClass != "scoped" {
			t.Fatalf("unexpected row scope: %+v", row)
		}
		if row.EntityKey != "rel-enqueue-1" || row.RetryCount != 0 {
			t.Fatalf("unexpected row state: %+v", row)
		}
		var payloadSyncID string
		if err := s.db.QueryRow(`SELECT payload_sync_id FROM sync_apply_deferred WHERE sync_id = ?`, "rel-enqueue-1").Scan(&payloadSyncID); err != nil {
			t.Fatalf("read payload_sync_id: %v", err)
		}
		if payloadSyncID != "rel-enqueue-1" {
			t.Fatalf("payload_sync_id = %q, want rel-enqueue-1", payloadSyncID)
		}
	})

	t.Run("collapses onto the apply-failure row and preserves retry state", func(t *testing.T) {
		s := newTestStore(t)
		mut := newMutation(t, "rel-enqueue-2", false)
		if err := s.EnqueueDeferredRelation(DefaultSyncTargetKey, mut); err != nil {
			t.Fatalf("first enqueue: %v", err)
		}
		// A replay against the missing endpoint is the failure path the row
		// must stay compatible with: it bumps retry_count and keeps 'deferred'.
		replay, err := s.ReplayDeferred()
		if err != nil {
			t.Fatalf("replay against missing endpoint: %v", err)
		}
		if replay.Failed != 1 {
			t.Fatalf("expected one still-deferred replay failure, got %+v", replay)
		}
		// Re-enqueueing the same edge must collapse onto the same row without
		// resetting its retry state.
		if err := s.EnqueueDeferredRelation(DefaultSyncTargetKey, mut); err != nil {
			t.Fatalf("second enqueue: %v", err)
		}
		rows, err := s.ListDeferred(ListDeferredOptions{Status: "deferred"})
		if err != nil {
			t.Fatalf("list deferred rows: %v", err)
		}
		if len(rows) != 1 || rows[0].SyncID != "rel-enqueue-2" || rows[0].RetryCount != 1 {
			t.Fatalf("re-enqueue reset or duplicated the retry row: %+v", rows)
		}
	})

	t.Run("rejects non-relation mutations", func(t *testing.T) {
		s := newTestStore(t)
		err := s.EnqueueDeferredRelation(DefaultSyncTargetKey, SyncMutation{
			Entity: SyncEntitySession, EntityKey: "sess-enqueue", Op: SyncOpUpsert, Payload: `{"id":"sess-enqueue"}`,
		})
		if err == nil {
			t.Fatal("expected non-relation mutations to be rejected")
		}
	})

	t.Run("blank entity key derives the deterministic hashed identity", func(t *testing.T) {
		s := newTestStore(t)
		mut := newMutation(t, "rel-enqueue-3", true)
		if err := s.EnqueueDeferredRelation(DefaultSyncTargetKey, mut); err != nil {
			t.Fatalf("enqueue with blank entity key: %v", err)
		}
		wantSyncID := relationApplyFailureSyncID("deferred", DefaultSyncTargetKey, mut)
		if n := countDeferredRows(t, s, wantSyncID); n != 1 {
			t.Fatalf("expected the hashed identity row %q to exist, found %d", wantSyncID, n)
		}
	})
}

// TestEnqueueDeferredRelationReArmsDeadRow pins the recovery contract for a
// pre-apply skipped relation that expired at the replay retry cap: only an
// explicit re-enqueue may re-arm the dead row — to 'deferred' with
// retry_count 0 — while ordinary apply redelivery and hash-keyed dead
// evidence must stay dead.
func TestEnqueueDeferredRelationReArmsDeadRow(t *testing.T) {
	newMutation := func(t *testing.T, syncID string) SyncMutation {
		t.Helper()
		return buildRelationMutation(t, syncRelationPayload{
			SyncID: syncID, SourceID: "obs-enqueue-rearm-src", TargetID: "obs-enqueue-rearm-gone",
			Relation: RelationRelated, JudgmentStatus: JudgmentStatusJudged,
			Project: "proj-enqueue-rearm", CreatedAt: "2026-09-12T10:00:00Z", UpdatedAt: "2026-09-12T10:00:00Z",
		})
	}

	// enqueueAndDriveToDead enqueues the relation as pre-apply skip evidence
	// and drives its row to 'dead' through real ReplayDeferredForScope
	// retries: four still-deferred FK misses, the fifth hits the cap of 5.
	enqueueAndDriveToDead := func(t *testing.T, s *Store, mut SyncMutation) {
		t.Helper()
		if err := s.EnqueueDeferredRelation(DefaultSyncTargetKey, mut); err != nil {
			t.Fatalf("EnqueueDeferredRelation: %v", err)
		}
		for i := 1; i <= 5; i++ {
			res, err := s.ReplayDeferredForScope(DefaultSyncTargetKey, "proj-enqueue-rearm")
			if err != nil {
				t.Fatalf("ReplayDeferredForScope (retry %d): %v", i, err)
			}
			if (i < 5 && (res.Failed != 1 || res.Dead != 0)) || (i == 5 && (res.Dead != 1 || res.Failed != 0)) {
				t.Fatalf("replay %d: got %+v, want one deferred retry per replay, dead only at the cap", i, res)
			}
		}
		if status, retryCount := getDeferredRow(t, s, mut.EntityKey); status != "dead" || retryCount != 5 {
			t.Fatalf("pre-re-arm row = (%q, %d), want (dead, 5)", status, retryCount)
		}
	}

	readDiagnostics := func(t *testing.T, s *Store, syncID string) (firstSeen, lastErr, lastAttempted string) {
		t.Helper()
		if err := s.db.QueryRow(
			`SELECT first_seen_at, ifnull(last_error, ''), ifnull(last_attempted_at, '') FROM sync_apply_deferred WHERE sync_id = ?`, syncID,
		).Scan(&firstSeen, &lastErr, &lastAttempted); err != nil {
			t.Fatalf("read row diagnostics for %s: %v", syncID, err)
		}
		return firstSeen, lastErr, lastAttempted
	}

	t.Run("re-enqueue re-arms a retry-cap dead row with a fresh retry window", func(t *testing.T) {
		s, _, _ := setupSyncApplyStore(t)
		mut := newMutation(t, newSyncID("rel-rearm"))
		enqueueAndDriveToDead(t, s, mut)
		deadFirstSeen, deadLastError, _ := readDiagnostics(t, s, mut.EntityKey)

		// Freeze last_attempted_at to a sentinel: the replays above already left
		// it non-empty, so only a fresh, well-formed timestamp written by the
		// re-enqueue itself proves the bump.
		const sentinel = "2000-01-01 00:00:00"
		sentinelAt := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		if _, err := s.db.Exec(`UPDATE sync_apply_deferred SET last_attempted_at = ? WHERE sync_id = ?`, sentinel, mut.EntityKey); err != nil {
			t.Fatalf("freeze last_attempted_at: %v", err)
		}

		if err := s.EnqueueDeferredRelation(DefaultSyncTargetKey, mut); err != nil {
			t.Fatalf("re-enqueue: %v", err)
		}
		deferred, dead, err := s.CountDeferredAndDeadForScope(DefaultSyncTargetKey, "proj-enqueue-rearm")
		if err != nil {
			t.Fatalf("CountDeferredAndDeadForScope: %v", err)
		}
		if deferred != 1 || dead != 0 {
			t.Fatalf("after re-enqueue: deferred=%d dead=%d, want one re-armed deferred row and no dead row", deferred, dead)
		}
		rows, err := s.ListDeferred(ListDeferredOptions{Status: "deferred"})
		if err != nil {
			t.Fatalf("list deferred rows: %v", err)
		}
		if len(rows) != 1 || rows[0].SyncID != mut.EntityKey || rows[0].RetryCount != 0 {
			t.Fatalf("re-armed row = %+v, want the same relation with retry_count 0", rows)
		}
		firstSeen, lastErr, lastAttempted := readDiagnostics(t, s, mut.EntityKey)
		bumpedAt, parseErr := time.Parse("2006-01-02 15:04:05", lastAttempted)
		if firstSeen != deadFirstSeen || lastErr != deadLastError || parseErr != nil || !bumpedAt.After(sentinelAt) {
			t.Fatalf("re-armed diagnostics = (%q, %q, %q), want first_seen_at=%q and last_error=%q preserved and last_attempted_at a fresh timestamp after %q", firstSeen, lastErr, lastAttempted, deadFirstSeen, deadLastError, sentinel)
		}
	})

	t.Run("first replay after re-arm becomes retry 1 and stays deferred", func(t *testing.T) {
		s, _, _ := setupSyncApplyStore(t)
		mut := newMutation(t, newSyncID("rel-rearm-retry1"))
		enqueueAndDriveToDead(t, s, mut)
		if err := s.EnqueueDeferredRelation(DefaultSyncTargetKey, mut); err != nil {
			t.Fatalf("re-enqueue: %v", err)
		}
		res, err := s.ReplayDeferredForScope(DefaultSyncTargetKey, "proj-enqueue-rearm")
		if err != nil {
			t.Fatalf("replay after re-arm: %v", err)
		}
		if res.Retried != 1 || res.Failed != 1 || res.Dead != 0 {
			t.Fatalf("replay after re-arm = %+v, want exactly one still-deferred retry", res)
		}
		if status, retryCount := getDeferredRow(t, s, mut.EntityKey); status != "deferred" || retryCount != 1 {
			t.Fatalf("row after replay = (%q, %d), want (deferred, 1)", status, retryCount)
		}
	})

	t.Run("ordinary apply redelivery leaves the retry-cap dead row dead", func(t *testing.T) {
		s, _, _ := setupSyncApplyStore(t)
		mut := newMutation(t, newSyncID("rel-rearm-redelivery"))
		enqueueAndDriveToDead(t, s, mut)
		if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-rearm-redelivery", []SyncMutation{mut}); err != nil {
			t.Fatalf("ApplyPulledChunk: %v", err)
		}
		if status, retryCount := getDeferredRow(t, s, mut.EntityKey); status != "dead" || retryCount != 5 {
			t.Fatalf("redelivered row = (%q, %d), want the dead row untouched at (dead, 5)", status, retryCount)
		}
		if got := countRelationRows(t, s, mut.EntityKey); got != 0 {
			t.Fatalf("dead relation must stay unapplied, got %d rows", got)
		}
	})

	t.Run("re-enqueue does not re-arm a dead row when entity key and payload sync_id disagree", func(t *testing.T) {
		s, _, _ := setupSyncApplyStore(t)
		mut := newMutation(t, newSyncID("rel-rearm-mismatch"))
		enqueueAndDriveToDead(t, s, mut)
		type deferredState struct {
			payload, payloadSyncID, entityKey, targetKey, project, scopeClass, applyStatus string
			retryCount                                                                     int
		}
		readState := func() deferredState {
			var state deferredState
			if err := s.db.QueryRow(`
				SELECT payload, payload_sync_id, entity_key, target_key, project, scope_class, apply_status, retry_count
				FROM sync_apply_deferred WHERE sync_id = ?
			`, mut.EntityKey).Scan(
				&state.payload, &state.payloadSyncID, &state.entityKey, &state.targetKey,
				&state.project, &state.scopeClass, &state.applyStatus, &state.retryCount,
			); err != nil {
				t.Fatalf("read dead row before foreign re-enqueue: %v", err)
			}
			return state
		}
		original := readState()

		// The dead row is keyed by the relation's own sync_id. A delivery that
		// claims that key while its payload encodes a different relation is not
		// the expired retry state's own edge, so it must not resurrect or mutate
		// the row. Its distinct scope exposes accidental metadata overwrites too.
		foreign := buildRelationMutation(t, syncRelationPayload{
			SyncID: newSyncID("rel-rearm-foreign"), SourceID: "obs-foreign-src", TargetID: "obs-foreign-gone",
			Relation: RelationRelated, JudgmentStatus: JudgmentStatusJudged,
			CreatedAt: "2026-09-12T10:00:00Z", UpdatedAt: "2026-09-12T10:00:00Z",
		})
		foreign.EntityKey = mut.EntityKey
		if err := s.EnqueueDeferredRelation("cloud:foreign", foreign); err != nil {
			t.Fatalf("re-enqueue with disagreeing payload sync_id: %v", err)
		}
		if got := readState(); got != original {
			t.Fatalf("foreign re-enqueue overwrote dead row: got %+v, want %+v", got, original)
		}
		deferred, dead, err := s.CountDeferredAndDeadForScope(DefaultSyncTargetKey, "proj-enqueue-rearm")
		if err != nil {
			t.Fatalf("CountDeferredAndDeadForScope: %v", err)
		}
		if deferred != 0 || dead != 1 {
			t.Fatalf("after disagreeing re-enqueue: deferred=%d dead=%d, want no re-armed deferred row", deferred, dead)
		}
	})

	t.Run("re-enqueue does not resurrect hash-keyed dead evidence", func(t *testing.T) {
		s, _, _ := setupSyncApplyStore(t)
		mut := newMutation(t, newSyncID("rel-rearm-hash"))
		mut.EntityKey = ""
		mut.Payload = "not json" // blank key: hashed identity; payload cannot apply
		// The skip queue writes the row as deferred under the hashed identity; an
		// ordinary apply of the same mutation records dead evidence under the same
		// hash and wins the conflict.
		if err := s.EnqueueDeferredRelation(DefaultSyncTargetKey, mut); err != nil {
			t.Fatalf("EnqueueDeferredRelation (blank key): %v", err)
		}
		if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-rearm-hash", []SyncMutation{mut}); err != nil {
			t.Fatalf("ApplyPulledChunk: %v", err)
		}
		deadSyncID := relationApplyFailureSyncID("dead", DefaultSyncTargetKey, mut)
		if status, _ := getDeferredRow(t, s, deadSyncID); status != "dead" {
			t.Fatalf("hash-keyed row = %q, want dead evidence before re-enqueue", status)
		}
		if err := s.EnqueueDeferredRelation(DefaultSyncTargetKey, mut); err != nil {
			t.Fatalf("re-enqueue over hash-keyed dead evidence: %v", err)
		}
		if status, _ := getDeferredRow(t, s, deadSyncID); status != "dead" {
			t.Fatalf("hash-keyed dead evidence = %q after re-enqueue, want dead", status)
		}
		if got := countDeferredRows(t, s, deadSyncID); got != 1 {
			t.Fatalf("hash-keyed dead row duplicated or lost: %d rows", got)
		}
	})
}
