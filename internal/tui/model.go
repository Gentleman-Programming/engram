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
	ScreenSetup
	ScreenCloudSettings
	ScreenCloudConfig
	ScreenCloudStatus
	ScreenCloudEnrollment
)

// Cloud Config form focus positions.
const (
	cloudConfigFocusInput = iota
	cloudConfigFocusTest
	cloudConfigFocusSave
	cloudConfigFocusCancel
)

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

type setupInstallMsg struct {
	result *setup.Result
	err    error
}

type cloudConfigLoadedMsg struct {
	serverURL   string
	tokenSource string
	err         error
}

type cloudPingMsg struct {
	status string
	err    error
}

type cloudStatusLoadedMsg struct {
	serverURL    string
	tokenSource  string
	lastSync     string
	pendingCount int64
	lastError    string
	err          error
}

type cloudEnrollmentItem struct {
	project  string
	enrolled bool
}

type cloudEnrollmentLoadedMsg struct {
	items []cloudEnrollmentItem
	err   error
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

	// Cloud config
	CloudConfigInput      textinput.Model
	CloudConfigServerURL  string
	CloudConfigTokenSource string
	CloudConfigError      string
	CloudConfigFocus      int
	CloudConfigPingStatus string
	CloudConfigSaving     bool
	CloudConfigTest       bool // true when the current ping is a test, not a save

	// Cloud status
	CloudStatusServerURL    string
	CloudStatusTokenSource  string
	CloudStatusHealth       string
	CloudStatusLastSync     string
	CloudStatusPendingCount int64
	CloudStatusLastError    string
	CloudStatusLoading      bool

	// Cloud enrollment
	CloudEnrollmentItems   []cloudEnrollmentItem
	CloudEnrollmentError   string
	CloudEnrollmentLoading bool
}

// New creates a new TUI model connected to the given store.
func New(s *store.Store, version string) Model {
	ti := textinput.New()
	ti.Placeholder = "Search memories..."
	ti.CharLimit = 256
	ti.Width = 60

	ci := textinput.New()
	ci.Placeholder = "https://cloud.example.com"
	ci.CharLimit = 256
	ci.Width = 60

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorLavender)

	return Model{
		store:          s,
		Version:        version,
		Screen:         ScreenDashboard,
		SearchInput:    ti,
		CloudConfigInput: ci,
		SetupSpinner:   sp,
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
		results, err := s.Search(query, store.SearchOptions{Limit: 50})
		return searchResultsMsg{results: results, query: query, err: err}
	}
}

func loadRecentObservations(s *store.Store) tea.Cmd {
	return func() tea.Msg {
		obs, err := s.AllObservations("", "", 50)
		return recentObservationsMsg{observations: obs, err: err}
	}
}

func loadObservationDetail(s *store.Store, id int64) tea.Cmd {
	return func() tea.Msg {
		obs, err := s.GetObservation(id)
		return observationDetailMsg{observation: obs, err: err}
	}
}

func loadTimeline(s *store.Store, obsID int64) tea.Cmd {
	return func() tea.Msg {
		tl, err := s.Timeline(obsID, 10, 10)
		return timelineMsg{timeline: tl, err: err}
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
		obs, err := s.SessionObservations(sessionID, 200)
		return sessionObservationsMsg{observations: obs, err: err}
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

func loadCloudConfigCmd(dataDir string) tea.Cmd {
	return func() tea.Msg {
		cc, err := loadCloudConfig(dataDir)
		if err != nil {
			return cloudConfigLoadedMsg{err: err}
		}
		return cloudConfigLoadedMsg{
			serverURL:   cc.ServerURL,
			tokenSource: tokenSourceMessage(dataDir),
			err:         nil,
		}
	}
}

func loadCloudStatusCmd(s *store.Store) tea.Cmd {
	return func() tea.Msg {
		cc, err := loadCloudConfig(s.DataDir())
		if err != nil {
			return cloudStatusLoadedMsg{err: err}
		}
		state, err := s.GetSyncState(store.DefaultSyncTargetKey)
		if err != nil {
			return cloudStatusLoadedMsg{err: err}
		}
		count, err := s.CountPendingSyncMutations(store.DefaultSyncTargetKey)
		if err != nil {
			return cloudStatusLoadedMsg{err: err}
		}
		lastErr := ""
		if state.LastError != nil {
			lastErr = *state.LastError
		}
		return cloudStatusLoadedMsg{
			serverURL:    cc.ServerURL,
			tokenSource:  tokenSourceMessage(s.DataDir()),
			lastSync:     state.UpdatedAt,
			pendingCount: count,
			lastError:    lastErr,
		}
	}
}

func loadCloudEnrollmentCmd(s *store.Store) tea.Cmd {
	return func() tea.Msg {
		names, err := s.ListProjectNames()
		if err != nil {
			return cloudEnrollmentLoadedMsg{err: err}
		}
		items := make([]cloudEnrollmentItem, 0, len(names))
		for _, name := range names {
			enrolled, err := s.IsProjectEnrolled(name)
			if err != nil {
				return cloudEnrollmentLoadedMsg{err: err}
			}
			items = append(items, cloudEnrollmentItem{project: name, enrolled: enrolled})
		}
		return cloudEnrollmentLoadedMsg{items: items}
	}
}
