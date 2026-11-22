package diagnostic

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Gentleman-Programming/engram/v3/internal/hostbind"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// LiveHostSessionReport observes a local host binding. Its result carries
// neither the identities it read nor authority to reopen or end a session.
func LiveHostSessionReport(ctx context.Context, s *store.Store, pid int) Report {
	build := func(code, status, severity, message, next string) Report {
		return buildReport("", []CheckResult{{
			CheckID:      "live_host_session",
			Result:       status,
			Severity:     severity,
			ReasonCode:   "live_host_session_" + code,
			Message:      message,
			Why:          "A live host needs an active session or a usable continuation for its next memory write.",
			SafeNextStep: next,
			Evidence:     mustJSON(map[string]bool{"host_binding_verified": code != "binding_unverified"}),
		}})
	}
	unverified := func() Report {
		return build("binding_unverified", StatusWarning, SeverityWarning, "Could not verify a live host binding for this PID.",
			"Confirm the host PID and that the CLI uses the same Engram data directory.")
	}
	const unavailable = "Check the configured Engram store and try the diagnostic again."
	start, err := hostbind.ProcessStart(pid)
	if err != nil {
		return unverified()
	}
	root, ok := hostbind.Lookup(s.DataDir(), pid)
	if !ok {
		return unverified()
	}
	session, err := s.GetSession(root)
	var resolved string
	if err == nil && session.EndedAt != nil {
		resolved, err = s.ResolveRuntimeSessionWithOwnershipModeContext(ctx, root, session.Project, session.Directory, session.OwnershipMode)
	}
	// Discard an observation if the PID or the host's binding changed while
	// reading the store, for example if the host cleared its conversation.
	current, ok := hostbind.Lookup(s.DataDir(), pid)
	end, startErr := hostbind.ProcessStart(pid)
	if startErr != nil || start != end || !ok || current != root {
		return unverified()
	}
	switch {
	case session == nil && errors.Is(err, sql.ErrNoRows):
		return build("session_missing", StatusWarning, SeverityWarning, "The verified host binding has no session in this store.", unavailable)
	case session != nil && errors.Is(err, sql.ErrNoRows):
		return build("ended", StatusWarning, SeverityWarning, "The verified live host is bound to an ended session and has no active continuation.",
			"Use the host's normal resume flow to register a live continuation.")
	case err != nil:
		return build("state_unavailable", StatusError, SeverityError, "Could not verify the session state for the live host binding.", unavailable)
	case resolved != "":
		return build("continuation_active", StatusOK, SeverityInfo, "The verified host has an active continuation.", "No action required.")
	default:
		return build("active", StatusOK, SeverityInfo, "The verified host has an active session.", "No action required.")
	}
}
