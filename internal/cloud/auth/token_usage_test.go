package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

type usageLookup struct {
	fakeManagedTokenLookup
	ids []string
	err error
}

func (f *usageLookup) MarkManagedTokenUsed(_ context.Context, id string) error {
	f.ids = append(f.ids, id)
	return f.err
}

func TestManagedTokenUsageAfterValidation(t *testing.T) {
	backendErr := errors.New("usage persistence unavailable")
	now := time.Now()
	for _, name := range []string{"valid", "invalid", "mismatched", "revoked", "disabled", "unknown", "legacy", "storage failure"} {
		t.Run(name, func(t *testing.T) {
			hasher := mustTokenHasher(t)
			record := ManagedTokenRecord{ID: "token-id", PrincipalID: "principal-id"}
			principal := Principal{ID: "principal-id", Kind: PrincipalKindHuman, Role: RoleMember, Enabled: true}
			var want error
			calls := 0
			presented := "managed"
			switch name {
			case "valid":
				calls = 1
			case "invalid":
				principal.Role = "invalid"
				want = ErrInvalidPrincipal
			case "mismatched":
				record.PrincipalID = "other"
				want = ErrTokenPrincipalMismatch
			case "revoked":
				record.RevokedAt = &now
				want = ErrTokenRevoked
			case "disabled":
				principal.Enabled = false
				want = ErrPrincipalDisabled
			case "unknown":
				presented = "unknown"
				want = ErrUnknownToken
			case "legacy":
				presented = "legacy"
			case "storage failure":
				calls = 1
				want = backendErr
			}
			lookup := &usageLookup{fakeManagedTokenLookup: fakeManagedTokenLookup{records: map[string]managedLookupResult{
				mustHash(t, hasher, "managed"): {record, principal},
			}}}
			if name == "storage failure" {
				lookup.err = backendErr
			}
			resolver := NewPrincipalResolver(ResolverConfig{Hasher: hasher, ManagedTokens: lookup, Legacy: LegacyCredentials{SyncToken: "legacy"}})
			got, err := resolver.ResolveBearerToken(context.Background(), presented)
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if want != nil && got.ID != "" {
				t.Fatalf("failed authentication returned principal: %+v", got)
			}
			if len(lookup.ids) != calls {
				t.Fatalf("recorded %v, want %d calls", lookup.ids, calls)
			}
			if calls == 1 && lookup.ids[0] != record.ID {
				t.Fatalf("recorded wrong token: %v", lookup.ids)
			}
		})
	}
}

// A lookup-only backend must never silently permit managed authentication.
func TestManagedTokenUsageRequiresRecorder(t *testing.T) {
	h := mustTokenHasher(t)
	lookup := lookupWithoutUsage{fakeManagedTokenLookup{records: map[string]managedLookupResult{
		mustHash(t, h, "managed"): {
			ManagedTokenRecord{ID: "t", PrincipalID: "p"},
			Principal{ID: "p", Kind: PrincipalKindHuman, Role: RoleMember, Enabled: true},
		},
	}}}
	resolver := NewPrincipalResolver(ResolverConfig{Hasher: h, ManagedTokens: lookup})
	if _, err := resolver.ResolveBearerToken(context.Background(), "managed"); !errors.Is(err, ErrTokenUsageRecorderRequired) {
		t.Fatalf("lookup-only backend must reject authentication: %v", err)
	}
}

type lookupWithoutUsage struct{ lookup fakeManagedTokenLookup }

func (l lookupWithoutUsage) FindManagedTokenByHash(ctx context.Context, hash string) (ManagedTokenRecord, Principal, error) {
	return l.lookup.FindManagedTokenByHash(ctx, hash)
}
