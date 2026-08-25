package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestPrincipalDomainValidationAndStringValues(t *testing.T) {
	for _, kind := range []PrincipalKind{PrincipalKindHuman, PrincipalKindServiceAccount, PrincipalKindLegacy} {
		if !kind.Valid() || kind.String() != string(kind) {
			t.Fatalf("expected valid kind string round-trip for %q", kind)
		}
	}
	if PrincipalKind("robot").Valid() {
		t.Fatal("unknown principal kind must be invalid")
	}
	for _, role := range []Role{RoleAdmin, RoleMember} {
		if !role.Valid() || role.String() != string(role) {
			t.Fatalf("expected valid role string round-trip for %q", role)
		}
	}
	if Role("owner").Valid() {
		t.Fatal("unknown role must be invalid")
	}
	for _, source := range []PrincipalSource{PrincipalSourceManagedToken, PrincipalSourceLegacyEnvSync, PrincipalSourceLegacyEnvAdmin, PrincipalSourceBootstrapCLI, PrincipalSourceInsecureDev} {
		if !source.Valid() || source.String() != string(source) {
			t.Fatalf("expected valid source string round-trip for %q", source)
		}
	}
	principal := Principal{ID: "p-1", Kind: PrincipalKindHuman, Role: RoleAdmin, Source: PrincipalSourceManagedToken, Enabled: true}
	if err := principal.Validate(); err != nil {
		t.Fatalf("valid principal rejected: %v", err)
	}
	principal.Role = Role("owner")
	if err := principal.Validate(); err == nil {
		t.Fatal("invalid principal role must fail validation")
	}
}

func TestGenerateManagedTokenUsesEngramFormatAndHighEntropySecret(t *testing.T) {
	withDeterministicRandom(t, bytesReader(byte(0x01), managedTokenPrefixBytes, byte(0x02), managedTokenSecretBytes), func() {
		token, err := GenerateManagedToken("live")
		if err != nil {
			t.Fatalf("generate token: %v", err)
		}
		if token.Raw != "egc_live_01010101_AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI" || token.Prefix != "egc_live_01010101" {
			t.Fatalf("unexpected deterministic token: %+v", token)
		}
		if strings.Contains(token.Prefix, token.Secret) {
			t.Fatal("display prefix must not include secret material")
		}
		parts := strings.Split(token.Raw, "_")
		if len(parts) != 4 {
			t.Fatalf("expected egc_<env>_<prefix>_<secret>, got %q", token.Raw)
		}
		secretBytes, err := base64.RawURLEncoding.DecodeString(parts[3])
		if err != nil || len(secretBytes) < 32 {
			t.Fatalf("secret must be URL-safe base64 with >=32 bytes entropy, bytes=%d err=%v", len(secretBytes), err)
		}
	})
}

func TestGenerateManagedTokenNormalizesEnvironmentAndReportsRandomFailures(t *testing.T) {
	cases := map[string]string{
		"":              "egc_live_01010101_",
		"  LIVE  ":      "egc_live_01010101_",
		"Prod_US East!": "egc_prod-us-east_01010101_",
		"___":           "egc_live_01010101_",
	}
	for input, wantPrefix := range cases {
		withDeterministicRandom(t, bytesReader(byte(0x01), managedTokenPrefixBytes, byte(0x02), managedTokenSecretBytes), func() {
			token, err := GenerateManagedToken(input)
			if err != nil {
				t.Fatalf("generate token for %q: %v", input, err)
			}
			if !strings.HasPrefix(token.Raw, wantPrefix) {
				t.Fatalf("token for %q should have prefix %q, got %q", input, wantPrefix, token.Raw)
			}
		})
	}

	withDeterministicRandom(t, failingReader{}, func() {
		if _, err := GenerateManagedToken("live"); err == nil || !strings.Contains(err.Error(), "generate token prefix") {
			t.Fatalf("expected prefix random failure, got %v", err)
		}
	})
	withDeterministicRandom(t, bytesReader(byte(0x01), managedTokenPrefixBytes), func() {
		if _, err := GenerateManagedToken("live"); err == nil || !strings.Contains(err.Error(), "generate token secret") {
			t.Fatalf("expected secret random failure, got %v", err)
		}
	})
}

// TestManagedTokenHasherRejectsTooShortPepper proves a non-empty but
// too-short pepper (e.g. a 1-char ENGRAM_CLOUD_TOKEN_PEPPER) is rejected
// instead of silently being accepted as the HMAC key, matching the
// precedent set by auth.NewService's JWT secret length check.
func TestManagedTokenHasherRejectsTooShortPepper(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pepper []byte
	}{
		{"one byte", []byte("x")},
		{"31 bytes (one short of minimum)", []byte(strings.Repeat("a", managedTokenPepperMinBytes-1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewManagedTokenHasher(tc.pepper); !errors.Is(err, ErrTokenPepperTooShort) {
				t.Fatalf("expected ErrTokenPepperTooShort for %d-byte pepper, got %v", len(tc.pepper), err)
			}
		})
	}

	// Exactly the minimum length must be accepted.
	if _, err := NewManagedTokenHasher([]byte(strings.Repeat("a", managedTokenPepperMinBytes))); err != nil {
		t.Fatalf("expected exactly-minimum-length pepper to be accepted, got %v", err)
	}
}

