package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// cloudBootstrapAuditAction is the audit action recorded for every CLI
// bootstrap attempt (accepted or denied), per design.md's MVP audit action
// list ("bootstrap.cli").
const cloudBootstrapAuditAction = "bootstrap.cli"

const (
	cloudBootstrapAuditOutcomeSuccess = "success"
	cloudBootstrapAuditOutcomeDenied  = "denied"
	// cloudBootstrapAuditOutcomeError marks a bootstrap.cli audit event
	// recording that an optional post-admin-creation step (project grant,
	// token issuance, or the completion-detail audit itself) failed. It is
	// distinct from "denied" (a pre-creation refusal, e.g. duplicate first
	// admin): by the time this outcome is used, the admin has already been
	// durably created and durably audited (see cmdCloudBootstrapAdmin).
	cloudBootstrapAuditOutcomeError = "error"
)

// cloudBootstrapStore is the storage boundary used by `engram cloud bootstrap
// admin`. It is satisfied by *cloudstore.CloudStore and by test fakes, using
// the exact same store methods (and therefore the exact same atomic
// first-admin guard, audit path, and grant normalization) as the admin
// API/dashboard handlers in internal/cloud/cloudserver — this command does
// not fork a parallel bootstrap path.
//
// CreateFirstAdminHumanUser (not a separate HasActiveAdmin-then-
// CreateHumanUser sequence) MUST be used for first-admin bootstrap: doing
// the check and the create as two separate calls reintroduces a
// check-then-act (TOCTOU) race where two concurrent bootstrap attempts could
// both observe "no active admin" and both create a first admin.
type cloudBootstrapStore interface {
	CreateFirstAdminHumanUser(ctx context.Context, params cloudstore.CreateHumanUserParams) (cloudstore.HumanUser, error)
	CreatePrincipalTokenWithAudit(ctx context.Context, params cloudstore.CreatePrincipalTokenParams, audit cloudstore.AuthAuditEvent) (cloudstore.PrincipalToken, error)
	CreateProjectGrant(ctx context.Context, params cloudstore.CreateProjectGrantParams) (cloudstore.ProjectGrant, error)
	InsertAuthAuditEvent(ctx context.Context, event cloudstore.AuthAuditEvent) error
	Close() error
}

// newCloudBootstrapStore is injectable for testing so CLI bootstrap tests
// never require a live Postgres instance. In production it opens the same
// cloud runtime database used by `engram cloud serve`.
var newCloudBootstrapStore = func(cfg cloud.Config) (cloudBootstrapStore, error) {
	return cloudstore.New(cfg)
}

type cloudBootstrapAdminArgs struct {
	username       string
	email          string
	grantProjects  []string
	issueToken     bool
	issueTokenName string
}

func cmdCloudBootstrap() {
	if len(os.Args) < 4 {
		printCloudBootstrapUsage()
		fmt.Fprintln(os.Stderr, "error: a bootstrap subcommand is required")
		exitFunc(1)
		return
	}
	switch strings.TrimSpace(os.Args[3]) {
	case "admin":
		cmdCloudBootstrapAdmin()
	case "--help", "-h", "help":
		printCloudBootstrapUsage()
	default:
		printCloudBootstrapUsage()
		fmt.Fprintf(os.Stderr, "error: unknown cloud bootstrap subcommand: %s\n", os.Args[3])
		exitFunc(1)
	}
}

func printCloudBootstrapUsage() {
	fmt.Println("usage: engram cloud bootstrap admin --username <name> [--email <email>] [--grant-project <project>]... [--issue-token [name]]")
	fmt.Println("creates the first managed admin for a self-hosted cloud deployment")
}

