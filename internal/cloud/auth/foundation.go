package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	PrincipalKindHuman          PrincipalKind = "human"
	PrincipalKindServiceAccount PrincipalKind = "service_account"
	PrincipalKindLegacy         PrincipalKind = "legacy"
)

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

const (
	PrincipalSourceManagedToken   PrincipalSource = "managed_token"
	PrincipalSourceLegacyEnvSync  PrincipalSource = "legacy_env_sync"
	PrincipalSourceLegacyEnvAdmin PrincipalSource = "legacy_env_admin"
	PrincipalSourceBootstrapCLI   PrincipalSource = "bootstrap_cli"
	PrincipalSourceInsecureDev    PrincipalSource = "insecure_dev"
)

const managedTokenHashPrefix = "hmac-sha256:v1:"
const managedTokenDomainSeparator = "engram-cloud-token:v1:"
const managedTokenSecretBytes = 32
const managedTokenPrefixBytes = 4

// managedTokenPepperMinBytes is the minimum accepted length for a dedicated
// cloud token pepper, matching the precedent set by auth.NewService's JWT
// secret length check (ErrSecretTooShort, also 32 bytes). A too-short pepper
// would be silently accepted as an HMAC key otherwise.
const managedTokenPepperMinBytes = 32

var cryptoRandRead = rand.Read

var ErrTokenPepperRequired = errors.New("dedicated cloud token pepper is required")
var ErrTokenPepperTooShort = fmt.Errorf("dedicated cloud token pepper must be at least %d bytes", managedTokenPepperMinBytes)
var ErrManagedTokenRequired = errors.New("managed token is required")
var ErrUnknownToken = errors.New("unknown token")
var ErrTokenRevoked = errors.New("token is revoked")
var ErrPrincipalDisabled = errors.New("principal is disabled")
var ErrInvalidPrincipal = errors.New("invalid principal")

type PrincipalKind string

type Role string

type PrincipalSource string

type Principal struct {
	ID          string
	Kind        PrincipalKind
	DisplayName string
	Role        Role
	Enabled     bool
	Source      PrincipalSource
	TokenID     string
}

type ManagedToken struct {
	Raw    string
	Prefix string
	Secret string
}

type ManagedTokenRecord struct {
	ID          string
	PrincipalID string
	Hash        string
	RevokedAt   *time.Time
}

type ManagedTokenLookup interface {
	FindManagedTokenByHash(ctx context.Context, hash string) (ManagedTokenRecord, Principal, error)
}

type ManagedTokenHasher struct {
	pepper []byte
}

type LegacyCredentials struct {
	SyncToken  string
	AdminToken string
}

type ResolverConfig struct {
	Hasher        *ManagedTokenHasher
	ManagedTokens ManagedTokenLookup
	Legacy        LegacyCredentials
}

type PrincipalResolver struct {
	hasher        *ManagedTokenHasher
	managedTokens ManagedTokenLookup
	legacy        LegacyCredentials
}

func (k PrincipalKind) String() string { return string(k) }

func (k PrincipalKind) Valid() bool {
	return k == PrincipalKindHuman || k == PrincipalKindServiceAccount || k == PrincipalKindLegacy
}

func (r Role) String() string { return string(r) }

func (r Role) Valid() bool { return r == RoleAdmin || r == RoleMember }

func (s PrincipalSource) String() string { return string(s) }

func (s PrincipalSource) Valid() bool {
	return s == PrincipalSourceManagedToken || s == PrincipalSourceLegacyEnvSync || s == PrincipalSourceLegacyEnvAdmin || s == PrincipalSourceBootstrapCLI || s == PrincipalSourceInsecureDev
}

func (p Principal) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidPrincipal)
	}
	if !p.Kind.Valid() {
		return fmt.Errorf("%w: invalid kind", ErrInvalidPrincipal)
	}
	if !p.Role.Valid() {
		return fmt.Errorf("%w: invalid role", ErrInvalidPrincipal)
	}
	if !p.Source.Valid() {
		return fmt.Errorf("%w: invalid source", ErrInvalidPrincipal)
	}
	return nil
}

func GenerateManagedToken(environment string) (ManagedToken, error) {
	environment = normalizeManagedTokenEnvironment(environment)
	prefixBytes := make([]byte, managedTokenPrefixBytes)
	if _, err := cryptoRandRead(prefixBytes); err != nil {
		return ManagedToken{}, fmt.Errorf("generate token prefix: %w", err)
	}
	secretBytes := make([]byte, managedTokenSecretBytes)
	if _, err := cryptoRandRead(secretBytes); err != nil {
		return ManagedToken{}, fmt.Errorf("generate token secret: %w", err)
	}

	prefixID := hex.EncodeToString(prefixBytes)
	secret := strings.ReplaceAll(base64.RawURLEncoding.EncodeToString(secretBytes), "_", "-")
	displayPrefix := "egc_" + environment + "_" + prefixID
	return ManagedToken{
		Raw:    displayPrefix + "_" + secret,
		Prefix: displayPrefix,
		Secret: secret,
	}, nil
}