func TestManagedTokenHasherRequiresDedicatedPepperAndUsesDomainSeparatedHMAC(t *testing.T) {
	if _, err := NewManagedTokenHasher(nil); !errors.Is(err, ErrTokenPepperRequired) {
		t.Fatalf("expected ErrTokenPepperRequired, got %v", err)
	}
	pepper := []byte("dedicated-cloud-token-pepper-32-bytes")
	hasher, err := NewManagedTokenHasher(pepper)
	if err != nil {
		t.Fatalf("new token hasher: %v", err)
	}
	rawToken := "egc_live_ab12cd34_secret"
	verifier, err := hasher.Hash(rawToken)
	if err != nil {
		t.Fatalf("hash token: %v", err)
	}
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte("engram-cloud-token:v1:" + rawToken))
	want := "hmac-sha256:v1:" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if verifier != want || strings.Contains(verifier, rawToken) || strings.Contains(verifier, "secret") {
		t.Fatalf("verifier must be domain-separated HMAC without raw material: got=%q want=%q", verifier, want)
	}
	if !hasher.Verify(rawToken, verifier) {
		t.Fatal("exact token must verify")
	}
	for _, wrong := range []string{"egc_live_ab12cd34_secre", rawToken + "-extra", ""} {
		if hasher.Verify(wrong, verifier) {
			t.Fatalf("wrong token %q must not verify", wrong)
		}
	}
	if hasher.Verify(rawToken, strings.TrimPrefix(verifier, "hmac-sha256:v1:")) {
		t.Fatal("verifier must require exact HMAC scheme/version")
	}
}

func TestResolverRejectsUnsafeManagedTokenRecords(t *testing.T) {
	hasher := mustTokenHasher(t)
	activeHash := mustHash(t, hasher, "active-token")
	revokedHash := mustHash(t, hasher, "revoked-token")
	disabledHash := mustHash(t, hasher, "disabled-token")
	mismatchHash := mustHash(t, hasher, "mismatch-token")
	invalidHash := mustHash(t, hasher, "invalid-token")
	revokedAt := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	lookup := fakeManagedTokenLookup{records: map[string]managedLookupResult{
		activeHash:   {ManagedTokenRecord{ID: "tok-active", PrincipalID: "p-active", Hash: activeHash}, Principal{ID: "p-active", Kind: PrincipalKindHuman, Role: RoleMember, Source: PrincipalSourceManagedToken, Enabled: true}},
		revokedHash:  {ManagedTokenRecord{ID: "tok-revoked", PrincipalID: "p-active", Hash: revokedHash, RevokedAt: &revokedAt}, Principal{ID: "p-active", Kind: PrincipalKindHuman, Role: RoleMember, Source: PrincipalSourceManagedToken, Enabled: true}},
		disabledHash: {ManagedTokenRecord{ID: "tok-disabled", PrincipalID: "p-disabled", Hash: disabledHash}, Principal{ID: "p-disabled", Kind: PrincipalKindHuman, Role: RoleMember, Source: PrincipalSourceManagedToken, Enabled: false}},
		mismatchHash: {ManagedTokenRecord{ID: "tok-mismatch", PrincipalID: "p-token", Hash: mismatchHash}, Principal{ID: "p-other", Kind: PrincipalKindHuman, Role: RoleMember, Source: PrincipalSourceManagedToken, Enabled: true}},
		invalidHash:  {ManagedTokenRecord{ID: "tok-invalid", PrincipalID: "p-invalid", Hash: invalidHash}, Principal{ID: "p-invalid", Kind: PrincipalKindHuman, Role: Role("owner"), Source: PrincipalSourceManagedToken, Enabled: true}},
	}}
	resolver := NewPrincipalResolver(ResolverConfig{Hasher: hasher, ManagedTokens: lookup})

	principal, err := resolver.ResolveBearerToken(context.Background(), "active-token")
	if err != nil || principal.ID != "p-active" || principal.TokenID != "tok-active" || principal.Source != PrincipalSourceManagedToken {
		t.Fatalf("active token principal mismatch: principal=%+v err=%v", principal, err)
	}
	if _, err := resolver.ResolveBearerToken(context.Background(), "revoked-token"); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("expected revoked token rejection, got %v", err)
	}
	if _, err := resolver.ResolveBearerToken(context.Background(), "disabled-token"); !errors.Is(err, ErrPrincipalDisabled) {
		t.Fatalf("expected disabled principal rejection, got %v", err)
	}
	if _, err := resolver.ResolveBearerToken(context.Background(), "mismatch-token"); !errors.Is(err, ErrInvalidPrincipal) {
		t.Fatalf("expected token/principal mismatch rejection, got %v", err)
	}
	if _, err := resolver.ResolveBearerToken(context.Background(), "invalid-token"); !errors.Is(err, ErrInvalidPrincipal) {
		t.Fatalf("expected invalid stored principal rejection, got %v", err)
	}
}

