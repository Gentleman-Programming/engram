package obsidian

import (
	"encoding/json"
	"errors"
	"os"
)

// SyncState tracks the state of a previous export run.
// It is persisted as JSON in {vault}/engram/.engram-sync-state.json.
type SyncState struct {
	PendingRetirements map[int64][]string `json:"pending_retirements,omitempty"` // obs ID → superseded paths awaiting removal
	LastExportAt       string             `json:"last_export_at"`
	Files              map[int64]string   `json:"files"`        // obs ID → relative vault path
	SessionHubs        map[string]string  `json:"session_hubs"` // session ID → relative path
	TopicHubs          map[string]string  `json:"topic_hubs"`   // topic prefix → relative path
	Version            int                `json:"version"`      // schema version (1)
}

// ExportResult summarizes what happened during an export run.
type ExportResult struct {
	Created     int
	Updated     int
	Deleted     int
	Skipped     int
	HubsCreated int
	Errors      []error
}

// ReadState reads the sync state from the given JSON file path.
// If the file does not exist, it returns an empty SyncState and no error.
func ReadState(path string) (SyncState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SyncState{
				PendingRetirements: make(map[int64][]string),
				Files:              make(map[int64]string),
				SessionHubs:        make(map[string]string),
				TopicHubs:          make(map[string]string),
			}, nil
		}
		return SyncState{}, err
	}

	var state SyncState
	if err := json.Unmarshal(data, &state); err != nil {
		return SyncState{}, err
	}

	// Ensure maps are non-nil after unmarshal, including older version-1 states.
	if state.PendingRetirements == nil {
		state.PendingRetirements = make(map[int64][]string)
	}
	// Ensure maps are non-nil after unmarshal
	if state.Files == nil {
		state.Files = make(map[int64]string)
	}
	if state.SessionHubs == nil {
		state.SessionHubs = make(map[string]string)
	}
	if state.TopicHubs == nil {
		state.TopicHubs = make(map[string]string)
	}

	return state, nil
}

// WriteState persists the sync state as JSON to the given file path.
// The file is written atomically (overwrite) with 0644 permissions.
func WriteState(path string, s SyncState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
