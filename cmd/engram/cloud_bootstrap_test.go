package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// fakeCloudBootstrapStore is an in-memory cloudBootstrapStore double so CLI
// bootstrap tests never require a live Postgres instance (mirrors the
// adminTestStore pattern used in internal/cloud/cloudserver).
type fakeCloudBootstrapStore struct {
	hasAdmin    bool
	hasAdminErr error

	createUserErr  error
	createGrantErr error
	createTokenErr error
	auditErr       error
	auditErrOnCall int

	users       []cloudstore.HumanUser
	grants      []cloudstore.ProjectGrant
	tokens      []cloudstore.PrincipalToken
	auditEvents []cloudstore.AuthAuditEvent

	createUserCalls       int
	createGrantCalls      int
	createTokenCalls      int
	createFirstAdminCalls int
	auditCalls            int
	closeCalls            int

	// createFirstAdminMu guards CreateFirstAdminHumanUser's check-then-create
	// critical section, mirroring the full-transaction-duration hold of the
	// real cloudstore advisory lock (pg_advisory_xact_lock), so this fake
	// genuinely serializes concurrent bootstrap attempts instead of merely
	// looking atomic.
	createFirstAdminMu sync.Mutex
}

func (s *fakeCloudBootstrapStore) HasActiveAdmin(context.Context) (bool, error) {
	if s.hasAdminErr != nil {
		return false, s.hasAdminErr
	}
	return s.hasAdmin, nil
}

func (s *fakeCloudBootstrapStore) CreateHumanUser(_ context.Context, params cloudstore.CreateHumanUserParams) (cloudstore.HumanUser, error) {
	s.createUserCalls++
	if s.createUserErr != nil {
		return cloudstore.HumanUser{}, s.createUserErr
	}
	user := cloudstore.HumanUser{
		PrincipalID: "p-" + params.Username,
		Username:    params.Username,
		Email:       params.Email,
		DisplayName: params.DisplayName,
		Role:        params.Role,
		Enabled:     true,
	}
	s.users = append(s.users, user)
	return user, nil
}

// CreateFirstAdminHumanUser mirrors cloudstore.CreateFirstAdminHumanUser's
// atomic check-then-create contract: the whole check-and-create critical
// section is held under a single lock (standing in for the real advisory
// lock held for a full Postgres transaction), so concurrent callers cannot
// race past the "no active admin" check together. A deliberate sleep between
// the check and the create widens the race window enough that, WITHOUT the
// lock, concurrent goroutines reliably interleave and create more than one
// admin (see TestCloudBootstrapAdminConcurrentFirstAdminCreatesExactlyOneAdmin's
// RED evidence in apply-progress.md).
func (s *fakeCloudBootstrapStore) CreateFirstAdminHumanUser(ctx context.Context, params cloudstore.CreateHumanUserParams) (cloudstore.HumanUser, error) {
	s.createFirstAdminMu.Lock()
	defer s.createFirstAdminMu.Unlock()
	s.createFirstAdminCalls++
	if s.hasAdminErr != nil {
		return cloudstore.HumanUser{}, s.hasAdminErr
	}
	if s.hasAdmin {
		return cloudstore.HumanUser{}, cloudstore.ErrAdminAlreadyExists
	}
	// Simulate the real cloudstore implementation's DB round-trip between
	// the existence check and the insert, so a caller (or fake) that does
	// NOT hold a lock across this whole section would race here.
	time.Sleep(5 * time.Millisecond)
	user, err := s.CreateHumanUser(ctx, cloudstore.CreateHumanUserParams{
		Username:    params.Username,
		Email:       params.Email,
		DisplayName: params.DisplayName,
		Role:        string(cloudauth.RoleAdmin),
	})
	if err != nil {
		return cloudstore.HumanUser{}, err
	}
	s.hasAdmin = true
	return user, nil
}

func (s *fakeCloudBootstrapStore) CreatePrincipalToken(_ context.Context, params cloudstore.CreatePrincipalTokenParams) (cloudstore.PrincipalToken, error) {
	s.createTokenCalls++
	if s.createTokenErr != nil {
		return cloudstore.PrincipalToken{}, s.createTokenErr
	}
	token := fakePrincipalToken(params)
	s.tokens = append(s.tokens, token)
	return token, nil
}

