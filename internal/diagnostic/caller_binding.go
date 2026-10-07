package diagnostic

import (
	"database/sql"
	"errors"
	"math/big"
	"strings"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// CallerBindingInput is host evidence, never authorization to resume or write.
type CallerBindingInput struct {
	Project            string             `json:"project,omitempty"`
	RuntimeSessionID   string             `json:"runtime_session_id,omitempty"`
	EffectiveSessionID string             `json:"effective_session_id,omitempty"`
	HostContext        *CallerHostContext `json:"host_context,omitempty"`
}

type CallerHostContext struct {
	AppendEntryAvailable *bool `json:"append_entry_available"`
	BranchAvailable      *bool `json:"branch_available"`
}

// CallerBindingAssessment deliberately contains no identifying evidence.
type CallerBindingAssessment struct {
	Status                 string `json:"status"`
	RootState              string `json:"root_state"`
	EffectiveState         string `json:"effective_state"`
	ReasonCode             string `json:"reason_code"`
	SafeNextStep           string `json:"safe_next_step"`
	WriteSuccessGuaranteed bool   `json:"write_success_guaranteed"`
}

type CallerSessionReader interface {
	GetSession(string) (*store.Session, error)
}

// AssessCallerBinding reads only the supplied identities. It never selects a
// continuation, registers sessions, renews leases, or predicts write success.
func AssessCallerBinding(reader CallerSessionReader, in CallerBindingInput) CallerBindingAssessment {
	out := CallerBindingAssessment{RootState: "unknown", EffectiveState: "unknown"}
	result := func(status, reason, next string) CallerBindingAssessment {
		out.Status, out.ReasonCode, out.SafeNextStep = status, reason, next
		return out
	}
	const collect = "Collect caller context without registering a session."
	const inspect = "Inspect the runtime binding; diagnosis does not repair it."
	const resume = "Resume through a host that can persist effective identity; do not reopen the ended session."
	if strings.TrimSpace(in.RuntimeSessionID) == "" || strings.TrimSpace(in.EffectiveSessionID) == "" || strings.TrimSpace(in.Project) == "" {
		return result("unknown", "caller_context_missing", collect)
	}
	project, err := projectpkg.Resolve(projectpkg.ResolutionOptions{Mode: projectpkg.ResolutionExplicit, Explicit: in.Project})
	if err != nil {
		return result("unknown", "caller_context_missing", collect)
	}
	unavailable := func() CallerBindingAssessment {
		return result("unknown", "caller_binding_unavailable", "Verify support and availability of the caller diagnostic.")
	}
	if reader == nil {
		return unavailable()
	}
	root, err := reader.GetSession(in.RuntimeSessionID)
	if errors.Is(err, sql.ErrNoRows) {
		out.RootState = "missing"
		return result("unknown", "session_not_registered", "Use the normal runtime registration path; diagnosis does not register sessions.")
	}
	if err != nil || root == nil {
		return unavailable()
	}
	state := func(row *store.Session) string {
		if row.EndedAt != nil {
			return "ended"
		}
		return "active"
	}
	out.RootState = state(root)
	effective := root
	if in.EffectiveSessionID != in.RuntimeSessionID {
		effective, err = reader.GetSession(in.EffectiveSessionID)
		if errors.Is(err, sql.ErrNoRows) {
			out.EffectiveState = "missing"
			return result("unknown", "effective_session_missing", inspect)
		}
		if err != nil || effective == nil {
			return unavailable()
		}
	}
	out.EffectiveState = state(effective)
	for _, row := range []*store.Session{root, effective} {
		persisted, err := projectpkg.Resolve(projectpkg.ResolutionOptions{Mode: projectpkg.ResolutionExplicit, Explicit: row.Project})
		if err != nil || persisted.Project != project.Project {
			return result("blocked", "session_project_conflict", inspect)
		}
	}
	if in.EffectiveSessionID != in.RuntimeSessionID {
		prefix := in.RuntimeSessionID + ":resume:"
		suffix := strings.TrimPrefix(in.EffectiveSessionID, prefix)
		n, ok := new(big.Int).SetString(suffix, 10)
		if !strings.HasPrefix(in.EffectiveSessionID, prefix) || !ok || n.Cmp(big.NewInt(2)) < 0 || n.String() != suffix {
			return result("blocked", "effective_mapping_invalid", inspect)
		}
	}
	if in.HostContext == nil || in.HostContext.AppendEntryAvailable == nil || in.HostContext.BranchAvailable == nil {
		return result("unknown", "host_context_missing", collect)
	}
	if out.EffectiveState == "active" {
		return result("ok", "binding_observed", "No action required; a later write is not guaranteed.")
	}
	// Go's runtime registration supports continuation identities, but the host
	// must persist that identity before subsequent writes can use it safely.
	if !*in.HostContext.AppendEntryAvailable || !*in.HostContext.BranchAvailable {
		return result("blocked", "ended_session_without_persistence", resume)
	}
	return result("warning", "resume_required", resume)
}
