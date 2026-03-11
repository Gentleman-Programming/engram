package main

import (
	"testing"
)

func boolPtr(v bool) *bool { return &v }

func TestIsTeamSyncEnabled(t *testing.T) {
	tests := []struct {
		name     string
		config   *CloudConfig
		envVar   string
		expected bool
	}{
		{
			name:     "nil config defaults to true",
			config:   nil,
			expected: true,
		},
		{
			name:     "nil TeamSync defaults to true",
			config:   &CloudConfig{},
			expected: true,
		},
		{
			name:     "TeamSync explicitly true",
			config:   &CloudConfig{TeamSync: boolPtr(true)},
			expected: true,
		},
		{
			name:     "TeamSync explicitly false",
			config:   &CloudConfig{TeamSync: boolPtr(false)},
			expected: false,
		},
		{
			name:     "env var true overrides config false",
			config:   &CloudConfig{TeamSync: boolPtr(false)},
			envVar:   "true",
			expected: true,
		},
		{
			name:     "env var 1 overrides config false",
			config:   &CloudConfig{TeamSync: boolPtr(false)},
			envVar:   "1",
			expected: true,
		},
		{
			name:     "env var false overrides config true",
			config:   &CloudConfig{TeamSync: boolPtr(true)},
			envVar:   "false",
			expected: false,
		},
		{
			name:     "env var 0 overrides config true",
			config:   &CloudConfig{TeamSync: boolPtr(true)},
			envVar:   "0",
			expected: false,
		},
		{
			name:     "env var false overrides nil config",
			config:   nil,
			envVar:   "false",
			expected: false,
		},
		{
			name:     "env var arbitrary string treated as true",
			config:   &CloudConfig{TeamSync: boolPtr(false)},
			envVar:   "yes",
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envVar != "" {
				t.Setenv("ENGRAM_TEAM_SYNC", tc.envVar)
			} else {
				t.Setenv("ENGRAM_TEAM_SYNC", "")
			}

			got := tc.config.IsTeamSyncEnabled()
			if got != tc.expected {
				t.Fatalf("IsTeamSyncEnabled() = %v, want %v", got, tc.expected)
			}
		})
	}
}