func (s *fakeCloudBootstrapStore) CreatePrincipalTokenWithAudit(_ context.Context, params cloudstore.CreatePrincipalTokenParams, audit cloudstore.AuthAuditEvent) (cloudstore.PrincipalToken, error) {
	s.createTokenCalls++
	if s.createTokenErr != nil {
		return cloudstore.PrincipalToken{}, s.createTokenErr
	}
	token := fakePrincipalToken(params)
	if err := s.insertAuthAuditEvent(audit); err != nil {
		return cloudstore.PrincipalToken{}, err
	}
	s.tokens = append(s.tokens, token)
	return token, nil
}

func fakePrincipalToken(params cloudstore.CreatePrincipalTokenParams) cloudstore.PrincipalToken {
	return cloudstore.PrincipalToken{
		ID:                   "tok-" + params.PrincipalID,
		PrincipalID:          params.PrincipalID,
		TokenPrefix:          params.TokenPrefix,
		TokenHash:            params.TokenHash,
		Name:                 params.Name,
		CreatedByPrincipalID: params.CreatedByPrincipalID,
	}
}

func (s *fakeCloudBootstrapStore) CreateProjectGrant(_ context.Context, params cloudstore.CreateProjectGrantParams) (cloudstore.ProjectGrant, error) {
	s.createGrantCalls++
	if s.createGrantErr != nil {
		return cloudstore.ProjectGrant{}, s.createGrantErr
	}
	grant := cloudstore.ProjectGrant{
		PrincipalID:          params.PrincipalID,
		Project:              params.Project,
		GrantedByPrincipalID: params.GrantedByPrincipalID,
	}
	s.grants = append(s.grants, grant)
	return grant, nil
}

func (s *fakeCloudBootstrapStore) InsertAuthAuditEvent(_ context.Context, event cloudstore.AuthAuditEvent) error {
	return s.insertAuthAuditEvent(event)
}

func (s *fakeCloudBootstrapStore) insertAuthAuditEvent(event cloudstore.AuthAuditEvent) error {
	s.auditCalls++
	if s.auditErr != nil {
		return s.auditErr
	}
	if s.auditErrOnCall > 0 && s.auditCalls == s.auditErrOnCall {
		return errNewCloudBootstrapFixture("audit store unavailable on configured call")
	}
	if strings.TrimSpace(event.ActorSource) == "" {
		return errActorSourceRequiredFixture
	}
	s.auditEvents = append(s.auditEvents, event)
	return nil
}

func (s *fakeCloudBootstrapStore) Close() error {
	s.closeCalls++
	return nil
}

var errActorSourceRequiredFixture = errNewCloudBootstrapFixture("actor source is required")

type cloudBootstrapFixtureError string

func errNewCloudBootstrapFixture(msg string) error { return cloudBootstrapFixtureError(msg) }

func (e cloudBootstrapFixtureError) Error() string { return string(e) }

func stubNewCloudBootstrapStore(t *testing.T, store *fakeCloudBootstrapStore) (calls *int) {
	t.Helper()
	old := newCloudBootstrapStore
	n := 0
	newCloudBootstrapStore = func(cloud.Config) (cloudBootstrapStore, error) {
		n++
		return store, nil
	}
	t.Cleanup(func() { newCloudBootstrapStore = old })
	return &n
}

