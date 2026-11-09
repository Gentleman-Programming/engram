package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
)

func TestManualFallbackRemainsIndependentOfRuntimeSessions(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("manual-save-dotfiles", "stale-owner", "/old"); err != nil {
		t.Fatal(err)
	}
	id, err := s.ResolveManualSession("dotfiles", "/work/dotfiles")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: id, Project: "dotfiles", Type: "manual", Title: "Manual save", Content: "New content", Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ActiveRuntimeSessions("dotfiles", "/work/dotfiles")
	if err != nil || len(ids) != 0 {
		t.Fatalf("manual fallback became runtime: %v, err=%v", ids, err)
	}
	if err := s.StartSessionWithOwnershipMode("runtime", "dotfiles", "/work/dotfiles", SessionOwnershipShared); err != nil {
		t.Fatal(err)
	}
	ids, err = s.ActiveRuntimeSessions("dotfiles", "/work/dotfiles")
	if err != nil || !reflect.DeepEqual(ids, []string{"runtime"}) {
		t.Fatalf("runtime selection=%v err=%v", ids, err)
	}
}

func TestManualFallbackRejectsConflictingAlternativeWithoutMutation(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("manual-save-dotfiles", "stale-owner", "/old"); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("dotfiles"))
	alternate := "manual-fallback-" + hex.EncodeToString(digest[:])
	if err := s.CreateSession(alternate, "other-owner", "/other"); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetSession(alternate)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.ResolveManualSession("dotfiles", "/work/dotfiles")
	if id != "" || !errors.Is(err, ErrSessionOwnershipMismatch) {
		t.Fatalf("id=%q err=%v", id, err)
	}
	after, err := s.GetSession(alternate)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("conflicting alternate changed: %v err=%v", after, err)
	}
}
