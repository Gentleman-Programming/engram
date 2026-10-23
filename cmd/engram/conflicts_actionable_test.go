package main

import (
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"strconv"
	"strings"
	"testing"
)

func TestConflictsActionable(t *testing.T) {
	cfg := testConfig(t)
	a, _, _ := seedRelation(t, cfg, "alpha")
	b, _, _ := seedRelation(t, cfg, "beta")
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.SaveRelation(store.SaveRelationParams{SyncID: "cross-evidence", SourceID: a, TargetID: b})
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.ListRelations(store.ListRelationsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	for _, item := range items {
		if item.SyncID == r.SyncID {
			id = item.ID
		}
	}
	withArgs(t, "engram", "conflicts", "list", "--all")
	out, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" || !strings.Contains(out, "Total:  2") {
		t.Fatalf("list: %q %q", out, stderr)
	}
	withArgs(t, "engram", "conflicts", "show", strconv.FormatInt(id, 10))
	out, stderr = captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" || !strings.Contains(out, "cross-evidence") {
		t.Fatalf("show: %q %q", out, stderr)
	}
	withArgs(t, "engram", "conflicts", "stats", "--all")
	out, stderr = captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatal(stderr)
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "pending:" {
			found = true
			if f[1] != "2" {
				t.Fatal(out)
			}
		}
	}
	if !found {
		t.Fatal(out)
	}
}