// TestCloudBootstrapAdminCreatesFirstManagedAdmin proves the happy path: no
// managed admin exists yet, valid --username input creates the first managed
// human admin with the admin role and records a success bootstrap.cli audit
// event.
func TestCloudBootstrapAdminCreatesFirstManagedAdmin(t *testing.T) {
	stubExitWithPanic(t)
	store := &fakeCloudBootstrapStore{}
	stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "alice")
	stdout, _, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)
	if recovered != nil {
		t.Fatalf("expected bootstrap to succeed, panicked/exited with %v; stdout=%q", recovered, stdout)
	}

	if store.createUserCalls != 1 {
		t.Fatalf("expected CreateHumanUser to be called once, got %d", store.createUserCalls)
	}
	if len(store.users) != 1 {
		t.Fatalf("expected 1 user created, got %d", len(store.users))
	}
	if store.users[0].Role != string(cloudauth.RoleAdmin) {
		t.Fatalf("expected bootstrap admin role %q, got %q", cloudauth.RoleAdmin, store.users[0].Role)
	}
	if !strings.Contains(stdout, "alice") {
		t.Fatalf("expected stdout to mention created username, got %q", stdout)
	}

	if len(store.auditEvents) != 1 {
		t.Fatalf("expected exactly 1 audit event, got %d", len(store.auditEvents))
	}
	event := store.auditEvents[0]
	if event.Action != "bootstrap.cli" {
		t.Fatalf("expected bootstrap.cli audit action, got %q", event.Action)
	}
	if event.Outcome != "success" {
		t.Fatalf("expected success outcome, got %q", event.Outcome)
	}
	if event.ActorSource != string(cloudauth.PrincipalSourceBootstrapCLI) {
		t.Fatalf("expected actor source %q, got %q", cloudauth.PrincipalSourceBootstrapCLI, event.ActorSource)
	}
}

// TestCloudBootstrapAdminRefusesDuplicateFirstAdmin proves that when a managed
// admin already exists, CLI bootstrap refuses to create a second one, makes
// no mutation, and still records an auditable denial.
func TestCloudBootstrapAdminRefusesDuplicateFirstAdmin(t *testing.T) {
	stubExitWithPanic(t)
	store := &fakeCloudBootstrapStore{hasAdmin: true}
	stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "bob")
	_, stderr, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)

	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1 for duplicate bootstrap, got recovered=%v stderr=%q", recovered, stderr)
	}
	if store.createUserCalls != 0 {
		t.Fatalf("expected no user creation on duplicate bootstrap, got %d calls", store.createUserCalls)
	}
	if len(store.auditEvents) != 1 {
		t.Fatalf("expected exactly 1 denial audit event, got %d", len(store.auditEvents))
	}
	if store.auditEvents[0].Outcome != "denied" {
		t.Fatalf("expected denied outcome, got %q", store.auditEvents[0].Outcome)
	}
}

// TestCloudBootstrapAdminConcurrentFirstAdminCreatesExactlyOneAdmin is the
// REVIEW REMEDIATION regression test for the first-admin bootstrap TOCTOU
// race: two concurrent bootstrap attempts must never both create a first
// admin. It drives fakeCloudBootstrapStore.CreateFirstAdminHumanUser (the
// same atomic method cmdCloudBootstrapAdmin now calls, replacing the old
// separate HasActiveAdmin-then-CreateHumanUser sequence) directly from N
// concurrent goroutines and asserts exactly one succeeds while every other
// attempt is refused with cloudstore.ErrAdminAlreadyExists, with exactly one
// admin durably present in the store afterward.
//
// RED evidence (recorded in apply-progress.md): before
// CreateFirstAdminHumanUser held its lock across the full check+create
// section, an unsynchronized (check, sleep, create) implementation reliably
// let all N goroutines observe "no active admin" and all create an admin,
// failing this exact assertion (successes=5, duplicates=0, len(store.users)=5).
func TestCloudBootstrapAdminConcurrentFirstAdminCreatesExactlyOneAdmin(t *testing.T) {
	store := &fakeCloudBootstrapStore{}

	const attempts = 5
	type result struct {
		err error
	}
	results := make(chan result, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.CreateFirstAdminHumanUser(context.Background(), cloudstore.CreateHumanUserParams{
				Username: fmt.Sprintf("racer-%d", i),
			})
			results <- result{err: err}
		}(i)
	}
	wg.Wait()
	close(results)

	var successes, duplicates int
	for r := range results {
		switch {
		case r.err == nil:
			successes++
		case errors.Is(r.err, cloudstore.ErrAdminAlreadyExists):
			duplicates++
		default:
			t.Fatalf("unexpected error from concurrent first-admin bootstrap: %v", r.err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one concurrent bootstrap attempt to succeed, got %d successes and %d duplicates (store.users=%d)", successes, duplicates, len(store.users))
	}
	if duplicates != attempts-1 {
		t.Fatalf("expected the other %d attempts to be refused as duplicates, got %d", attempts-1, duplicates)
	}
	if len(store.users) != 1 {
		t.Fatalf("expected exactly 1 admin user created despite %d concurrent bootstrap attempts, got %d", attempts, len(store.users))
	}
	if store.createFirstAdminCalls != attempts {
		t.Fatalf("expected all %d concurrent attempts to go through the atomic CreateFirstAdminHumanUser path, got %d calls", attempts, store.createFirstAdminCalls)
	}
}

// TestCloudBootstrapAdminGrantFailureAuditsAdminCreationAndFailure proves
// that when an optional --grant-project step fails after the admin has
// already been durably created: (1) the admin-creation success audit event
// was already recorded (immediately, not deferred to the end), (2) a
// distinct failure/partial audit event is also recorded, and (3) the
// command still exits non-zero.
func TestCloudBootstrapAdminGrantFailureAuditsAdminCreationAndFailure(t *testing.T) {
	stubExitWithPanic(t)
	store := &fakeCloudBootstrapStore{createGrantErr: errNewCloudBootstrapFixture("grant backend unavailable")}
	stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "greta", "--grant-project", "alpha")
	_, stderr, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)

	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1 when grant creation fails, got recovered=%v stderr=%q", recovered, stderr)
	}
	if store.createUserCalls != 1 {
		t.Fatalf("expected the admin to still be durably created before the grant failure, got %d user creations", store.createUserCalls)
	}
	if len(store.auditEvents) != 2 {
		t.Fatalf("expected exactly 2 audit events (admin creation + grant failure), got %d: %+v", len(store.auditEvents), store.auditEvents)
	}
	adminEvent := store.auditEvents[0]
	if adminEvent.Outcome != cloudBootstrapAuditOutcomeSuccess || adminEvent.Metadata["created_admin"] != true {
		t.Fatalf("expected the first audit event to record admin-creation success, got %+v", adminEvent)
	}
	failureEvent := store.auditEvents[1]
	if failureEvent.Outcome != cloudBootstrapAuditOutcomeError {
		t.Fatalf("expected a distinct failure audit event for the grant failure, got %+v", failureEvent)
	}
}

