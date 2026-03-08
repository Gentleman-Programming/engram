package cloudserver

import (
	"strings"
	"testing"
)

func TestValidateRegisterRequest(t *testing.T) {
	tests := []struct {
		name     string
		username string
		email    string
		password string
		wantErr  string
	}{
		// Valid
		{
			name:     "valid inputs",
			username: "jane_doe",
			email:    "jane@example.com",
			password: "supersecret",
		},
		{
			name:     "username with hyphen",
			username: "jane-doe",
			email:    "jane@example.com",
			password: "supersecret",
		},
		{
			name:     "username with dot",
			username: "alan.b",
			email:    "alan@example.com",
			password: "supersecret",
		},
		{
			name:     "username with unicode letters",
			username: "señor_dev",
			email:    "senor@example.com",
			password: "supersecret",
		},
		{
			name:     "email with localhost domain",
			username: "janedoe",
			email:    "jane@localhost",
			password: "supersecret",
		},
		{
			name:     "password exactly 8 chars is valid",
			username: "janedoe",
			email:    "jane@example.com",
			password: "12345678",
		},
		{
			name:     "password exactly 72 chars is valid",
			username: "janedoe",
			email:    "jane@example.com",
			password: strings.Repeat("a", 72),
		},
		// Username
		{
			name:     "username too short",
			username: "ab",
			email:    "jane@example.com",
			password: "supersecret",
			wantErr:  "username must be at least",
		},
		{
			name:     "username too long",
			username: strings.Repeat("a", 51),
			email:    "jane@example.com",
			password: "supersecret",
			wantErr:  "username must be at most",
		},
		{
			name:     "username with space",
			username: "jane doe",
			email:    "jane@example.com",
			password: "supersecret",
			wantErr:  "username may only contain",
		},
		{
			name:     "username with @ sign",
			username: "jane@doe",
			email:    "jane@example.com",
			password: "supersecret",
			wantErr:  "username may only contain",
		},
		// Email
		{
			name:     "email missing @",
			username: "janedoe",
			email:    "janeatexample.com",
			password: "supersecret",
			wantErr:  "email must contain an @",
		},
		{
			name:     "email @ at start",
			username: "janedoe",
			email:    "@example.com",
			password: "supersecret",
			wantErr:  "email must contain an @",
		},
		{
			name:     "email domain starts with dot",
			username: "janedoe",
			email:    "jane@.example.com",
			password: "supersecret",
			wantErr:  "email domain is invalid",
		},
		{
			name:     "email domain ends with dot",
			username: "janedoe",
			email:    "jane@example.",
			password: "supersecret",
			wantErr:  "email domain is invalid",
		},
		// Password
		{
			name:     "password too short",
			username: "janedoe",
			email:    "jane@example.com",
			password: "1234567",
			wantErr:  "password must be at least 8",
		},
		{
			name:     "password over 72 chars",
			username: "janedoe",
			email:    "jane@example.com",
			password: strings.Repeat("a", 73),
			wantErr:  "password must be at most 72",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRegisterRequest(tt.username, tt.email, tt.password)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateSearchParams(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		limit   int
		wantErr string
	}{
		// Valid
		{"valid query and limit", "golang context", 10, ""},
		{"max query length", strings.Repeat("a", 500), 10, ""},
		{"max limit", "something", 100, ""},
		{"min limit", "something", 1, ""},
		// Query
		{"query too long", strings.Repeat("a", 501), 10, "q must be at most"},
		// Limit
		{"limit zero", "something", 0, "limit must be between"},
		{"limit negative", "something", -1, "limit must be between"},
		{"limit over max", "something", 101, "limit must be between"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSearchParams(tt.query, tt.limit)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
