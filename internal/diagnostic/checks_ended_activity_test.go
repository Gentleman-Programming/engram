package diagnostic

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// seedDiagnosticSession writes a session row directly so ended_at can be set to
// a fixed instant instead of the store's current time. An empty endedAt leaves
// the session open.
func seedDiagnosticSession(t *testing.T, s *store.Store, id, project, endedAt string) {
	t.Helper()
	var end any
	if endedAt != "" {
		end = endedAt
	}
	if _, err := s.DB().Exec(
		`INSERT INTO sessions (id, project, ownership_mode, directory, started_at, ended_at) VALUES (?, ?, 'project_owned', '/work/engram', ?, ?)`,
		id, project, "2026-10-12 22:00:00", end,
	); err != nil {
		t.Fatalf("insert session %q: %v", id, err)
	}
}

// seedDiagnosticPrompt writes a prompt row with an explicit creation time.
func seedDiagnosticPrompt(t *testing.T, s *store.Store, syncID, sessionID, project, createdAt string) {
	t.Helper()
	if _, err := s.DB().Exec(
		`INSERT INTO user_prompts (sync_id, session_id, content, project, created_at) VALUES (?, ?, 'hello', ?, ?)`,
		syncID, sessionID, project, createdAt,
	); err != nil {
		t.Fatalf("insert prompt %q: %v", syncID, err)
	}
}

