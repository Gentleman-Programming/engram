package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMarkReviewedCanonicalTimestamp(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnrollProject("demo"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("precision", "demo", "/tmp/demo"); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "precision", Type: "decision", Title: "decision", Content: "content", Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReviewed(id); err != nil {
		t.Fatal(err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse("2006-01-02 15:04:05", obs.UpdatedAt); err != nil {
		t.Fatalf("noncanonical stored timestamp %q: %v", obs.UpdatedAt, err)
	}
	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) == 0 {
		t.Fatal("enrolled review did not queue a mutation")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(pending[len(pending)-1].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["updated_at"] != obs.UpdatedAt {
		t.Fatalf("queued timestamp %v differs from stored %s", payload["updated_at"], obs.UpdatedAt)
	}
}
