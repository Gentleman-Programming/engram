// Package tui implements the Bubbletea terminal UI for Engram.
//
// Following the Gentleman Bubbletea patterns:
// - Screen constants as iota
// - Single Model struct holds ALL state
// - Update() with type switch
// - Per-screen key handlers returning (tea.Model, tea.Cmd)
// - Vim keys (j/k) for navigation
// - PrevScreen for back navigation
package tui

import (
	"github.com/Gentleman-Programming/engram/internal/setup"
	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/Gentleman-Programming/engram/internal/version"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Screens ─────────────────────────────────────────────────────────────────

type Screen int

const (
	ScreenDashboard Screen = iota
	ScreenSearch
	ScreenSearchResults
	ScreenRecent
	ScreenObservationDetail
	ScreenTimeline
	ScreenSessions
	ScreenSessionDetail
	ScreenTrash
	ScreenConfirmDelete
	ScreenSetup
)

type confirmAction int

const (
	confirmNone confirmAction = iota
	confirmPurgeObservation
	confirmDeleteSession
)

type confirmState struct {
	Action           confirmAction
	Title            string
	Body             string
	ObservationID    int64
	SessionID        string
	ReturnScreen     Screen
	ObservationCount int
}

// ─── Custom Messages ─────────────────────────────────────────────────────────

type updateCheckMsg struct {
	result version.CheckResult
}

type statsLoadedMsg struct {
	stats *store.Stats
	err   error
}

type searchResultsMsg struct {
	results []store.SearchResult
	query   string
	err     error
}

type recentObservationsMsg struct {
	observations []store.Observation
	err          error
}

type observationDetailMsg struct {
	observation *store.Observation
	err         error
}

type timelineMsg struct {
	timeline *store.TimelineResult
	err      error
}

type recentSessionsMsg struct {
	sessions []store.SessionSummary
	err      error
}

type sessionObservationsMsg struct {
	observations []store.Observation
	err          error
}

type trashObservationsMsg struct {
	observations []store.Observation
	err          error
}

type observationDeletedMsg struct {
	id  int64
	err error
}

type observationPurgedMsg struct {
	id  int64
	err error
}

type observationRestoredMsg struct {
	id  int64
	err error
}

type sessionDeletedMsg struct {
	sessionID string
	result    *store.DeleteSessionCascadeResult
	err       error
}

type setupInstallMsg struct {
	result *setup.Result
	err    error
}

// ─── Model ───────────────────────────────────────────────────────────────────

type Model struct {
	store      *store.Store
	Version    string
	Screen     Screen
	PrevScreen Screen
	Width      int
	Height     int
	Cursor     int
	Scroll     int

	// Update notification
	UpdateStatus version.CheckStatus
	UpdateMsg    string

	// Error display
	ErrorMsg string

	// Dashboard
	Stats *store.Stats

	// Search
	SearchInput   textinput.Model
	SearchQuery   string
	SearchResults []store.SearchResult

	// Recent observations
	RecentObservations []store.Observation

	// Recycle bin
	TrashObservations []store.Observation

	// Destructive action confirmation
	Confirm confirmState

	// Observation detail
	SelectedObservation *store.Observation
	DetailScroll        int

	// Timeline
	Timeline *store.TimelineResult

	// Sessions
	Sessions            []store.SessionSummary
	SelectedSessionIdx  int
	SessionObservations []store.Observation
	SessionDetailScroll int

	// Clipboard feedback
	CopyFeedback string // "✓ Copied!" or "" — shown for 2 s after copy

	// Setup
	SetupAgents           []setup.Agent
	SetupResult           *setup.Result
	SetupError            string
	SetupDone             bool
	SetupInstalling       bool
	SetupInstallingName   string // agent name being installed (for display)
	SetupAllowlistPrompt  bool   // true = showing y/n prompt for allowlist
	SetupAllowlistApplied bool   // true = allowlist was added successfully
	SetupAllowlistError   string // error message if allowlist injection failed
	SetupSpinner          spinner.Model
}

// New creates a new TUI model connected to the given store.
func New(s *store.Store, version string) Model {
	ti := textinput.New()
	ti.Placeholder = "Search memories..."
	ti.CharLimit = 256
	ti.Width = 60

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorLavender)

	return Model{
		store:        s,
		Version:      version,
		Screen:       ScreenDashboard,
		SearchInput:  ti,
		SetupSpinner: sp,
	}
}

// Init loads initial data (stats for the dashboard).
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		loadStats(m.store),
		checkForUpdate(m.Version),
		tea.EnterAltScreen,
	)
}

// ─── Commands (data loading) ─────────────────────────────────────────────────

func checkForUpdate(v string) tea.Cmd {
	return func() tea.Msg {
		return updateCheckMsg{result: version.CheckLatest(v)}
	}
}

func loadStats(s *store.Store) tea.Cmd {
	return func() tea.Msg {
		stats, err := s.Stats()
		return statsLoadedMsg{stats: stats, err: err}
	}
}

func searchMemories(s *store.Store, query string) tea.Cmd {
	return func() tea.Msg {
		results, err := s.Search(query, store.SearchOptions{Limit: 50, IncludeDeleted: true})
		return searchResultsMsg{results: results, query: query, err: err}
	}
}

func loadRecentObservations(s *store.Store) tea.Cmd {
	return func() tea.Msg {
		obs, err := s.AllObservationsWithOptions(store.ObservationListOptions{Limit: 50, IncludeDeleted: true})
		return recentObservationsMsg{observations: obs, err: err}
	}
}

