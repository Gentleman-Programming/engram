package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/store"
)

var ErrSecretTooShort = errors.New("jwt secret must be at least 32 bytes")
var ErrBearerTokenNotConfigured = errors.New("cloud bearer token is not configured")
var ErrInvalidDashboardSessionToken = errors.New("invalid dashboard session token")
var ErrProjectNotAllowed = errors.New("project is not allowed for this token")

type Service struct {
	store         *cloudstore.CloudStore
	expectedToken string
	dashboardAuth map[string]struct{}
	allowed       map[string]struct{}
	jwtSecret     []byte
	now           func() time.Time
}

type ProjectScopeAuthorizer struct {
	allowed map[string]struct{}
}

func NewService(store *cloudstore.CloudStore, jwtSecret string) (*Service, error) {
	if len(jwtSecret) < 32 {
		return nil, ErrSecretTooShort
	}
	return &Service{store: store, jwtSecret: []byte(jwtSecret), now: time.Now}, nil
}

func NewProjectScopeAuthorizer(projects []string) *ProjectScopeAuthorizer {
	a := &ProjectScopeAuthorizer{allowed: make(map[string]struct{})}
	a.SetAllowedProjects(projects)
	return a
}

type dashboardSessionClaims struct {
	TokenHash string `json:"token_hash"`
	Exp       int64  `json:"exp"`
	Iat       int64  `json:"iat"`
}

// MintDashboardSession returns a signed dashboard session token.
// The token is opaque to clients and validated by ParseDashboardSession.
func (s *Service) MintDashboardSession(bearerToken string) (string, error) {
	bearerToken = strings.TrimSpace(bearerToken)
	if bearerToken == "" {
		return "", fmt.Errorf("bearer token is required")
	}
	issuedAt := s.now().UTC()
	claims := dashboardSessionClaims{
		TokenHash: s.dashboardTokenHash(bearerToken),
		Iat:       issuedAt.Unix(),
		Exp:       issuedAt.Add(8 * time.Hour).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(payload)
	signature := s.sign(payloadPart)
	return payloadPart + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// ParseDashboardSession verifies and decodes a signed dashboard session token.
func (s *Service) ParseDashboardSession(sessionToken string) (string, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	parts := strings.Split(sessionToken, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ErrInvalidDashboardSessionToken
	}
	expectedSig := s.sign(parts[0])
	providedSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrInvalidDashboardSessionToken
	}
	if !hmac.Equal(expectedSig, providedSig) {
		return "", ErrInvalidDashboardSessionToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrInvalidDashboardSessionToken
	}
	var claims dashboardSessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", ErrInvalidDashboardSessionToken
	}
	if strings.TrimSpace(claims.TokenHash) == "" {
		return "", ErrInvalidDashboardSessionToken
	}
	if claims.Exp <= s.now().UTC().Unix() {
		return "", ErrInvalidDashboardSessionToken
	}
	expectedToken := strings.TrimSpace(s.expectedToken)
	if expectedToken == "" {
		return "", ErrBearerTokenNotConfigured
	}
	if hmac.Equal([]byte(claims.TokenHash), []byte(s.dashboardTokenHash(expectedToken))) {
		return expectedToken, nil
	}
	for token := range s.dashboardAuth {
		token = strings.TrimSpace(token)
		if token == "" || token == expectedToken {
			continue
		}
		if hmac.Equal([]byte(claims.TokenHash), []byte(s.dashboardTokenHash(token))) {
			return token, nil
		}
	}
	return "", ErrInvalidDashboardSessionToken
}

func (s *Service) sign(payloadPart string) []byte {
	mac := hmac.New(sha256.New, s.jwtSecret)
	_, _ = mac.Write([]byte(payloadPart))
	return mac.Sum(nil)
}

func (s *Service) dashboardTokenHash(token string) string {
	mac := hmac.New(sha256.New, s.jwtSecret)
	_, _ = mac.Write([]byte("dashboard:"))
	_, _ = mac.Write([]byte(token))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) SetBearerToken(token string) {
	s.expectedToken = strings.TrimSpace(token)
}

func (s *Service) SetDashboardSessionTokens(tokens []string) {
	s.dashboardAuth = make(map[string]struct{})
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		s.dashboardAuth[token] = struct{}{}
	}
}

func (s *Service) SetAllowedProjects(projects []string) {
	s.allowed = make(map[string]struct{})
	for _, project := range projects {
		normalized, _ := store.NormalizeProject(project)
		normalized = strings.TrimSpace(normalized)
		if normalized == "" {
			continue
		}
		s.allowed[normalized] = struct{}{}
	}
}

func (s *Service) AuthorizeProject(project string) error {
	return authorizeProjectAgainstAllowlist(project, s.allowed)
}

// EnrolledProjects returns the sorted list of projects that this Service is
// authorized to serve. Used by cloudserver's mutation pull to filter mutations
// to the caller's enrolled projects (REQ-202).
//
// The interface is cloudserver.EnrolledProjectsProvider; this method makes
// *Service satisfy it without importing cloudserver (structural assertion).
func (s *Service) EnrolledProjects() []string {
	return sortedAllowlist(s.allowed)
}

func (a *ProjectScopeAuthorizer) SetAllowedProjects(projects []string) {
	a.allowed = make(map[string]struct{})
	for _, project := range projects {
		normalized, _ := store.NormalizeProject(project)
		normalized = strings.TrimSpace(normalized)
		if normalized == "" {
			continue
		}
		a.allowed[normalized] = struct{}{}
	}
}

func (a *ProjectScopeAuthorizer) AuthorizeProject(project string) error {
	return authorizeProjectAgainstAllowlist(project, a.allowed)
}

// EnrolledProjects returns the sorted list of projects this authorizer allows.
// Matches the cloudserver.EnrolledProjectsProvider contract so mutation pull
// can filter server-side by the caller's enrolled projects (REQ-202) rather
// than fail-closing to an empty result set.
func (a *ProjectScopeAuthorizer) EnrolledProjects() []string {
	return sortedAllowlist(a.allowed)
}

// sortedAllowlist returns a sorted slice of the map keys.
// Isolated to one spot so both Service and ProjectScopeAuthorizer behave
// identically and tests can pin ordering.
func sortedAllowlist(allowed map[string]struct{}) []string {
	if len(allowed) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(allowed))
	for project := range allowed {
		out = append(out, project)
	}
	sort.Strings(out)
	return out
}

func authorizeProjectAgainstAllowlist(project string, allowed map[string]struct{}) error {
	if len(allowed) == 0 {
		return fmt.Errorf("cloud project allowlist is not configured")
	}
	normalized, _ := store.NormalizeProject(project)
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return fmt.Errorf("project is required")
	}
	if _, ok := allowed[normalized]; ok {
		return nil
	}
	return fmt.Errorf("%w", ErrProjectNotAllowed)
}

func (s *Service) Authorize(r *http.Request) error {
	if strings.TrimSpace(s.expectedToken) == "" {
		return ErrBearerTokenNotConfigured
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return fmt.Errorf("missing authorization header")
	}
	parts := strings.Fields(header)
	if len(parts) != 2 {
		return fmt.Errorf("authorization must use Bearer token")
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return fmt.Errorf("authorization must use Bearer token")
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return fmt.Errorf("bearer token is required")
	}
	if !hmac.Equal([]byte(token), []byte(s.expectedToken)) {
		return fmt.Errorf("invalid bearer token")
	}
	return nil
}
