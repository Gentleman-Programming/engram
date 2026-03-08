package cloudserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// TestWriteStoreError verifies that writeStoreError maps errors to the correct
// HTTP status codes. These tests do not require a database or Docker.
func TestWriteStoreError(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantStatus   int
		wantBodyFrag string
	}{
		{
			name:         "ErrNotFound returns 404",
			err:          cloudstore.ErrNotFound,
			wantStatus:   http.StatusNotFound,
			wantBodyFrag: "not found",
		},
		{
			name:         "wrapped ErrNotFound returns 404",
			err:          fmt.Errorf("cloudstore: get observation: %w", cloudstore.ErrNotFound),
			wantStatus:   http.StatusNotFound,
			wantBodyFrag: "not found",
		},
		{
			name:         "DB connection refused returns 503",
			err:          fmt.Errorf("dial tcp: connection refused"),
			wantStatus:   http.StatusServiceUnavailable,
			wantBodyFrag: "database unavailable",
		},
		{
			name:         "generic error returns 500",
			err:          fmt.Errorf("unexpected failure"),
			wantStatus:   http.StatusInternalServerError,
			wantBodyFrag: "unexpected failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeStoreError(w, tt.err, tt.err.Error())

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			body := w.Body.String()
			if tt.wantBodyFrag != "" && !strings.Contains(body, tt.wantBodyFrag) {
				t.Errorf("body %q does not contain %q", body, tt.wantBodyFrag)
			}
		})
	}
}

// TestIsDBConnectionError verifies that known Postgres connection error strings
// are recognized as database connection failures.
func TestIsDBConnectionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"connection refused", fmt.Errorf("dial tcp: connection refused"), true},
		{"broken pipe", fmt.Errorf("write: broken pipe"), true},
		{"driver bad connection", fmt.Errorf("driver: bad connection"), true},
		{"database is closed", fmt.Errorf("sql: database is closed"), true},
		{"unrelated error", fmt.Errorf("invalid input"), false},
		{"ErrNotFound is not a connection error", cloudstore.ErrNotFound, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDBConnectionError(tt.err)
			if got != tt.want {
				t.Errorf("isDBConnectionError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