// TestCloudBootstrapAdminTokenCreateFailureAuditsAdminCreationAndFailure
// proves that when token PERSISTENCE fails after the admin has already been
// durably created: (1) the admin-creation success audit event was already
// recorded, (2) no raw token is printed (nothing was durably minted), (3) a
// distinct failure/partial audit event is recorded, and (4) the command
// still exits non-zero.
func TestCloudBootstrapAdminTokenCreateFailureAuditsAdminCreationAndFailure(t *testing.T) {
	stubExitWithPanic(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN_PEPPER", "dedicated-cloud-token-pepper-for-tests")
	store := &fakeCloudBootstrapStore{createTokenErr: errNewCloudBootstrapFixture("token backend unavailable")}
	stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "hank", "--issue-token")
	stdout, stderr, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)

	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1 when token creation fails, got recovered=%v stderr=%q", recovered, stderr)
	}
	if store.createUserCalls != 1 {
		t.Fatalf("expected the admin to still be durably created before the token failure, got %d user creations", store.createUserCalls)
	}
	if strings.Contains(stdout, "egc_live_") {
		t.Fatalf("expected no raw token to be printed when token persistence fails, got stdout=%q", stdout)
	}
	if len(store.auditEvents) != 2 {
		t.Fatalf("expected exactly 2 audit events (admin creation + token failure), got %d: %+v", len(store.auditEvents), store.auditEvents)
	}
	adminEvent := store.auditEvents[0]
	if adminEvent.Outcome != cloudBootstrapAuditOutcomeSuccess || adminEvent.Metadata["created_admin"] != true {
		t.Fatalf("expected the first audit event to record admin-creation success, got %+v", adminEvent)
	}
	failureEvent := store.auditEvents[1]
	if failureEvent.Outcome != cloudBootstrapAuditOutcomeError {
		t.Fatalf("expected a distinct failure audit event for the token failure, got %+v", failureEvent)
	}
}

