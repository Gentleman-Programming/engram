package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
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
	const collect = "Collect caller context without registering a session."
	const register = "Use the normal runtime registration path; diagnosis does not register sessions."
	const inspect = "Inspect the runtime binding; diagnosis does not repair it."
	const observe = "No action required; a later write is not guaranteed."
	const resume = "Resume through a host that can persist effective identity; do not reopen the ended session."
	const verify = "Verify support and availability of the caller diagnostic."
	for _, tc := range []struct {
		name, root, effective, project, reason, status string
		rootState, effectiveState, next                string
		endRoot, endEffective                          bool
		host                                           *diagnostic.CallerHostContext
		lookupErr                                      error
	}{
		{name: "missing caller", reason: "caller_context_missing", status: "unknown", rootState: "unknown", effectiveState: "unknown", next: collect},
		{name: "fresh root", root: "missing", effective: "missing", project: "fixture", reason: "session_not_registered", status: "unknown", rootState: "missing", effectiveState: "unknown", next: register},
		{name: "missing effective", root: "root", effective: "missing", project: "fixture", reason: "effective_session_missing", status: "unknown", rootState: "active", effectiveState: "missing", next: inspect},
		{name: "conflicting project", root: "root", effective: "root", project: "other", reason: "session_project_conflict", status: "blocked", rootState: "active", effectiveState: "active", next: inspect},
		{name: "noncanonical ordinal", root: "root", effective: "root:resume:02", project: "fixture", reason: "effective_mapping_invalid", status: "blocked", rootState: "active", effectiveState: "active", next: inspect},
		{name: "ordinal one", root: "root", effective: "root:resume:1", project: "fixture", reason: "effective_mapping_invalid", status: "blocked", rootState: "active", effectiveState: "active", next: inspect},
		{name: "foreign identity", root: "root", effective: "foreign", project: "fixture", reason: "effective_mapping_invalid", status: "blocked", rootState: "active", effectiveState: "active", next: inspect},
		{name: "host absent", root: "root", effective: "root", project: "fixture", reason: "host_context_missing", status: "unknown", rootState: "active", effectiveState: "active", next: collect},
		{name: "live root", root: "root", effective: "root", project: "fixture", host: &diagnostic.CallerHostContext{&no, &no}, reason: "binding_observed", status: "ok", rootState: "active", effectiveState: "active", next: observe},
		{name: "ended continuation", root: "root", effective: "root:resume:2", project: "fixture", endRoot: true, endEffective: true, host: &diagnostic.CallerHostContext{&yes, &yes}, reason: "resume_required", status: "warning", rootState: "ended", effectiveState: "ended", next: resume},
		{name: "ended root with live continuation", root: "root", effective: "root:resume:2", project: "fixture", endRoot: true, host: &diagnostic.CallerHostContext{&yes, &yes}, reason: "binding_observed", status: "ok", rootState: "ended", effectiveState: "active", next: observe},
		{name: "branch unavailable", root: "root", effective: "root", project: "fixture", endRoot: true, host: &diagnostic.CallerHostContext{&yes, &no}, reason: "ended_session_without_persistence", status: "blocked", rootState: "ended", effectiveState: "ended", next: resume},
		{name: "lookup unavailable", root: "root", effective: "root", project: "fixture", lookupErr: errors.New("private database path"), reason: "caller_binding_unavailable", status: "unknown", rootState: "unknown", effectiveState: "unknown", next: verify},
		{name: "both persistence APIs unavailable", root: "root", effective: "root", project: "fixture", endRoot: true, host: &diagnostic.CallerHostContext{&no, &no}, reason: "ended_session_without_persistence", status: "blocked", rootState: "ended", effectiveState: "ended", next: resume},
		{name: "append evidence missing", root: "root", effective: "root", project: "fixture", host: &diagnostic.CallerHostContext{BranchAvailable: &yes}, reason: "host_context_missing", status: "unknown", rootState: "active", effectiveState: "active", next: collect},
		{name: "branch evidence missing", root: "root", effective: "root", project: "fixture", host: &diagnostic.CallerHostContext{AppendEntryAvailable: &yes}, reason: "host_context_missing", status: "unknown", rootState: "active", effectiveState: "active", next: collect},
		{name: "active root with ended continuation", root: "root", effective: "root:resume:2", project: "fixture", endEffective: true, host: &diagnostic.CallerHostContext{&yes, &yes}, reason: "resume_required", status: "warning", rootState: "active", effectiveState: "ended", next: resume},
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
			want := diagnostic.CallerBindingAssessment{Status: tc.status, ReasonCode: tc.reason, RootState: tc.rootState, EffectiveState: tc.effectiveState, SafeNextStep: tc.next}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("assessment = %#v, want %#v", got, want)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "fixture") {
				t.Fatalf("assessment leaked evidence: %s", raw)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			keys := []string{"status", "root_state", "effective_state", "reason_code", "safe_next_step", "write_success_guaranteed"}
			if len(fields) != len(keys) {
				t.Fatalf("unexpected assessment fields: %s", raw)
			}
			for _, key := range keys {
				if _, ok := fields[key]; !ok {
					t.Fatalf("missing assessment field %s: %s", key, raw)
				}
			}
		})
	}
}

