package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestKnowledgeReviewDefaults(t *testing.T) {
	for _, tc := range []struct {
		typ    string
		months int
	}{
		{"config", 3}, {"discovery", 6}, {"pattern", 6}, {"architecture", 12}, {"bugfix", 12},
		{"decision", 6}, {"policy", 12}, {"preference", 3},
		{"learning", 0}, {"manual", 0}, {"tool_use", 0}, {"file_change", 0}, {"file_read", 0}, {"command", 0}, {"search", 0}, {"session_summary", 0}, {"custom", 0},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("defaults", "demo", "/tmp/demo"); err != nil {
				t.Fatal(err)
			}
			start := time.Now().UTC()
			id, err := s.AddObservation(AddObservationParams{SessionID: "defaults", Type: tc.typ, Title: tc.typ, Content: "knowledge defaults", Project: "demo"})
			if err != nil {
				t.Fatal(err)
			}
			end := time.Now().UTC()
			obs, err := s.GetObservation(id)
			if err != nil {
				t.Fatal(err)
			}
			assertReviewWindow(t, obs.ReviewAfter, tc.months, start, end)
			if obs.State() != ObservationStateActive {
				t.Fatalf("new state = %q", obs.State())
			}
			start = time.Now().UTC()
			if err := s.MarkReviewed(id); err != nil {
				t.Fatal(err)
			}
			end = time.Now().UTC()
			obs, err = s.GetObservation(id)
			if err != nil {
				t.Fatal(err)
			}
			assertReviewWindow(t, obs.ReviewAfter, tc.months, start, end)
			if obs.State() != ObservationStateActive {
				t.Fatalf("reviewed state = %q", obs.State())
			}
		})
	}
}

func assertReviewWindow(t *testing.T, date *string, months int, start, end time.Time) {
	t.Helper()
	if months == 0 {
		if date != nil {
			t.Fatalf("review_after = %q, want nil", *date)
		}
		return
	}
	if date == nil {
		t.Fatal("review_after missing")
	}
	got, err := time.Parse("2006-01-02 15:04:05", *date)
	if err != nil {
		t.Fatal(err)
	}
	lower := start.AddDate(0, months, 0).Truncate(time.Second)
	upper := end.AddDate(0, months, 0).Truncate(time.Second)
	if got.Before(lower) || got.After(upper) {
		t.Fatalf("review_after = %s, want [%s,%s]", got, lower, upper)
	}
}

func TestKnowledgeReviewLegacySyncPreserved(t *testing.T) {
	for _, typ := range []string{"config", "discovery", "pattern", "architecture", "bugfix"} {
		for _, field := range []string{"missing", "null", "date"} {
			t.Run(typ+"/"+field, func(t *testing.T) {
				s := newTestStore(t)
				if err := s.CreateSession("legacy", "demo", "/tmp/demo"); err != nil {
					t.Fatal(err)
				}
				payload := map[string]any{"sync_id": "legacy-knowledge", "session_id": "legacy", "type": typ, "title": "legacy", "content": "existing knowledge", "project": "demo", "created_at": "2020-01-01 00:00:00"}
				if field == "null" {
					payload["review_after"] = nil
				}
				if field == "date" {
					payload["review_after"] = "2020-07-01 00:00:00"
				}
				apply := func(seq int64) {
					t.Helper()
					data, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					if err := s.ApplyPulledMutation(LocalChunkTargetKey, SyncMutation{Seq: seq, Entity: SyncEntityObservation, EntityKey: "legacy-knowledge", Op: SyncOpUpsert, Payload: string(data)}); err != nil {
						t.Fatal(err)
					}
				}
				check := func() {
					t.Helper()
					obs, err := s.GetObservationBySyncID("legacy-knowledge")
					if err != nil {
						t.Fatal(err)
					}
					if field == "date" {
						if obs.ReviewAfter == nil || *obs.ReviewAfter != "2020-07-01 00:00:00" {
							t.Fatalf("date not preserved: %+v", obs)
						}
					} else if obs.ReviewAfter != nil {
						t.Fatalf("legacy null changed: %+v", obs)
					}
				}
				apply(1)
				check()
				delete(payload, "review_after")
				payload["title"] = "updated legacy"
				apply(2)
				check()
			})
		}
	}
}

func TestKnowledgeReviewOverdueVisibility(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("visibility", "demo", "/tmp/demo"); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ key, date string }{{"overdue", "2020-01-01 00:00:00"}, {"active", "2999-01-01 00:00:00"}} {
		data, err := json.Marshal(map[string]any{"sync_id": tc.key, "session_id": "visibility", "type": "config", "title": "configuration xy " + tc.key, "content": "preserved knowledge", "project": "demo", "review_after": tc.date})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ApplyPulledMutation(LocalChunkTargetKey, SyncMutation{Seq: int64(i + 1), Entity: SyncEntityObservation, EntityKey: tc.key, Op: SyncOpUpsert, Payload: string(data)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"configuration", "xy"} {
		results, err := s.Search(query, SearchOptions{Project: "demo", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 2 {
			t.Fatalf("search %q: %+v, want both memories", query, results)
		}
		for _, r := range results {
			want := ObservationStateActive
			if r.SyncID == "overdue" {
				want = ObservationStateNeedsReview
			}
			if r.State() != want || r.Content != "preserved knowledge" {
				t.Fatalf("search %q changed memory: %+v", query, r)
			}
		}
	}
	due, err := s.ObservationsNeedingReview("demo", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].SyncID != "overdue" || due[0].State() != ObservationStateNeedsReview {
		t.Fatalf("review queue = %+v", due)
	}
	if err := s.MarkReviewed(due[0].ID); err != nil {
		t.Fatal(err)
	}
	due, err = s.ObservationsNeedingReview("demo", 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("reviewed queue = %+v, %v", due, err)
	}
}
