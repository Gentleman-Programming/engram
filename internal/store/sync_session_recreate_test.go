package store

import (
	"fmt"
	"strings"
	"testing"
)

func recreateSessionMutation(op, extra string) SyncMutation {
	return SyncMutation{Entity: SyncEntitySession, EntityKey: "S1", Op: op,
		Payload: fmt.Sprintf(`{"id":"S1","project":"p","directory":"/p","started_at":"2099-01-01 00:00:00"%s}`, extra)}
}

// These callers must retain ordinary delete effects and atomic failure, even
// though chunk import can now preserve a session during an intermediate delete.
func TestSessionRecreateDeleteBoundaries(t *testing.T) {
	upsert := recreateSessionMutation(SyncOpUpsert, "")
	remove := recreateSessionMutation(SyncOpDelete, `,"deleted_at":"2026-01-01 00:00:00"`)
	for _, tc := range []struct {
		name                              string
		mutations                         []SyncMutation
		perMutation, child, fail, survives bool
	}{
		{name: "ordinary chunk delete", mutations: []SyncMutation{remove}},
		{name: "per mutation delete", mutations: []SyncMutation{remove}, perMutation: true},
		{name: "ordinary restrictive child", mutations: []SyncMutation{remove}, child: true, fail: true, survives: true},
		{name: "per mutation restrictive child", mutations: []SyncMutation{remove}, perMutation: true, child: true, fail: true, survives: true},
		{name: "repeated recreation final delete", mutations: []SyncMutation{remove, upsert, remove, upsert, remove}},
		{name: "final delete remains restrictive", mutations: []SyncMutation{remove, upsert, remove}, child: true, fail: true, survives: true},
		{name: "deleted flag is not resurrection", mutations: []SyncMutation{remove, recreateSessionMutation(SyncOpUpsert, `,"deleted":true`)}, child: true, fail: true, survives: true},
		{name: "deleted timestamp is not resurrection", mutations: []SyncMutation{remove, recreateSessionMutation(SyncOpUpsert, `,"deleted_at":"2026-01-01 00:00:00"`)}, child: true, fail: true, survives: true},
		{name: "hard delete is not resurrection", mutations: []SyncMutation{remove, recreateSessionMutation(SyncOpUpsert, `,"hard_delete":true`)}, child: true, fail: true, survives: true},
		{name: "invalid future payload", mutations: []SyncMutation{remove, {Entity: SyncEntitySession, EntityKey: "S1", Op: SyncOpUpsert, Payload: `{`}}, fail: true, survives: true},
		{name: "invalid future directory", mutations: []SyncMutation{remove, {Entity: SyncEntitySession, EntityKey: "S1", Op: SyncOpUpsert, Payload: `{"id":"S1","project":"p","directory":null}`}}, fail: true, survives: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			const target = "cloud:p"
			if err := s.ApplyPulledChunkForDomain(target, "seed", []SyncMutation{upsert}, true); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddPrompt(AddPromptParams{SessionID: "S1", Project: "p", Content: "keep unless final delete"}); err != nil {
				t.Fatal(err)
			}
			if tc.child {
				if _, err := s.AddObservation(AddObservationParams{SessionID: "S1", Project: "p", Type: "note", Title: "local", Content: "local child"}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.GetSyncState(target)
			if err != nil {
				t.Fatal(err)
			}
			if tc.perMutation {
				mutation := tc.mutations[0]
				mutation.Seq = before.LastPulledSeq + 1
				err = s.ApplyPulledMutation(target, mutation)
			} else {
				err = s.ApplyPulledChunkForDomain(target, "candidate", tc.mutations, true)
			}
			if (err != nil) != tc.fail {
				t.Fatalf("apply error = %v, want failure %v", err, tc.fail)
			}
			if tc.child && tc.fail && !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
				t.Fatalf("ordinary delete lost FK error: %v", err)
			}
			_, sessionErr := s.GetSession("S1")
			if (sessionErr == nil) != tc.survives {
				t.Fatalf("session error = %v, want survives %v", sessionErr, tc.survives)
			}
			prompts, err := s.RecentPrompts("p", 10)
			if err != nil || (len(prompts) == 1) != tc.survives {
				t.Fatalf("prompts = %+v, %v", prompts, err)
			}
			chunks, err := s.GetSyncedChunksForTarget(target)
			if err != nil || chunks["candidate"] != (!tc.fail && !tc.perMutation) {
				t.Fatalf("chunk accounting = %v, %v", chunks, err)
			}
			if tc.fail {
				after, err := s.GetSyncState(target)
				if err != nil || after.LastPulledSeq != before.LastPulledSeq {
					t.Fatalf("failed apply advanced cursor: %+v, %v", after, err)
				}
			}
		})
	}
}

