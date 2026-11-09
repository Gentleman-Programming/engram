package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResumeScopeRejectsWithoutChangingIdentities(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)
	a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
	for _, dir := range []string{a, b} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	s := newTestStore(t)
	if err := s.CreateSessionWithOwnershipMode("host", "owner", a, SessionOwnershipProjectOwned); err != nil {
		t.Fatal(err)
	}
	if err := s.EndSession("host", "terminal"); err != nil {
		t.Fatal(err)
	}
	original, err := s.GetSession("host")
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{SessionOwnershipProjectOwned, SessionOwnershipShared} {
		id, err := s.ResumeSessionWithOwnershipMode("host", "foreign", b, mode)
		if id != "" || !errors.Is(err, ErrSessionOwnershipMismatch) {
			t.Fatalf("foreign resume mode=%s id=%q err=%v", mode, id, err)
		}
		current, err := s.GetSession("host")
		if err != nil || !reflect.DeepEqual(current, original) {
			t.Fatalf("root changed=%+v err=%v", current, err)
		}
		after, err := s.Stats()
		if err != nil || !reflect.DeepEqual(after, before) {
			t.Fatalf("counters changed=%+v err=%v", after, err)
		}
		if _, err := s.GetSession("host:resume:2"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("rejection created continuation: %v", err)
		}
	}
	// Simulate an already-created incompatible continuation using public registration.
	if err := s.CreateSessionWithOwnershipMode("host:resume:2", "owner", b, SessionOwnershipProjectOwned); err != nil {
		t.Fatal(err)
	}
	continuation, err := s.GetSession("host:resume:2")
	if err != nil {
		t.Fatal(err)
	}
	if id, err := s.ResumeSessionWithOwnershipMode("host", "owner", a, SessionOwnershipProjectOwned); err != nil || id != "host:resume:2" {
		t.Fatalf("selection id=%q err=%v", id, err)
	}
	registered, err := s.GetSession("host:resume:2")
	if err != nil {
		t.Fatal(err)
	}
	// Successful registration renews the lease even for an existing identity;
	// it must not repair the persisted scope. Rejected runtime calls renew nothing.
	continuation.RuntimeLeaseExpiresAt = registered.RuntimeLeaseExpiresAt
	if !reflect.DeepEqual(registered, continuation) {
		t.Fatalf("registration repaired continuation: %+v", registered)
	}
	continuation = registered
	for _, dir := range []string{a, b} {
		if _, err := s.ResolveRuntimeSessionWithOwnershipMode("host", "owner", dir, SessionOwnershipProjectOwned); !errors.Is(err, ErrRuntimeSessionScopeConflict) {
			t.Fatalf("resolve=%v", err)
		}
		if _, err := s.EndRuntimeSession("host", "owner", dir, SessionOwnershipProjectOwned, "rejected"); !errors.Is(err, ErrRuntimeSessionScopeConflict) {
			t.Fatalf("end=%v", err)
		}
	}
	after, err := s.GetSession("host:resume:2")
	if err != nil || !reflect.DeepEqual(after, continuation) {
		t.Fatalf("continuation silently repaired: %+v %v", after, err)
	}
	if _, err := s.GetSession("host:resume:3"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("advanced past conflict: %v", err)
	}
}