func cmdCloudBootstrapAdmin() {
	args, err := parseCloudBootstrapAdminArgs(os.Args[4:])
	if err != nil {
		printCloudBootstrapUsage()
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		exitFunc(1)
		return
	}

	runtimeCfg := cloud.ConfigFromEnv()

	// Validate the dedicated managed-token pepper BEFORE touching the store,
	// so a misconfigured deployment fails clearly instead of partially
	// bootstrapping an admin without the token it was asked to issue.
	var hasher *cloudauth.ManagedTokenHasher
	if args.issueToken {
		pepper := strings.TrimSpace(runtimeCfg.TokenPepper)
		if pepper == "" {
			fmt.Fprintln(os.Stderr, "error: --issue-token requires ENGRAM_CLOUD_TOKEN_PEPPER to be configured (a dedicated cloud token pepper, distinct from ENGRAM_JWT_SECRET)")
			exitFunc(1)
			return
		}
		h, herr := cloudauth.NewManagedTokenHasher([]byte(pepper))
		if herr != nil {
			fatal(fmt.Errorf("cloud bootstrap admin: %w", herr))
			return
		}
		hasher = h
	}

	cs, err := newCloudBootstrapStore(runtimeCfg)
	if err != nil {
		fatal(fmt.Errorf("cloud bootstrap admin: connect cloud store: %w", err))
		return
	}
	defer cs.Close()

	ctx := context.Background()

	// Atomic check-and-create: cs.CreateFirstAdminHumanUser checks for an
	// existing active admin and creates the new admin within a single
	// transaction (see cloudstore.CreateFirstAdminHumanUser), instead of the
	// old HasActiveAdmin-then-CreateHumanUser sequence, which left a
	// check-then-act (TOCTOU) window where two concurrent bootstrap attempts
	// could both observe "no active admin" and both create a first admin.
	user, err := cs.CreateFirstAdminHumanUser(ctx, cloudstore.CreateHumanUserParams{
		Username:    args.username,
		Email:       args.email,
		DisplayName: args.username,
	})
	if err != nil {
		if errors.Is(err, cloudstore.ErrAdminAlreadyExists) {
			// Best-effort: this is a rejection, not a mutation, so a store
			// hiccup on the audit insert should not change the refusal outcome.
			if aerr := recordCloudBootstrapAudit(ctx, cs, "", cloudBootstrapAuditOutcomeDenied, "managed_admin_already_exists", map[string]any{"created_admin": false, "username": args.username}); aerr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to record bootstrap denial audit event: %v\n", aerr)
			}
			fmt.Fprintln(os.Stderr, "error: a managed admin already exists; refusing to create a duplicate first admin via CLI bootstrap")
			exitFunc(1)
			return
		}
		fatal(fmt.Errorf("cloud bootstrap admin: create admin user: %w", err))
		return
	}

	// This is the authoritative mutation event, recorded IMMEDIATELY after
	// admin creation succeeds — before any optional grant/token step runs —
	// so the newly-created admin is always audited even if a later optional
	// step below fails. Fail closed (mirrors recordAdminAudit's
	// mutation-then-audit-then-fail convention in
	// internal/cloud/cloudserver/admin_handlers.go): a lost audit event for a
	// brand-new admin must never happen silently.
	adminAuditMetadata := map[string]any{"created_admin": true, "username": args.username}
	if err := recordCloudBootstrapAudit(ctx, cs, user.PrincipalID, cloudBootstrapAuditOutcomeSuccess, "", adminAuditMetadata); err != nil {
		fatal(fmt.Errorf("cloud bootstrap admin: created admin %s (principal_id=%s) but failed to record the required bootstrap audit event: %w", args.username, user.PrincipalID, err))
		return
	}

	grants := make([]cloudstore.ProjectGrant, 0, len(args.grantProjects))
	for _, project := range args.grantProjects {
		grant, gerr := cs.CreateProjectGrant(ctx, cloudstore.CreateProjectGrantParams{
			PrincipalID:          user.PrincipalID,
			Project:              project,
			GrantedByPrincipalID: user.PrincipalID,
		})
		if gerr != nil {
			// The admin is already durably created and durably audited above.
			// This is a failed OPTIONAL step: record a compensating
			// failure/partial audit event (best-effort — the mandatory audit
			// trail already exists regardless of whether this one succeeds)
			// and still exit non-zero; never fail silently.
			recordCloudBootstrapAuditBestEffort(ctx, cs, user.PrincipalID, cloudBootstrapAuditOutcomeError, "grant_project_failed", map[string]any{"created_admin": true, "username": args.username, "failed_step": "grant_project", "project": project})
			fatal(fmt.Errorf("cloud bootstrap admin: grant project %q: %w", project, gerr))
			return
		}
		grants = append(grants, grant)
	}

	var rawToken string
	if args.issueToken {
		managedToken, terr := cloudauth.GenerateManagedToken("live")
		if terr != nil {
			recordCloudBootstrapAuditBestEffort(ctx, cs, user.PrincipalID, cloudBootstrapAuditOutcomeError, "token_generate_failed", map[string]any{"created_admin": true, "username": args.username, "failed_step": "issue_token"})
			fatal(fmt.Errorf("cloud bootstrap admin: generate token: %w", terr))
			return
		}
		tokenHash, herr := hasher.Hash(managedToken.Raw)
		if herr != nil {
			recordCloudBootstrapAuditBestEffort(ctx, cs, user.PrincipalID, cloudBootstrapAuditOutcomeError, "token_hash_failed", map[string]any{"created_admin": true, "username": args.username, "failed_step": "issue_token"})
			fatal(fmt.Errorf("cloud bootstrap admin: hash token: %w", herr))
			return
		}
		name := strings.TrimSpace(args.issueTokenName)
		if name == "" {
			name = "cli-bootstrap"
		}
		metadata := cloudBootstrapCompletionMetadata(args.username, args.issueToken, grants)
		metadata["token_prefix"] = managedToken.Prefix
		_, createTokenErr := cs.CreatePrincipalTokenWithAudit(ctx, cloudstore.CreatePrincipalTokenParams{
			PrincipalID:          user.PrincipalID,
			TokenPrefix:          managedToken.Prefix,
			TokenHash:            tokenHash,
			Name:                 name,
			CreatedByPrincipalID: user.PrincipalID,
		}, cloudstore.AuthAuditEvent{
			ActorSource:       string(cloudauth.PrincipalSourceBootstrapCLI),
			TargetPrincipalID: strings.TrimSpace(user.PrincipalID),
			Action:            cloudBootstrapAuditAction,
			Outcome:           cloudBootstrapAuditOutcomeSuccess,
			ReasonCode:        "bootstrap_completed",
			Metadata:          metadata,
		})
		if createTokenErr != nil {
			// The managed token and its success/completion audit are one atomic
			// unit: if either write fails, nothing is durably minted and nothing is
			// safe to disclose.
			recordCloudBootstrapAuditBestEffort(ctx, cs, user.PrincipalID, cloudBootstrapAuditOutcomeError, "token_create_or_audit_failed", map[string]any{"created_admin": true, "username": args.username, "failed_step": "issue_token"})
			fatal(fmt.Errorf("cloud bootstrap admin: create token with completion audit: %w", createTokenErr))
			return
		}
		rawToken = managedToken.Raw
	}

	fmt.Printf("✓ Managed admin created: username=%s principal_id=%s\n", user.Username, user.PrincipalID)
	for _, grant := range grants {
		fmt.Printf("✓ Project grant created: project=%s\n", grant.Project)
	}
	if args.issueToken {
		fmt.Println()
		fmt.Println("Token issued — SHOWN ONCE, copy and store it now, it cannot be retrieved again:")
		fmt.Println(rawToken)
	}

	// Completion-detail audit for grant-only bootstraps. Token bootstraps record
	// this same success/completion audit atomically with token persistence above,
	// before the raw token is disclosed.
	if len(grants) > 0 && !args.issueToken {
		metadata := cloudBootstrapCompletionMetadata(args.username, args.issueToken, grants)
		if err := recordCloudBootstrapAudit(ctx, cs, user.PrincipalID, cloudBootstrapAuditOutcomeSuccess, "bootstrap_completed", metadata); err != nil {
			recordCloudBootstrapAuditBestEffort(ctx, cs, user.PrincipalID, cloudBootstrapAuditOutcomeError, "completion_audit_failed", map[string]any{"created_admin": true, "username": args.username})
			fatal(fmt.Errorf("cloud bootstrap admin: created admin %s (principal_id=%s) and issued optional grants/token but failed to record the completion audit event: %w", args.username, user.PrincipalID, err))
			return
		}
	}
}