func normalizeManagedTokenEnvironment(environment string) string {
	environment = strings.TrimSpace(strings.ToLower(environment))
	if environment == "" {
		return "live"
	}
	var builder strings.Builder
	previousHyphen := false
	for _, r := range environment {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if allowed {
			builder.WriteRune(r)
			previousHyphen = false
			continue
		}
		if !previousHyphen {
			builder.WriteByte('-')
			previousHyphen = true
		}
	}
	normalized := strings.Trim(builder.String(), "-")
	if normalized == "" {
		return "live"
	}
	return normalized
}

func NewManagedTokenHasher(pepper []byte) (*ManagedTokenHasher, error) {
	if len(pepper) == 0 {
		return nil, ErrTokenPepperRequired
	}
	if len(pepper) < managedTokenPepperMinBytes {
		return nil, ErrTokenPepperTooShort
	}
	copied := make([]byte, len(pepper))
	copy(copied, pepper)
	return &ManagedTokenHasher{pepper: copied}, nil
}

func (h *ManagedTokenHasher) Hash(rawToken string) (string, error) {
	if h == nil || len(h.pepper) == 0 {
		return "", ErrTokenPepperRequired
	}
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return "", ErrManagedTokenRequired
	}
	return managedTokenHashPrefix + base64.RawURLEncoding.EncodeToString(h.sum(rawToken)), nil
}

func (h *ManagedTokenHasher) Verify(rawToken string, verifier string) bool {
	if h == nil || len(h.pepper) == 0 {
		return false
	}
	rawToken = strings.TrimSpace(rawToken)
	verifier = strings.TrimSpace(verifier)
	if rawToken == "" || !strings.HasPrefix(verifier, managedTokenHashPrefix) {
		return false
	}
	expected := h.sum(rawToken)
	encodedMAC := strings.TrimPrefix(verifier, managedTokenHashPrefix)
	provided, err := base64.RawURLEncoding.DecodeString(encodedMAC)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, provided)
}

func (h *ManagedTokenHasher) sum(rawToken string) []byte {
	mac := hmac.New(sha256.New, h.pepper)
	_, _ = mac.Write([]byte(managedTokenDomainSeparator))
	_, _ = mac.Write([]byte(rawToken))
	return mac.Sum(nil)
}

func NewPrincipalResolver(config ResolverConfig) *PrincipalResolver {
	return &PrincipalResolver{hasher: config.Hasher, managedTokens: config.ManagedTokens, legacy: config.Legacy}
}

func (r *PrincipalResolver) ResolveBearerToken(ctx context.Context, token string) (Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Principal{}, ErrUnknownToken
	}
	if r != nil {
		if principal, ok := r.resolveLegacy(token); ok {
			return principal, nil
		}
	}
	if r != nil && r.managedTokens != nil {
		if r.hasher == nil {
			return Principal{}, ErrTokenPepperRequired
		}
		hash, err := r.hasher.Hash(token)
		if err != nil {
			return Principal{}, err
		}
		record, principal, err := r.managedTokens.FindManagedTokenByHash(ctx, hash)
		if err == nil {
			if record.PrincipalID == "" || record.PrincipalID != principal.ID {
				return Principal{}, fmt.Errorf("%w: token principal mismatch", ErrInvalidPrincipal)
			}
			if principal.Source == "" {
				principal.Source = PrincipalSourceManagedToken
			}
			if err := principal.Validate(); err != nil {
				return Principal{}, err
			}
			if record.RevokedAt != nil {
				return Principal{}, ErrTokenRevoked
			}
			if !principal.Enabled {
				return Principal{}, ErrPrincipalDisabled
			}
			principal.TokenID = record.ID
			return principal, nil
		}
		if !errors.Is(err, ErrUnknownToken) {
			return Principal{}, err
		}
	}
	return Principal{}, ErrUnknownToken
}

func (r *PrincipalResolver) resolveLegacy(token string) (Principal, bool) {
	if legacyTokenEqual(token, r.legacy.SyncToken) {
		return Principal{ID: "legacy:sync", Kind: PrincipalKindLegacy, DisplayName: "LEGACY_SYNC", Role: RoleMember, Enabled: true, Source: PrincipalSourceLegacyEnvSync}, true
	}
	if legacyTokenEqual(token, r.legacy.AdminToken) {
		return Principal{ID: "legacy:admin", Kind: PrincipalKindLegacy, DisplayName: "OPERATOR", Role: RoleAdmin, Enabled: true, Source: PrincipalSourceLegacyEnvAdmin}, true
	}
	return Principal{}, false
}

func legacyTokenEqual(presented string, expected string) bool {
	presented = strings.TrimSpace(presented)
	expected = strings.TrimSpace(expected)
	if presented == "" || expected == "" {
		return false
	}
	presentedHash := sha256.Sum256([]byte(presented))
	expectedHash := sha256.Sum256([]byte(expected))
	return hmac.Equal(presentedHash[:], expectedHash[:])
}