func TestCallerBindingProjectValidation(t *testing.T) {
	yes := true
	for _, tc := range []struct {
		name, caller, rootProject, effectiveProject, status, reason string
	}{
		{"matching invalid path", "/", "/", "", "unknown", "caller_context_missing"},
		{"caller path", "private/name", "fixture", "", "unknown", "caller_context_missing"},
		{"caller backslash", `private\name`, "fixture", "", "unknown", "caller_context_missing"},
		{"caller control", "private\x00name", "fixture", "", "unknown", "caller_context_missing"},
		{"invalid root", "fixture", "/", "", "blocked", "session_project_conflict"},
		{"invalid effective", "fixture", "fixture", `private\name`, "blocked", "session_project_conflict"},
		{"normalized valid names", " FIX--TURE__NAME ", "fix-ture_name", " Fix--Ture__Name ", "ok", "binding_observed"},
		{"valid conflict", "other", "fixture", "", "blocked", "session_project_conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := map[string]*store.Session{"root": {Project: tc.rootProject}}
			effective := "root"
			if tc.effectiveProject != "" {
				effective = "root:resume:2"
				rows[effective] = &store.Session{Project: tc.effectiveProject}
			}
			before := make(map[string]store.Session)
			for id, row := range rows {
				before[id] = *row
			}
			got := diagnostic.AssessCallerBinding(callerReader{rows: rows}, diagnostic.CallerBindingInput{
				Project: tc.caller, RuntimeSessionID: "root", EffectiveSessionID: effective,
				HostContext: &diagnostic.CallerHostContext{&yes, &yes},
			})
			state, next := "active", "Inspect the runtime binding; diagnosis does not repair it."
			switch tc.status {
			case "unknown":
				state, next = "unknown", "Collect caller context without registering a session."
			case "ok":
				next = "No action required; a later write is not guaranteed."
			}
			want := diagnostic.CallerBindingAssessment{Status: tc.status, RootState: state, EffectiveState: state, ReasonCode: tc.reason, SafeNextStep: next}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("assessment = %#v, want %#v", got, want)
			}
			body, err := json.Marshal(got)
			if err != nil || strings.Contains(string(body), "private") || strings.Contains(string(body), "fixture") || strings.Contains(string(body), "root:") {
				t.Fatalf("unsafe assessment: %s (%v)", body, err)
			}
			for id, row := range rows {
				if !reflect.DeepEqual(*row, before[id]) {
					t.Fatal("diagnosis changed a supplied session")
				}
			}
		})
	}
}