func loadObservationDetail(s *store.Store, id int64) tea.Cmd {
	return func() tea.Msg {
		obs, err := s.GetObservationIncludingDeleted(id)
		return observationDetailMsg{observation: obs, err: err}
	}
}

func loadTimeline(s *store.Store, obsID int64) tea.Cmd {
	return func() tea.Msg {
		tl, err := s.Timeline(obsID, 10, 10)
		if err != nil {
			if deletedTimeline, deletedErr := loadDeletedFocusTimeline(s, obsID, 10, 10); deletedErr == nil {
				tl = deletedTimeline
				err = nil
			}
		}
		return timelineMsg{timeline: tl, err: err}
	}
}

const deletedFocusTimelineSessionLimit = 10000

func loadDeletedFocusTimeline(s *store.Store, obsID int64, before, after int) (*store.TimelineResult, error) {
	focus, err := s.GetObservationIncludingDeleted(obsID)
	if err != nil {
		return nil, err
	}
	if !store.IsObservationDeleted(*focus) {
		return nil, store.ErrObservationNotFound
	}

	session, _ := s.GetSession(focus.SessionID)
	observations, err := s.SessionObservationsWithOptions(focus.SessionID, store.ObservationListOptions{
		Limit:          deletedFocusTimelineSessionLimit,
		IncludeDeleted: true,
	})
	if err != nil {
		return nil, err
	}

	focusIdx := -1
	for i, obs := range observations {
		if obs.ID == obsID {
			focusIdx = i
			break
		}
	}

	beforeEntries := make([]store.TimelineEntry, 0, before)
	if focusIdx > 0 {
		for i := focusIdx - 1; i >= 0 && len(beforeEntries) < before; i-- {
			obs := observations[i]
			if store.IsObservationDeleted(obs) {
				continue
			}
			beforeEntries = append(beforeEntries, timelineEntryFromObservation(obs))
		}
		for i, j := 0, len(beforeEntries)-1; i < j; i, j = i+1, j-1 {
			beforeEntries[i], beforeEntries[j] = beforeEntries[j], beforeEntries[i]
		}
	}

	afterEntries := make([]store.TimelineEntry, 0, after)
	if focusIdx >= 0 {
		for i := focusIdx + 1; i < len(observations) && len(afterEntries) < after; i++ {
			obs := observations[i]
			if store.IsObservationDeleted(obs) {
				continue
			}
			afterEntries = append(afterEntries, timelineEntryFromObservation(obs))
		}
	}

	totalActive := 0
	for _, obs := range observations {
		if !store.IsObservationDeleted(obs) {
			totalActive++
		}
	}

	return &store.TimelineResult{
		Focus:        *focus,
		Before:       beforeEntries,
		After:        afterEntries,
		SessionInfo:  session,
		TotalInRange: totalActive,
	}, nil
}

func timelineEntryFromObservation(obs store.Observation) store.TimelineEntry {
	return store.TimelineEntry{
		ID:             obs.ID,
		SessionID:      obs.SessionID,
		Type:           obs.Type,
		Title:          obs.Title,
		Content:        obs.Content,
		ToolName:       obs.ToolName,
		Project:        obs.Project,
		Scope:          obs.Scope,
		TopicKey:       obs.TopicKey,
		RevisionCount:  obs.RevisionCount,
		DuplicateCount: obs.DuplicateCount,
		LastSeenAt:     obs.LastSeenAt,
		CreatedAt:      obs.CreatedAt,
		UpdatedAt:      obs.UpdatedAt,
		DeletedAt:      obs.DeletedAt,
	}
}

func loadRecentSessions(s *store.Store) tea.Cmd {
	return func() tea.Msg {
		sessions, err := s.AllSessions("", 50)
		return recentSessionsMsg{sessions: sessions, err: err}
	}
}

func loadSessionObservations(s *store.Store, sessionID string) tea.Cmd {
	return func() tea.Msg {
		obs, err := s.SessionObservationsWithOptions(sessionID, store.ObservationListOptions{Limit: 200, IncludeDeleted: true})
		return sessionObservationsMsg{observations: obs, err: err}
	}
}

func loadTrashObservations(s *store.Store) tea.Cmd {
	return func() tea.Msg {
		obs, err := s.DeletedObservations(200)
		return trashObservationsMsg{observations: obs, err: err}
	}
}

func deleteObservationCmd(s *store.Store, id int64) tea.Cmd {
	return func() tea.Msg {
		return observationDeletedMsg{id: id, err: s.DeleteObservation(id, false)}
	}
}

func purgeObservationCmd(s *store.Store, id int64) tea.Cmd {
	return func() tea.Msg {
		return observationPurgedMsg{id: id, err: s.PurgeObservation(id)}
	}
}

func restoreObservationCmd(s *store.Store, id int64) tea.Cmd {
	return func() tea.Msg {
		return observationRestoredMsg{id: id, err: s.RestoreObservation(id)}
	}
}

func deleteSessionCascadeCmd(s *store.Store, sessionID string) tea.Cmd {
	return func() tea.Msg {
		result, err := s.DeleteSessionCascade(sessionID)
		return sessionDeletedMsg{sessionID: sessionID, result: result, err: err}
	}
}

func installAgent(agentName string) tea.Cmd {
	return func() tea.Msg {
		result, err := installAgentFn(agentName)
		return setupInstallMsg{result: result, err: err}
	}
}

var installAgentFn = setup.Install
var addClaudeCodeAllowlistFn = setup.AddClaudeCodeAllowlist