func TestEndedSessionPostEndActivityCheck(t *testing.T) {
	tests := []struct {
		name          string
		project       string
		scopeProject  string
		sessions      []seedSession
		prompts       []seedPrompt
		wantStatus    string
		wantFindings  int
		wantSessionID string
		wantPrompts   int64
		wantEndedAt   string
	}{
		{
			name:    "ended root with post-end prompts and no continuation warns",
			project: "engram",
			sessions: []seedSession{
				{id: "host-a", project: "engram", endedAt: "2026-10-12 22:38:06"},
			},
			prompts: []seedPrompt{
				{sessionID: "host-a", project: "engram", createdAt: "2026-10-12 22:40:00"},
				{sessionID: "host-a", project: "engram", createdAt: "2026-10-12 22:41:00"},
			},
			wantStatus:    StatusWarning,
			wantFindings:  1,
			wantSessionID: "host-a",
			wantPrompts:   2,
		},
		{
			name:    "ended root with an open continuation is ok",
			project: "engram",
			sessions: []seedSession{
				{id: "host-b", project: "engram", endedAt: "2026-10-12 22:38:06"},
				{id: "host-b:resume:2", project: "engram", endedAt: ""},
			},
			prompts: []seedPrompt{
				{sessionID: "host-b", project: "engram", createdAt: "2026-10-12 22:40:00"},
			},
			wantStatus: StatusOK,
		},
		{
			name:    "ended root without post-end activity is ok",
			project: "engram",
			sessions: []seedSession{
				{id: "host-c", project: "engram", endedAt: "2026-10-12 22:38:06"},
			},
			prompts: []seedPrompt{
				{sessionID: "host-c", project: "engram", createdAt: "2026-10-12 22:30:00"},
			},
			wantStatus: StatusOK,
		},
		{
			name:    "closed continuation with post-end prompts reports the family once",
			project: "engram",
			sessions: []seedSession{
				{id: "host-d", project: "engram", endedAt: "2026-10-12 22:38:06"},
				{id: "host-d:resume:2", project: "engram", endedAt: "2026-10-12 22:39:00"},
			},
			prompts: []seedPrompt{
				{sessionID: "host-d", project: "engram", createdAt: "2026-10-12 22:40:00"},
				{sessionID: "host-d:resume:2", project: "engram", createdAt: "2026-10-12 22:41:00"},
			},
			wantStatus:    StatusWarning,
			wantFindings:  1,
			wantSessionID: "host-d",
			wantPrompts:   2,
		},
		{
			name:    "activity in another project is not reported",
			project: "engram",
			sessions: []seedSession{
				{id: "host-e", project: "other", endedAt: "2026-10-12 22:38:06"},
			},
			prompts: []seedPrompt{
				{sessionID: "host-e", project: "other", createdAt: "2026-10-12 22:40:00"},
			},
			wantStatus: StatusOK,
		},
		{
			name:    "a later continuation that covered the activity clears the warning",
			project: "engram",
			sessions: []seedSession{
				{id: "host-f", project: "engram", endedAt: "2026-10-12 22:38:06"},
				{id: "host-f:resume:2", project: "engram", endedAt: "2026-10-12 22:45:00"},
			},
			prompts: []seedPrompt{
				{sessionID: "host-f", project: "engram", createdAt: "2026-10-12 22:40:00"},
			},
			wantStatus: StatusOK,
		},
		{
			name:    "activity newer than the latest family end is still reported",
			project: "engram",
			sessions: []seedSession{
				{id: "host-g", project: "engram", endedAt: "2026-10-12 22:38:06"},
				{id: "host-g:resume:2", project: "engram", endedAt: "2026-10-12 22:45:00"},
			},
			prompts: []seedPrompt{
				{sessionID: "host-g", project: "engram", createdAt: "2026-10-12 22:40:00"},
				{sessionID: "host-g", project: "engram", createdAt: "2026-10-12 22:50:00"},
			},
			wantStatus:    StatusWarning,
			wantFindings:  1,
			wantSessionID: "host-g",
			wantPrompts:   1,
			wantEndedAt:   "2026-10-12 22:45:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newDiagnosticTestStore(t)
			for _, session := range tt.sessions {
				seedDiagnosticSession(t, s, session.id, session.project, session.endedAt)
			}
			for i, prompt := range tt.prompts {
				seedDiagnosticPrompt(t, s, fmt.Sprintf("prompt-%d", i), prompt.sessionID, prompt.project, prompt.createdAt)
			}

			report, err := NewRunner().RunOne(context.Background(), Scope{Store: s, Project: tt.project}, CheckEndedSessionPostEndActivity)
			if err != nil {
				t.Fatalf("RunOne: %v", err)
			}
			if report.Status != tt.wantStatus {
				t.Fatalf("status=%q, want %q: %+v", report.Status, tt.wantStatus, report)
			}
			if len(report.Checks[0].Findings) != tt.wantFindings {
				t.Fatalf("findings=%+v, want %d", report.Checks[0].Findings, tt.wantFindings)
			}
			if tt.wantFindings == 0 {
				return
			}
			finding := report.Checks[0].Findings[0]
			if finding.Severity != SeverityWarning {
				t.Fatalf("severity=%q, want %q", finding.Severity, SeverityWarning)
			}
			if finding.RequiresConfirmation {
				t.Fatal("diagnostic-only finding must not require confirmation")
			}
			var evidence struct {
				SessionID          string `json:"session_id"`
				EndedAt            string `json:"ended_at"`
				PostEndPromptCount int64  `json:"post_end_prompt_count"`
			}
			if err := json.Unmarshal(finding.Evidence, &evidence); err != nil {
				t.Fatalf("decode finding evidence: %v", err)
			}
			if evidence.SessionID != tt.wantSessionID || evidence.PostEndPromptCount != tt.wantPrompts {
				t.Fatalf("evidence=%+v, want session_id=%q prompts=%d", evidence, tt.wantSessionID, tt.wantPrompts)
			}
			if tt.wantEndedAt != "" && evidence.EndedAt != tt.wantEndedAt {
				t.Fatalf("evidence ended_at=%q, want %q", evidence.EndedAt, tt.wantEndedAt)
			}
		})
	}
}

type seedSession struct {
	id      string
	project string
	endedAt string
}

type seedPrompt struct {
	sessionID string
	project   string
	createdAt string
}