// TestCloudBootstrapAdminTokenAuditFailureDoesNotPersistOrDiscloseRawToken
// proves the token+audit atomicity contract for bootstrap --issue-token: when
// the completion audit fails after token generation, the command exits non-zero,
// no raw token is printed, and no managed token is persisted.
func TestCloudBootstrapAdminTokenAuditFailureDoesNotPersistOrDiscloseRawToken(t *testing.T) {
	stubExitWithPanic(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN_PEPPER", "dedicated-cloud-token-pepper-for-tests")
	store := &fakeCloudBootstrapStore{auditErrOnCall: 2}
	stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "lena", "--issue-token")
	stdout, stderr, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)

	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1 when token audit fails, got recovered=%v stderr=%q", recovered, stderr)
	}
	if strings.Contains(stdout, "egc_live_") {
		t.Fatalf("expected no raw token to be printed when token audit fails, got stdout=%q", stdout)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("expected no token to be persisted when token audit fails, got %+v", store.tokens)
	}
	if store.createTokenCalls != 1 {
		t.Fatalf("expected exactly one atomic token issuance attempt, got %d", store.createTokenCalls)
	}
	for _, event := range store.auditEvents {
		if event.Outcome == cloudBootstrapAuditOutcomeSuccess && event.ReasonCode == "bootstrap_completed" {
			t.Fatalf("expected no durable token completion audit when atomic token audit fails, got %+v", event)
		}
	}
}

// TestCloudBootstrapAdminAuditFailureStillCreatesAdminAndExitsNonZero proves
// that when the audit store itself is broken (auditErr set) for a fresh
// bootstrap (hasAdmin=false): the admin mutation still happens durably
// (CreateFirstAdminHumanUser does not depend on the audit store), the
// command fails fast and exits non-zero BEFORE attempting any optional
// grant/token step (so no token can ever be minted/orphaned when the audit
// trail can't be guaranteed), and the fatal error message says so clearly.
func TestCloudBootstrapAdminAuditFailureStillCreatesAdminAndExitsNonZero(t *testing.T) {
	stubExitWithPanic(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN_PEPPER", "dedicated-cloud-token-pepper-for-tests")
	store := &fakeCloudBootstrapStore{auditErr: errNewCloudBootstrapFixture("audit store unavailable")}
	stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "iris", "--issue-token")
	stdout, stderr, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)

	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1 when the mandatory admin-creation audit fails, got recovered=%v stdout=%q stderr=%q", recovered, stdout, stderr)
	}
	if store.createUserCalls != 1 {
		t.Fatalf("expected the admin mutation to still happen even though its audit event could not be recorded, got %d user creations", store.createUserCalls)
	}
	if store.createTokenCalls != 0 {
		t.Fatalf("expected no token to ever be attempted when the mandatory admin audit failed, got %d token creations", store.createTokenCalls)
	}
	if strings.Contains(stdout, "egc_live_") {
		t.Fatalf("expected no raw token to be printed, got stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "failed to record the required bootstrap audit event") {
		t.Fatalf("expected a clear admin-created-but-not-audited error, got stderr=%q", stderr)
	}
}

// TestCloudBootstrapAdminIssuesTokenExactlyOnce proves --issue-token prints
// the raw token exactly once, never persists/audits it, and requires the
// dedicated cloud token pepper to be configured.
func TestCloudBootstrapAdminIssuesTokenExactlyOnce(t *testing.T) {
	stubExitWithPanic(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN_PEPPER", "dedicated-cloud-token-pepper-for-tests")
	store := &fakeCloudBootstrapStore{}
	stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "carol", "--issue-token", "first-token")
	stdout, _, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)
	if recovered != nil {
		t.Fatalf("expected bootstrap with token issuance to succeed, got %v; stdout=%q", recovered, stdout)
	}

	if store.createTokenCalls != 1 {
		t.Fatalf("expected CreatePrincipalToken to be called once, got %d", store.createTokenCalls)
	}
	if len(store.tokens) != 1 {
		t.Fatalf("expected 1 token created, got %d", len(store.tokens))
	}
	rawTokenOccurrences := strings.Count(stdout, "egc_live_")
	if rawTokenOccurrences != 1 {
		t.Fatalf("expected raw token to be printed exactly once, found %d occurrences in stdout=%q", rawTokenOccurrences, stdout)
	}

	// TRIANGULATE: the persisted token hash must never equal the raw token,
	// and the raw token secret (not merely its non-secret token_prefix, which
	// design.md explicitly allows in audit metadata) must never appear
	// anywhere in audit metadata.
	persistedHash := store.tokens[0].TokenHash
	if persistedHash == "" || strings.Contains(stdout, persistedHash) {
		t.Fatalf("expected a persisted hash distinct from anything printed, got %q in stdout=%q", persistedHash, stdout)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	rawToken := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(rawToken, "egc_live_") || rawToken == store.tokens[0].TokenPrefix {
		t.Fatalf("expected last stdout line to be the full raw token (longer than its prefix %q), got %q", store.tokens[0].TokenPrefix, rawToken)
	}
	for _, event := range store.auditEvents {
		for key, value := range event.Metadata {
			if s, ok := value.(string); ok && s == rawToken {
				t.Fatalf("audit metadata key %q leaked the raw token secret: %q", key, s)
			}
		}
	}
}

