package cloudstore

import (
	"context"
	"errors"
	"testing"
)

func TestVerifyPromptSourceAttestation(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	if err := cs.RegisterSessionAuthority(ctx, "session", "owner", "registrar"); err != nil {
		t.Fatal(err)
	}
	if err := cs.ClaimPromptPair(ctx, "session", "inbox", "sync", "prompt", "claimer"); err != nil {
		t.Fatal(err)
	}
	row, err := cs.AttestPromptSource(ctx, "session", "inbox", "sync", "owner", "prompt", "human")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		id     int64
		fields [5]string
		found  bool
	}{
		{"exact", row.ID, [5]string{"session", "inbox", "sync", "owner", "prompt"}, true},
		{"missing", row.ID + 1, [5]string{"session", "inbox", "sync", "owner", "prompt"}, false},
		{"wrong inbox", row.ID, [5]string{"session", "other", "sync", "owner", "prompt"}, false},
		{"wrong owner", row.ID, [5]string{"session", "inbox", "sync", "other", "prompt"}, false},
		{"wrong prompt", row.ID, [5]string{"session", "inbox", "sync", "owner", "other"}, false},
		{"wrong session", row.ID, [5]string{"other", "inbox", "sync", "owner", "prompt"}, false},
		{"wrong sync", row.ID, [5]string{"session", "inbox", "other", "owner", "prompt"}, false},
		{"invalid id", 0, [5]string{"session", "inbox", "sync", "owner", "prompt"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fields
			got, err := cs.VerifyPromptSourceAttestation(ctx, tc.id, f[0], f[1], f[2], f[3], f[4])
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.found {
				t.Fatalf("verified=%v want %v", got, tc.found)
			}
		})
	}
}

func TestPromptSourceAttestationExactAuthorityAndAppendOnlyAudit(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	if _, err := cs.db.ExecContext(ctx, `INSERT INTO cloud_project_sessions (project_name, session_id) VALUES ('owner', 'unregistered')`); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.AttestPromptSource(ctx, "unregistered", "inbox", "sync", "owner", "prompt", "human"); !errors.Is(err, ErrPromptSourceAttestationUnbound) {
		t.Fatalf("indexed session conferred authority: %v", err)
	}
	if err := cs.RegisterSessionAuthority(ctx, "session", "owner", "registrar"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.AttestPromptSource(ctx, "session", "inbox", "sync", "owner", "prompt", "human"); !errors.Is(err, ErrPromptSourceAttestationUnbound) {
		t.Fatalf("unclaimed pair conferred authority: %v", err)
	}
	if err := cs.ClaimPromptPair(ctx, "session", "inbox", "sync", "prompt", "claimer"); err != nil {
		t.Fatal(err)
	}
	before, err := cs.GetPromptPairClaim(ctx, "session", "inbox")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args [6]string
	}{
		{"wrong session", [6]string{"other", "inbox", "sync", "owner", "prompt", "human"}},
		{"wrong inbox", [6]string{"session", "other", "sync", "owner", "prompt", "human"}},
		{"wrong sync", [6]string{"session", "inbox", "other", "owner", "prompt", "human"}},
		{"wrong owner", [6]string{"session", "inbox", "sync", "prompt", "prompt", "human"}},
		{"wrong prompt", [6]string{"session", "inbox", "sync", "owner", "owner", "human"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.args
			if _, err := cs.AttestPromptSource(ctx, a[0], a[1], a[2], a[3], a[4], a[5]); !errors.Is(err, ErrPromptSourceAttestationUnbound) {
				t.Fatalf("accepted mismatch: %v", err)
			}
		})
	}
	for i := 0; i < 6; i++ {
		a := [6]string{"session", "inbox", "sync", "owner", "prompt", "human"}
		a[i] = "  "
		if _, err := cs.AttestPromptSource(ctx, a[0], a[1], a[2], a[3], a[4], a[5]); err == nil {
			t.Fatalf("accepted blank field %d", i)
		}
	}
	first, err := cs.AttestPromptSource(ctx, "session", "inbox", "sync", "owner", "prompt", "human-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := cs.AttestPromptSource(ctx, "session", "inbox", "sync", "owner", "prompt", "human-two")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.AttestedAt.IsZero() || second.AttestedAt.IsZero() || first.ActorID != "human-one" || second.ActorID != "human-two" {
		t.Fatalf("audit records not independent: %+v %+v", first, second)
	}
	var count int
	if err := cs.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_prompt_source_attestations`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	after, err := cs.GetPromptPairClaim(ctx, "session", "inbox")
	if err != nil || *before != *after {
		t.Fatalf("claim audit changed: %+v %+v %v", before, after, err)
	}
}
