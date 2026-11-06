package sync

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestBuildGitExportChunkPromptHistory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		include bool
		history map[string]struct{}
		want    []string
	}{
		{"default privacy", false, nil, nil},
		{"late opt in", true, nil, []string{"old-unshared", "new-shared"}},
		{"history wins over timestamp", true, map[string]struct{}{"new-shared": {}}, []string{"old-unshared"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := &store.ExportData{
				Sessions: []store.Session{{ID: "parent", StartedAt: "2025-01-01"}},
				Prompts: []store.Prompt{
					{SyncID: "old-unshared", SessionID: "parent", CreatedAt: "2025-01-01"},
					{SyncID: "new-shared", SessionID: "parent", CreatedAt: "2025-01-03"},
				},
			}
			before, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			inputs := gitExportInputs{lastChunkTime: "2025-01-02", includePrompts: tc.include, historicalPrompts: tc.history}
			chunk, err := buildGitExportChunk(data, inputs)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, prompt := range chunk.Prompts {
				got = append(got, prompt.SyncID)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("prompts=%v want=%v", got, tc.want)
			}
			if len(got) > 0 && (len(chunk.Sessions) != 1 || chunk.Sessions[0].ID != "parent") {
				t.Fatalf("missing parent closure: %+v", chunk.Sessions)
			}
			after, err := json.Marshal(data)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("input changed: %v", err)
			}
			repeated, err := buildGitExportChunk(data, inputs)
			if err != nil || !reflect.DeepEqual(chunk, repeated) {
				t.Fatalf("non-deterministic build: %v", err)
			}
		})
	}
}

func TestBuildGitExportChunkReviewRestoration(t *testing.T) {
	date := "2025-02-01"
	for _, tc := range []struct {
		name  string
		prior map[string]historicalReviewState
		want  int
	}{
		{"unknown", nil, 0},
		{"same date", map[string]historicalReviewState{"obs": {date: &date}}, 0},
		{"tombstone", map[string]historicalReviewState{"obs": {deleted: true}}, 0},
		{"authoritative null", map[string]historicalReviewState{"obs": {}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := &store.ExportData{
				Sessions:     []store.Session{{ID: "parent", StartedAt: "2025-01-01"}},
				Observations: []store.Observation{{SyncID: "obs", SessionID: "parent", CreatedAt: "2025-01-02", UpdatedAt: "2025-01-02", ReviewAfter: &date}},
			}
			chunk, err := buildGitExportChunk(data, gitExportInputs{
				lastChunkTime: "2025-01-02", historicalObservations: map[string]struct{}{"obs": {}}, reviewDates: tc.prior,
			})
			if err != nil || len(chunk.Observations) != tc.want || len(chunk.Sessions) != tc.want {
				t.Fatalf("chunk=%+v err=%v want=%d", chunk, err, tc.want)
			}
		})
	}
}

func TestBuildGitExportChunkClearAndMutationOrder(t *testing.T) {
	data := &store.ExportData{Observations: []store.Observation{{SyncID: "obs", SessionID: "parent", CreatedAt: "2025-01-01", UpdatedAt: "2025-01-01"}}}
	relation := store.SyncMutation{Entity: store.SyncEntityRelation, EntityKey: "edge", Op: store.SyncOpDelete, Payload: `{}`}
	deletion := store.SyncMutation{Entity: store.SyncEntityObservation, EntityKey: "gone", Op: store.SyncOpDelete, Payload: `{}`}
	inputs := gitExportInputs{
		lastChunkTime: "2025-01-02", historicalObservations: map[string]struct{}{"obs": {}},
		relationMutations: []store.SyncMutation{relation}, localDeletes: []store.SyncMutation{deletion},
		events:         []store.ReviewClearEvent{{ID: 1, Key: "fresh", SyncID: "obs"}, {ID: 2, Key: "old", SyncID: "obs"}, {ID: 3, Key: "absent", SyncID: "missing"}},
		exportedClears: map[string]bool{"obs:2:old": true},
	}
	chunk, err := buildGitExportChunk(data, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chunk.ReviewClears, []string{"obs:1:fresh"}) || len(chunk.Mutations) != 3 {
		t.Fatalf("clear/order shape: %+v", chunk)
	}
	if chunk.Mutations[0] != relation || chunk.Mutations[1] != deletion {
		t.Fatal("relation/delete order changed")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(chunk.Mutations[2].Payload), &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["review_after"]) != "null" || chunk.Mutations[2].EntityKey != "obs" {
		t.Fatalf("review reset lost explicit null: %+v", chunk.Mutations[2])
	}
	if len(inputs.exportedClears) != 1 {
		t.Fatal("builder acknowledged a receipt")
	}
	inputs.exportedClears["obs:1:fresh"] = true
	inputs.exportedDeletes = map[string]struct{}{mutationIdentityKey(deletion): {}}
	repeated, err := buildGitExportChunk(data, inputs)
	if err != nil || len(repeated.ReviewClears) != 0 || len(repeated.Mutations) != 1 || repeated.Mutations[0] != relation {
		t.Fatalf("historical clear/delete replay: %+v %v", repeated, err)
	}
}