// TestCloudBootstrapAdminRequiresTokenPepperForIssuance proves that
// --issue-token without ENGRAM_CLOUD_TOKEN_PEPPER configured fails clearly and
// makes no mutation (fail before create, not a partial bootstrap).
func TestCloudBootstrapAdminRequiresTokenPepperForIssuance(t *testing.T) {
	stubExitWithPanic(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN_PEPPER", "")
	store := &fakeCloudBootstrapStore{}
	calls := stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "dave", "--issue-token")
	_, stderr, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)

	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1 without token pepper, got recovered=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stderr, "ENGRAM_CLOUD_TOKEN_PEPPER") {
		t.Fatalf("expected stderr to mention the required pepper env var, got %q", stderr)
	}
	if *calls != 0 {
		t.Fatalf("expected cloud store to never be constructed when pepper validation fails first, got %d calls", *calls)
	}
	if store.createUserCalls != 0 {
		t.Fatalf("expected no user creation when token pepper is missing, got %d calls", store.createUserCalls)
	}
}

// TestCloudBootstrapAdminRejectsTooShortTokenPepper proves that --issue-token
// with a non-empty but too-short ENGRAM_CLOUD_TOKEN_PEPPER (e.g. a 1-char
// value) fails clearly via cloudauth.ErrTokenPepperTooShort, before touching
// the store, instead of silently accepting a weak HMAC key.
func TestCloudBootstrapAdminRejectsTooShortTokenPepper(t *testing.T) {
	stubExitWithPanic(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN_PEPPER", "too-short")
	store := &fakeCloudBootstrapStore{}
	calls := stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "kim", "--issue-token")
	_, stderr, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)

	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1 for a too-short token pepper, got recovered=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stderr, "at least 32 bytes") {
		t.Fatalf("expected stderr to clearly mention the minimum pepper length, got %q", stderr)
	}
	if *calls != 0 {
		t.Fatalf("expected cloud store to never be constructed when pepper validation fails first, got %d calls", *calls)
	}
	if store.createUserCalls != 0 {
		t.Fatalf("expected no user creation when the token pepper is too short, got %d calls", store.createUserCalls)
	}
}

// TestCloudBootstrapAdminGrantsProjects proves repeated --grant-project flags
// create one normalized grant per project for the newly created admin.
func TestCloudBootstrapAdminGrantsProjects(t *testing.T) {
	stubExitWithPanic(t)
	store := &fakeCloudBootstrapStore{}
	stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "erin",
		"--grant-project", "alpha", "--grant-project", "beta")
	_, _, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)
	if recovered != nil {
		t.Fatalf("expected bootstrap with grants to succeed, got %v", recovered)
	}

	if store.createGrantCalls != 2 {
		t.Fatalf("expected 2 grant creations, got %d", store.createGrantCalls)
	}
	projects := map[string]bool{}
	for _, g := range store.grants {
		projects[g.Project] = true
	}
	if !projects["alpha"] || !projects["beta"] {
		t.Fatalf("expected grants for alpha and beta, got %+v", store.grants)
	}
}