// Invalid identity upserts are quarantined at their own mutation position,
// not promoted to chunk failures by the delete's resurrection lookahead.
func TestSessionRecreateInvalidFutureIdentityQuarantines(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		for _, recreate := range []bool{false, true} {
			t.Run(fmt.Sprintf("cloud=%v/recreate=%v", cloud, recreate), func(t *testing.T) {
				s := newTestStore(t)
				const target = "cloud:p"
				upsert := recreateSessionMutation(SyncOpUpsert, "")
				if err := s.ApplyPulledChunkForDomain(target, "seed", []SyncMutation{upsert}, cloud); err != nil {
					t.Fatal(err)
				}
				if _, err := s.AddPrompt(AddPromptParams{SessionID: "S1", Project: "p", Content: "local prompt"}); err != nil {
					t.Fatal(err)
				}
				mutations := []SyncMutation{
					recreateSessionMutation(SyncOpDelete, `,"deleted_at":"2026-01-01 00:00:00"`),
					{Entity: SyncEntitySession, EntityKey: "S1", Op: SyncOpUpsert, Payload: `{"id":"other"}`},
				}
				if recreate {
					mutations = append(mutations, upsert)
				}
				if err := s.ApplyPulledChunkForDomain(target, "candidate", mutations, cloud); err != nil {
					t.Fatalf("identity quarantine must not abort chunk: %v", err)
				}
				_, err := s.GetSession("S1")
				if (err == nil) != recreate {
					t.Fatalf("session error = %v, want survives %v", err, recreate)
				}
				prompts, err := s.RecentPrompts("p", 10)
				if err != nil || len(prompts) != map[bool]int{false: 0, true: 1}[recreate] {
					t.Fatalf("prompts = %+v, %v", prompts, err)
				}
				state, err := s.GetSyncState(target)
				if err != nil || state.LastPulledSeq != int64(1+len(mutations)) {
					t.Fatalf("cursor = %+v, %v", state, err)
				}
				chunks, err := s.GetSyncedChunksForTarget(target)
				if err != nil || !chunks["candidate"] {
					t.Fatalf("chunks = %v, %v", chunks, err)
				}
				deferred, dead, err := s.CountDeferredAndDeadForScope(target, "")
				if err != nil || deferred != 0 || dead != 1 {
					t.Fatalf("quarantine deferred=%d dead=%d, %v", deferred, dead, err)
				}
				if !recreate {
					tombstones, err := s.ExportLocalDeleteTombstones("p")
					if err != nil || len(tombstones) != 1 || tombstones[0].Entity != SyncEntitySession || tombstones[0].EntityKey != "S1" {
						t.Fatalf("tombstones = %+v, %v", tombstones, err)
					}
				}
			})
		}
	}
}

func TestSessionRecreateRespectsTombstoneAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, generation string
		cloud, survives  bool
	}{
		{"older", "2026-02-01", false, false},
		{"equal", "2026-02-02", false, false},
		{"newer", "2026-02-03", false, true},
		{"normalized equal", "2026-02-02T00:00:00Z", false, false},
		{"cloud older", "2026-02-01", true, true},
		{"stale then newer", "2026-02-01", false, true},
		{"missing generation", "", false, true},
		{"missing delete date", "2026-02-01", false, false},
		{"repeated delete", "2026-02-03", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			const target = "cloud:p"
			mutation := func(date, extra string) SyncMutation {
				return SyncMutation{Entity: SyncEntitySession, EntityKey: "S1", Op: SyncOpUpsert,
					Payload: fmt.Sprintf(`{"id":"S1","project":"p","directory":"/p","started_at":%q,"summary":%q}`, date, extra)}
			}
			if err := s.ApplyPulledChunkForDomain(target, "seed", []SyncMutation{mutation("2026-01-01", "original")}, tc.cloud); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddPrompt(AddPromptParams{SessionID: "S1", Project: "p", Content: "P1"}); err != nil {
				t.Fatal(err)
			}
			mutations := []SyncMutation{recreateSessionMutation(SyncOpDelete, `,"deleted_at":"2026-02-02"`), mutation(tc.generation, "incoming")}
			if tc.name == "missing delete date" {
				mutations[0] = recreateSessionMutation(SyncOpDelete, "")
			}
			if tc.name == "repeated delete" {
				mutations = append([]SyncMutation{mutations[0]}, mutations...)
			}
			if tc.name == "stale then newer" {
				mutations = append(mutations, mutation("2026-02-03", "incoming"))
			}
			if err := s.ApplyPulledChunkForDomain(target, "candidate", mutations, tc.cloud); err != nil {
				t.Fatal(err)
			}
			session, err := s.GetSession("S1")
			if (err == nil) != tc.survives {
				t.Fatalf("session = %+v, %v; want survives %v", session, err, tc.survives)
			}
			wantStarted := tc.generation
			if tc.name == "stale then newer" {
				wantStarted = "2026-02-03"
			}
			if tc.survives && (session.Summary == nil || *session.Summary != "incoming" || (wantStarted != "" && session.StartedAt != wantStarted) || session.StartedAt == "" || session.Directory != "/p" || session.Project != "p") {
				t.Fatalf("incoming session not applied: %+v", session)
			}
			prompts, err := s.RecentPrompts("p", 10)
			if err != nil || (len(prompts) == 1) != tc.survives {
				t.Fatalf("prompts = %+v, %v", prompts, err)
			}
			tombstones, err := s.ExportLocalDeleteTombstones("p")
			if err != nil || (len(tombstones) == 0) != tc.survives {
				t.Fatalf("active tombstones = %+v, %v", tombstones, err)
			}
			state, err := s.GetSyncState(target)
			if err != nil || state.LastPulledSeq != int64(1+len(mutations)) {
				t.Fatalf("state = %+v, %v", state, err)
			}
			chunks, err := s.GetSyncedChunksForTarget(target)
			if err != nil || !chunks["candidate"] {
				t.Fatalf("chunks = %v, %v", chunks, err)
			}
		})
	}
}

func TestSessionRecreatePreservesChildrenExceptExplicitDeletes(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		t.Run(fmt.Sprintf("cloud=%v", cloud), func(t *testing.T) {
			s := newTestStore(t)
			upsert := recreateSessionMutation(SyncOpUpsert, "")
			remove := recreateSessionMutation(SyncOpDelete, `,"deleted_at":"2026-01-01 00:00:00"`)
			const target = "cloud:p"
			if err := s.ApplyPulledChunkForDomain(target, "seed", []SyncMutation{upsert}, cloud); err != nil {
				t.Fatal(err)
			}
			id, err := s.AddObservation(AddObservationParams{SessionID: "S1", Project: "p", Type: "note", Title: "explicit", Content: "explicit delete"})
			if err != nil {
				t.Fatal(err)
			}
			obs, err := s.GetObservation(id)
			if err != nil {
				t.Fatal(err)
			}
			childDelete := SyncMutation{Entity: SyncEntityObservation, Op: SyncOpDelete, EntityKey: obs.SyncID,
				Payload: fmt.Sprintf(`{"sync_id":%q,"hard_delete":true}`, obs.SyncID)}
			if err := s.ApplyPulledChunkForDomain(target, "recreate", []SyncMutation{remove, upsert, childDelete}, cloud); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetSession("S1"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetObservationBySyncID(obs.SyncID); err == nil {
				t.Fatal("explicit child delete was ignored")
			}
		})
	}
}
