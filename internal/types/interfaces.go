package types

// StoreReader defines read operations available on both local SQLite and cloud backends.
type StoreReader interface {
	GetObservation(id int64) (*Observation, error)
	Search(query string, opts SearchOptions) ([]SearchResult, error)
	RecentSessions(project string, limit int) ([]SessionSummary, error)
	AllSessions(project string, limit int) ([]SessionSummary, error)
	SessionObservations(sessionID string, limit int) ([]Observation, error)
	RecentObservations(project, scope string, limit int) ([]Observation, error)
	AllObservations(project, scope string, limit int) ([]Observation, error)
	FormatContext(project, scope string) (string, error)
	Timeline(observationID int64, before, after int) (*TimelineResult, error)
	Stats() (*Stats, error)
	RecentPrompts(project string, limit int) ([]Prompt, error)
	SearchPrompts(query string, project string, limit int) ([]Prompt, error)
}

// StoreWriter defines write operations available on both local SQLite and cloud backends.
type StoreWriter interface {
	CreateSession(id, project, directory string) error
	EndSession(id string, summary string) error
	DeleteSession(id string) error
	AddObservation(p AddObservationParams) (int64, error)
	UpdateObservation(id int64, p UpdateObservationParams) (*Observation, error)
	DeleteObservation(id int64, hardDelete bool) error
	AddPrompt(p AddPromptParams) (int64, error)
	DeletePrompt(id int64) error
	PassiveCapture(p PassiveCaptureParams) (*PassiveCaptureResult, error)
	MigrateProject(oldName, newName string) (*MigrateResult, error)
}

// StoreInterface is the full interface used by MCP, HTTP, and TUI handlers.
// Both *store.Store (SQLite) and RemoteStore (cloud HTTP proxy) implement this.
type StoreInterface interface {
	StoreReader
	StoreWriter
}

// StoreSyncer defines sync-specific operations only available on local SQLite store.
// RemoteStore (cloud-only mode) does NOT implement this.
type StoreSyncer interface {
	Export() (*ExportData, error)
	Import(data *ExportData) (*ImportResult, error)
}
