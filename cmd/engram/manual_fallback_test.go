package main

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCmdSaveAvoidsForeignManualSession(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const legacy = "manual-save-dotfiles"
	const owner = "stale-wsl-project"
	if err := s.CreateSession(legacy, owner, "/old/dotfiles"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{SessionID: legacy, Project: owner, Type: "manual", Title: "Existing history", Content: "Preserve this", Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetSession(legacy)
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.RecentObservations(owner, "project", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	outputs := make(map[string]string)
	for _, title := range []string{"First save", "Second save"} {
		withArgs(t, "engram", "save", title, "New memory", "--project", "dotfiles")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSave(cfg) })
		if recovered != nil || stderr != "" || stdout == "" {
			t.Fatalf("save failed: panic=%v stdout=%q stderr=%q", recovered, stdout, stderr)
		}
		outputs[title] = stdout
	}
	s, err = store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, err := s.GetSession(legacy)
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := s.RecentObservations(owner, "project", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, preserved) {
		t.Fatal("foreign session or history changed")
	}
	saved, err := s.RecentObservations("dotfiles", "project", 10)
	if err != nil || len(saved) != 2 {
		t.Fatalf("saved=%v err=%v", saved, err)
	}
	for _, observation := range saved {
		want := fmt.Sprintf("Memory saved: #%d %q (manual)\n", observation.ID, observation.Title)
		if outputs[observation.Title] != want || observation.Content != "New memory" {
			t.Fatalf("saved output/content mismatch: output=%q observation=%v", outputs[observation.Title], observation)
		}
	}
	if saved[0].SessionID == legacy || saved[0].SessionID != saved[1].SessionID {
		t.Fatalf("fallback not isolated and stable: %v", saved)
	}
	session, err := s.GetSession(saved[0].SessionID)
	if err != nil || session.Project != "dotfiles" || session.OwnershipMode != store.SessionOwnershipProjectOwned {
		t.Fatalf("fallback=%v err=%v", session, err)
	}
}
