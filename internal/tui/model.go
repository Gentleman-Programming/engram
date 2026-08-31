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
	"errors"

	"github.com/Gentleman-Programming/engram/internal/cloudconfig"
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

const (
	cloudConfigFocusInput = iota
	cloudConfigFocusTest
	cloudConfigFocusSave
	cloudConfigFocusCancel
)

type SessionDeleteState int

const (
	SessionDeleteStateNone SessionDeleteState = iota
	SessionDeleteStatePrompt
	SessionDeleteStateDeleting
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

type sessionDeletedMsg struct {
	sessionID string
	err       error
}

type setupInstallMsg struct {
	result *setup.Result
	err    error
}

type cloudConfigLoadedMsg struct {
	generation  uint64
	serverURL   string
	tokenSource string
	err         error
}

type cloudPingMsg struct {
	origin     cloudPingOrigin
	generation uint64
	serverURL  string
	status     string
	err        error
}

type cloudStatusLoadedMsg struct {
	generation   uint64
	serverURL    string
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

type cloudEnrollmentToggledMsg struct {
	err error
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
	Sessions             []store.SessionSummary
	SelectedSessionIdx   int
	SessionObservations  []store.Observation
	SessionDetailScroll  int
	SessionDeleteState   SessionDeleteState
	SessionDeleteID      string
	SessionDeleteProject string

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

	// Cloud configuration
	CloudConfigInput       textinput.Model
	CloudConfigTokenSource string
	CloudConfigError       string
	CloudConfigFocus       int
	CloudConfigPingStatus  string
	CloudConfigSaving      bool
	CloudConfigTest        bool
	CloudRequestGeneration uint64

	// Cloud status
	CloudStatusServerURL    string
	CloudStatusHealth       string
	CloudStatusLastSync     string
	CloudStatusPendingCount int64
	CloudStatusLastError    string
	CloudStatusHealthError  string
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
		store:            s,
		Version:          version,
		Screen:           ScreenDashboard,
		SearchInput:      ti,
		CloudConfigInput: ci,
		SetupSpinner:     sp,
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

func deleteSession(s *store.Store, sessionID string) tea.Cmd {
	return func() tea.Msg {
		if s == nil {
			return sessionDeletedMsg{sessionID: sessionID, err: errors.New("store is unavailable")}
		}
		err := s.DeleteSession(sessionID)
		return sessionDeletedMsg{sessionID: sessionID, err: err}
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

func loadCloudConfigCmd(dataDir string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		cfg, err := loadCloudConfigForUI(dataDir)
		if err != nil {
			return cloudConfigLoadedMsg{generation: generation, err: err}
		}
		_, source := cloudconfig.EffectiveToken(dataDir)
		return cloudConfigLoadedMsg{generation: generation, serverURL: cfg.ServerURL, tokenSource: cloudconfig.SourceLabel(source)}
	}
}

func loadCloudStatusCmd(s *store.Store, generation uint64) tea.Cmd {
	return func() tea.Msg {
		if s == nil {
			return cloudStatusLoadedMsg{generation: generation, err: errors.New("store is unavailable")}
		}
		dataDir := s.DataDir()
		cfg, err := loadCloudConfigForUI(dataDir)
		if err != nil {
			return cloudStatusLoadedMsg{generation: generation, err: err}
		}
		summary, err := s.CloudSyncSummary()
		if err != nil {
			return cloudStatusLoadedMsg{generation: generation, err: err}
		}
		return cloudStatusLoadedMsg{
			generation:   generation,
			serverURL:    cfg.ServerURL,
			lastSync:     summary.LastSuccessAt,
			pendingCount: summary.PendingMutations,
			lastError:    summary.LastError,
		}
	}
}

func loadCloudEnrollmentCmd(s *store.Store) tea.Cmd {
	return func() tea.Msg {
		if s == nil {
			return cloudEnrollmentLoadedMsg{err: errors.New("store is unavailable")}
		}
		names, err := s.ListProjectsForCloudEnrollment()
		if err != nil {
			return cloudEnrollmentLoadedMsg{err: err}
		}
		enrolledProjects, err := s.ListEnrolledProjects()
		if err != nil {
			return cloudEnrollmentLoadedMsg{err: err}
		}
		enrolled := make(map[string]struct{}, len(enrolledProjects))
		for _, project := range enrolledProjects {
			name, _ := store.NormalizeProject(project.Project)
			if name != "" {
				enrolled[name] = struct{}{}
			}
		}
		items := make([]cloudEnrollmentItem, 0, len(names))
		for _, name := range names {
			_, isEnrolled := enrolled[name]
			items = append(items, cloudEnrollmentItem{project: name, enrolled: isEnrolled})
		}
		return cloudEnrollmentLoadedMsg{items: items}
	}
}

func toggleCloudEnrollmentCmd(s *store.Store, item cloudEnrollmentItem) tea.Cmd {
	return func() tea.Msg {
		if s == nil {
			return cloudEnrollmentToggledMsg{err: errors.New("store is unavailable")}
		}
		if item.enrolled {
			return cloudEnrollmentToggledMsg{err: s.UnenrollProject(item.project)}
		}
		return cloudEnrollmentToggledMsg{err: s.EnrollProject(item.project)}
	}
}
