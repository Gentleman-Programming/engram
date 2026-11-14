package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
	sqlite "modernc.org/sqlite"
)

func TestWriteProjectErrorClassification(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "missing", "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	cantopen := db.Ping()
	var sqliteErr *sqlite.Error
	if !errors.As(cantopen, &sqliteErr) || sqliteErr.Code()&255 != 14 {
		t.Fatalf("expected genuine SQLite CANTOPEN: %v", cantopen)
	}
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"cantopen", cantopen, "project_resolution_failed"},
		{"unexpected", errors.New("synthetic resolution failure"), "project_resolution_failed"},
		{"ambiguous", fmt.Errorf("resolve: %w", project.ErrAmbiguousProject), "ambiguous_project"},
		{"invalid-config", project.ErrInvalidConfig, "invalid_project_config"},
		{"generation", store.ErrDatabaseGenerationChanged, "database_generation_changed"},
		{"ownership", store.ErrProjectOwnershipAmbiguous, "session_project_ownership_ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := writeProjectErrorResult(NewSessionActivity(time.Minute), "synthetic-session", project.DetectionResult{AvailableProjects: []string{"synthetic-a", "synthetic-b"}}, tc.err)
			body := callResultJSON(t, result)
			if !result.IsError || body["error_code"] != tc.code || !strings.Contains(body["message"].(string), tc.err.Error()) {
				t.Fatalf("classification or error detail changed: %v", body)
			}
			_, hasToken := body["recovery_token"]
			_, hasTTL := body["token_ttl_seconds"]
			ambiguityHint := strings.Contains(fmt.Sprint(body["hint"]), "user_selected_after_ambiguous_project")
			wantRecovery := tc.code == "ambiguous_project"
			if hasToken != wantRecovery || hasTTL != wantRecovery || ambiguityHint != wantRecovery {
				t.Fatalf("inapplicable ambiguity recovery: %v", body)
			}
		})
	}
}

func TestHandleSaveSessionReadFailureIsNotAmbiguity(t *testing.T) {
	s := newMCPTestStore(t)
	const session = "synthetic-valid-session"
	if err := s.StartSession(session, "synthetic-project", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(session); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, readErr := s.GetSession(session)
	if readErr == nil {
		t.Fatal("expected closed-store read failure")
	}
	result, err := handleSave(s, MCPConfig{}, NewSessionActivity(time.Minute))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"session_id": session, "project": "synthetic-project", "title": "Synthetic probe", "content": "Synthetic only", "capture_prompt": false,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	body := callResultJSON(t, result)
	if !result.IsError || body["error_code"] != "project_resolution_failed" || !strings.Contains(body["message"].(string), readErr.Error()) {
		t.Fatalf("session read failure lost or misclassified: %v", body)
	}
	if body["recovery_token"] != nil || body["token_ttl_seconds"] != nil || strings.Contains(fmt.Sprint(body["hint"]), "user_selected_after_ambiguous_project") {
		t.Fatal("session read failure offered ambiguity recovery")
	}
}