func cloudBootstrapCompletionMetadata(username string, issuedToken bool, grants []cloudstore.ProjectGrant) map[string]any {
	metadata := map[string]any{
		"created_admin": true,
		"username":      username,
		"issued_token":  issuedToken,
	}
	if len(grants) > 0 {
		projects := make([]string, 0, len(grants))
		for _, grant := range grants {
			projects = append(projects, grant.Project)
		}
		metadata["grant_projects"] = projects
	}
	return metadata
}

func recordCloudBootstrapAudit(ctx context.Context, store cloudBootstrapStore, principalID, outcome, reasonCode string, metadata map[string]any) error {
	return store.InsertAuthAuditEvent(ctx, cloudstore.AuthAuditEvent{
		ActorSource:       string(cloudauth.PrincipalSourceBootstrapCLI),
		TargetPrincipalID: strings.TrimSpace(principalID),
		Action:            cloudBootstrapAuditAction,
		Outcome:           outcome,
		ReasonCode:        strings.TrimSpace(reasonCode),
		Metadata:          metadata,
	})
}

// recordCloudBootstrapAuditBestEffort attempts to record a bootstrap.cli
// audit event and only surfaces a warning on failure instead of failing the
// caller. It is used for compensating failure/completion events written
// AFTER the mandatory admin-creation event has already been durably
// recorded (see cmdCloudBootstrapAdmin) — the audit trail for the admin's
// existence is never lost even if one of these secondary events fails.
func recordCloudBootstrapAuditBestEffort(ctx context.Context, store cloudBootstrapStore, principalID, outcome, reasonCode string, metadata map[string]any) {
	if err := recordCloudBootstrapAudit(ctx, store, principalID, outcome, reasonCode, metadata); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to record bootstrap %s audit event: %v\n", reasonCode, err)
	}
}

func parseCloudBootstrapAdminArgs(args []string) (cloudBootstrapAdminArgs, error) {
	var out cloudBootstrapAdminArgs
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--username":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--username requires a value")
			}
			i++
			out.username = strings.TrimSpace(args[i])
		case "--email":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--email requires a value")
			}
			i++
			out.email = strings.TrimSpace(args[i])
		case "--grant-project":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--grant-project requires a value")
			}
			i++
			project := strings.TrimSpace(args[i])
			if project == "" {
				return out, fmt.Errorf("--grant-project requires a non-empty value")
			}
			out.grantProjects = append(out.grantProjects, project)
		case "--issue-token":
			out.issueToken = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				out.issueTokenName = strings.TrimSpace(args[i])
			}
		default:
			return out, fmt.Errorf("unknown flag: %s", args[i])
		}
	}
	if strings.TrimSpace(out.username) == "" {
		return out, fmt.Errorf("--username is required")
	}
	return out, nil
}