func TestResolverPreservesLegacyEnvPrincipalExpectations(t *testing.T) {
	resolver := NewPrincipalResolver(ResolverConfig{Legacy: LegacyCredentials{SyncToken: "legacy-sync", AdminToken: "legacy-admin"}})
	cases := []struct {
		token string
		want  Principal
	}{
		{"legacy-sync", Principal{ID: "legacy:sync", Kind: PrincipalKindLegacy, DisplayName: "LEGACY_SYNC", Role: RoleMember, Enabled: true, Source: PrincipalSourceLegacyEnvSync}},
		{"legacy-admin", Principal{ID: "legacy:admin", Kind: PrincipalKindLegacy, DisplayName: "OPERATOR", Role: RoleAdmin, Enabled: true, Source: PrincipalSourceLegacyEnvAdmin}},
	}
	for _, tc := range cases {
		got, err := resolver.ResolveBearerToken(context.Background(), tc.token)
		if err != nil || got != tc.want {
			t.Fatalf("legacy principal mismatch: got=%+v want=%+v err=%v", got, tc.want, err)
		}
	}
	if _, err := resolver.ResolveBearerToken(context.Background(), "unknown-token"); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("expected unknown token rejection, got %v", err)
	}

	resolverWithManagedLookupButNoHasher := NewPrincipalResolver(ResolverConfig{
		ManagedTokens: fakeManagedTokenLookup{},
		Legacy:        LegacyCredentials{SyncToken: "legacy-sync", AdminToken: "legacy-admin"},
	})
	principal, err := resolverWithManagedLookupButNoHasher.ResolveBearerToken(context.Background(), "legacy-sync")
	if err != nil || principal.Source != PrincipalSourceLegacyEnvSync {
		t.Fatalf("legacy sync must resolve even when managed lookup is configured without pepper: principal=%+v err=%v", principal, err)
	}
	if _, err := resolverWithManagedLookupButNoHasher.ResolveBearerToken(context.Background(), "managed-looking-token"); !errors.Is(err, ErrTokenPepperRequired) {
		t.Fatalf("non-legacy managed lookup without pepper must fail clearly, got %v", err)
	}
}

func TestResolverPropagatesManagedLookupFailures(t *testing.T) {
	backendErr := fmt.Errorf("token store unavailable")
	resolver := NewPrincipalResolver(ResolverConfig{Hasher: mustTokenHasher(t), ManagedTokens: failingManagedTokenLookup{err: backendErr}})
	if _, err := resolver.ResolveBearerToken(context.Background(), "managed-token"); !errors.Is(err, backendErr) {
		t.Fatalf("expected backend error propagation, got %v", err)
	}
}

func mustTokenHasher(t *testing.T) *ManagedTokenHasher {
	t.Helper()
	hasher, err := NewManagedTokenHasher([]byte("dedicated-cloud-token-pepper-32-bytes"))
	if err != nil {
		t.Fatalf("new token hasher: %v", err)
	}
	return hasher
}

func mustHash(t *testing.T, hasher *ManagedTokenHasher, raw string) string {
	t.Helper()
	hash, err := hasher.Hash(raw)
	if err != nil {
		t.Fatalf("hash %q: %v", raw, err)
	}
	return hash
}

type managedLookupResult struct {
	token     ManagedTokenRecord
	principal Principal
}

type fakeManagedTokenLookup struct {
	records map[string]managedLookupResult
}

func (f fakeManagedTokenLookup) FindManagedTokenByHash(_ context.Context, hash string) (ManagedTokenRecord, Principal, error) {
	result, ok := f.records[hash]
	if !ok {
		return ManagedTokenRecord{}, Principal{}, ErrUnknownToken
	}
	return result.token, result.principal, nil
}

type failingManagedTokenLookup struct {
	err error
}

func (f failingManagedTokenLookup) FindManagedTokenByHash(context.Context, string) (ManagedTokenRecord, Principal, error) {
	return ManagedTokenRecord{}, Principal{}, f.err
}

func withDeterministicRandom(t *testing.T, reader io.Reader, run func()) {
	t.Helper()
	original := cryptoRandRead
	cryptoRandRead = reader.Read
	t.Cleanup(func() { cryptoRandRead = original })
	run()
}

func bytesReader(chunks ...any) *strings.Reader {
	var builder strings.Builder
	for i := 0; i < len(chunks); i += 2 {
		value := chunks[i].(byte)
		count := chunks[i+1].(int)
		for range count {
			builder.WriteByte(value)
		}
	}
	return strings.NewReader(builder.String())
}

type failingReader struct{}

func (failingReader) Read(_ []byte) (int, error) {
	return 0, fmt.Errorf("entropy unavailable")
}