// TestCloudBootstrapAdminSurfacesInvalidGrantProjectStoreError proves that
// when --grant-project is syntactically non-empty (so it passes CLI arg
// parsing) but the store rejects it as invalid (e.g. a symbols-only value
// that cloudstore.normalizeCloudProjectGrant collapses to an empty
// project — see cloudstore's own normalization tests), the CLI surfaces the
// store-level validation error clearly with a non-zero exit instead of
// papering over it, and still leaves the admin durably created and audited
// (per the FIX 3 audit-ordering contract) since the admin creation itself
// succeeded before the grant step failed.
func TestCloudBootstrapAdminSurfacesInvalidGrantProjectStoreError(t *testing.T) {
	stubExitWithPanic(t)
	store := &fakeCloudBootstrapStore{createGrantErr: errNewCloudBootstrapFixture("cloudstore: project is required")}
	stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "ivan", "--grant-project", "!!!")
	_, stderr, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)

	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1 for an invalid grant-project value, got recovered=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stderr, "grant project") || !strings.Contains(stderr, "project is required") {
		t.Fatalf("expected stderr to clearly surface the store's grant validation error, got %q", stderr)
	}
	if store.createUserCalls != 1 {
		t.Fatalf("expected the admin to still be durably created despite the invalid grant, got %d user creations", store.createUserCalls)
	}
}

// TestCloudBootstrapAdminAcceptsMalformedEmailAsFreeformText documents a
// VERIFIED finding for FIX 5's "malformed --email" suggestion: neither the
// CLI nor cloudstore.CreateHumanUser (nor the cloud_human_users schema, which
// only has a UNIQUE constraint on email, no format check) validates email
// syntax anywhere in this codebase today. A "malformed" email is therefore
// NOT a store-level validation error being swallowed by the CLI — there is
// no such validation at any layer to surface. This test locks in that
// verified, pre-existing, intentional-by-omission behavior (free-form email
// text) so a future change does not silently start rejecting it without an
// explicit design decision.
func TestCloudBootstrapAdminAcceptsMalformedEmailAsFreeformText(t *testing.T) {
	stubExitWithPanic(t)
	store := &fakeCloudBootstrapStore{}
	stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "judy", "--email", "not-an-email-address")
	_, _, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)
	if recovered != nil {
		t.Fatalf("expected bootstrap to succeed with a syntactically malformed email (no format validation exists), got %v", recovered)
	}
	if store.createUserCalls != 1 {
		t.Fatalf("expected the admin to be created, got %d user creations", store.createUserCalls)
	}
	if store.users[0].Email != "not-an-email-address" {
		t.Fatalf("expected the malformed email to be stored verbatim as free-form text, got %q", store.users[0].Email)
	}
}

// TestCloudBootstrapAdminRejectsMissingUsername proves invalid input (missing
// required --username) fails fast with a usage error and never constructs the
// cloud store or touches any state.
func TestCloudBootstrapAdminRejectsMissingUsername(t *testing.T) {
	stubExitWithPanic(t)
	store := &fakeCloudBootstrapStore{}
	calls := stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin")
	_, stderr, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)

	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1 for missing --username, got recovered=%v stderr=%q", recovered, stderr)
	}
	if *calls != 0 {
		t.Fatalf("expected cloud store to never be constructed for invalid input, got %d calls", *calls)
	}
	if store.createUserCalls != 0 {
		t.Fatalf("expected no mutation for invalid input, got %d calls", store.createUserCalls)
	}
}

// TestCloudBootstrapAdminRejectsUnknownFlag proves an unrecognized flag is
// treated as invalid input rather than silently ignored.
func TestCloudBootstrapAdminRejectsUnknownFlag(t *testing.T) {
	stubExitWithPanic(t)
	store := &fakeCloudBootstrapStore{}
	calls := stubNewCloudBootstrapStore(t, store)

	withArgs(t, "engram", "cloud", "bootstrap", "admin", "--username", "frank", "--not-a-real-flag")
	_, _, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)

	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1 for unknown flag, got recovered=%v", recovered)
	}
	if *calls != 0 {
		t.Fatalf("expected cloud store to never be constructed for invalid input, got %d calls", *calls)
	}
}

// TestCloudBootstrapUnknownSubcommandIsRejected proves `engram cloud
// bootstrap <x>` for x != admin is rejected instead of silently no-op.
func TestCloudBootstrapUnknownSubcommandIsRejected(t *testing.T) {
	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "bootstrap", "service-account")
	_, _, recovered := captureOutputAndRecover(t, cmdCloudBootstrap)
	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1 for unsupported bootstrap subcommand, got %v", recovered)
	}
}
