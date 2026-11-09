package store

import "testing"

func TestCompactSearchProjectionConditionalFields(t *testing.T) {
	project, topic, review := "projection-project", "decision/projection", "2000-01-01"
	empty := ""
	rows := []SearchPreviewResult{
		{ID: 1, SyncID: "projection-one", Type: "decision", Title: "One", Scope: "project", Preview: "bounded", Truncated: true, Pinned: true, Project: &project, TopicKey: &topic, ReviewAfter: &review},
		{ID: 2, SyncID: "projection-two", Type: "note", Title: "Two", Scope: "personal", TopicKey: &empty},
	}
	entries := CompactSearchProjection(rows, nil)
	first := entries[0]
	if first["project"] != project || first["topic_key"] != topic || first["review_after"] != review || first["state"] != rows[0].State() || first["pinned"] != true || first["preview"] != "bounded" || first["truncated"] != true {
		t.Fatalf("metadata lost: %#v", first)
	}
	for _, key := range []string{"project", "topic_key", "review_after", "relations", "content"} {
		if _, ok := entries[1][key]; ok {
			t.Errorf("unexpected optional field %s: %#v", key, entries[1])
		}
	}
	if entries[1]["pinned"] != false || entries[1]["truncated"] != false {
		t.Fatalf("false fields must remain explicit: %#v", entries[1])
	}
	if emptyResults := CompactSearchProjection(nil, nil); emptyResults == nil || len(emptyResults) != 0 {
		t.Fatalf("empty projection must be stable array: %#v", emptyResults)
	}
}
