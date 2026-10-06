package server

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/diagnostic"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

type callerReader struct {
	rows map[string]*store.Session
	err  error
}

func (r callerReader) GetSession(id string) (*store.Session, error) {
	if r.err != nil {
		return nil, r.err
	}
	if row := r.rows[id]; row != nil {
		return row, nil
	}
	return nil, sql.ErrNoRows
}
func TestCallerBindingAssessment(t *testing.T) {
	yes, no := true, false
	ended := "synthetic timestamp"
	for _, tc := range []struct {
		name, root, effective, project, reason, status string
		endRoot, endEffective                          bool
		host                                           *diagnostic.CallerHostContext
		lookupErr                                      error
	}{
		{name: "missing caller", reason: "caller_context_missing", status: "unknown"},
		{name: "fresh root", root: "missing", effective: "missing", project: "fixture", reason: "session_not_registered", status: "unknown"},
		{name: "missing effective", root: "root", effective: "missing", project: "fixture", reason: "effective_session_missing", status: "unknown"},
		{name: "conflicting project", root: "root", effective: "root", project: "other", reason: "session_project_conflict", status: "blocked"},
		{name: "noncanonical ordinal", root: "root", effective: "root:resume:02", project: "fixture", reason: "effective_mapping_invalid", status: "blocked"},
		{name: "ordinal one", root: "root", effective: "root:resume:1", project: "fixture", reason: "effective_mapping_invalid", status: "blocked"},
		{name: "foreign identity", root: "root", effective: "foreign", project: "fixture", reason: "effective_mapping_invalid", status: "blocked"},
		{name: "host absent", root: "root", effective: "root", project: "fixture", reason: "host_context_missing", status: "unknown"},
		{name: "live root", root: "root", effective: "root", project: "fixture", host: &diagnostic.CallerHostContext{&no, &no}, reason: "binding_observed", status: "ok"},
		{name: "ended continuation", root: "root", effective: "root:resume:2", project: "fixture", endRoot: true, endEffective: true, host: &diagnostic.CallerHostContext{&yes, &yes}, reason: "resume_required", status: "warning"},
		{name: "branch unavailable", root: "root", effective: "root", project: "fixture", endRoot: true, host: &diagnostic.CallerHostContext{&yes, &no}, reason: "ended_session_without_persistence", status: "blocked"},
		{name: "lookup unavailable", root: "root", effective: "root", project: "fixture", lookupErr: errors.New("private database path"), reason: "caller_binding_unavailable", status: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := map[string]*store.Session{"root": {Project: "fixture"}}
			if tc.endRoot {
				rows["root"].EndedAt = &ended
			}
			if tc.effective != "root" && tc.effective != "missing" && tc.effective != "" {
				rows[tc.effective] = &store.Session{Project: "fixture"}
				if tc.endEffective {
					rows[tc.effective].EndedAt = &ended
				}
			}
			got := diagnostic.AssessCallerBinding(callerReader{rows, tc.lookupErr}, diagnostic.CallerBindingInput{Project: tc.project, RuntimeSessionID: tc.root, EffectiveSessionID: tc.effective, HostContext: tc.host})
			if got.Status != tc.status || got.ReasonCode != tc.reason || got.WriteSuccessGuaranteed || strings.Contains(got.SafeNextStep, "private") {
				t.Fatalf("assessment = %#v", got)
			}
		})
	}
}
