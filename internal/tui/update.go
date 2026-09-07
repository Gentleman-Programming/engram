package tui

import (
	"errors"
	"fmt"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloudconfig"
	"github.com/Gentleman-Programming/engram/v2/internal/setup"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// ─── Update ──────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		m = m.clampViewport()
		return m, nil

	case tea.KeyMsg:
		// Global quit — always works
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		// If search input is focused, let it handle most keys
		if m.Screen == ScreenSearch && m.SearchInput.Focused() {
			return m.handleSearchInputKeys(msg)
		}
		if m.Screen == ScreenCloudConfig && m.CloudConfigFocus == cloudConfigFocusInput && m.CloudConfigInput.Focused() {
			return m.handleCloudConfigInputKeys(msg)
		}
		return m.handleKeyPress(msg.String())

	// ─── Data loaded messages ────────────────────────────────────────────
	case updateCheckMsg:
		m.UpdateStatus = msg.result.Status
		m.UpdateMsg = msg.result.Message
		return m, nil

	case statsLoadedMsg:
		if msg.err != nil {
			m.ErrorMsg = msg.err.Error()
			return m, nil
		}
		m.Stats = msg.stats
		return m, nil

	case searchResultsMsg:
		if msg.err != nil {
			m.ErrorMsg = msg.err.Error()
			return m, nil
		}
		m.SearchResults = msg.results
		m.SearchQuery = msg.query
		m.Screen = ScreenSearchResults
		m.Cursor = 0
		m.Scroll = 0
		return m, nil

	case recentObservationsMsg:
		if msg.err != nil {
			m.ErrorMsg = msg.err.Error()
			return m, nil
		}
		m.RecentObservations = msg.observations
		return m, nil

	case observationDetailMsg:
		if msg.err != nil {
			m.ErrorMsg = msg.err.Error()
			return m, nil
		}
		m.SelectedObservation = msg.observation
		m.Screen = ScreenObservationDetail
		m.DetailScroll = 0
		return m, nil

	case timelineMsg:
		if msg.err != nil {
			m.ErrorMsg = msg.err.Error()
			return m, nil
		}
		m.Timeline = msg.timeline
		m.Screen = ScreenTimeline
		m.Scroll = 0
		return m, nil

	case recentSessionsMsg:
		if msg.err != nil {
			m.ErrorMsg = msg.err.Error()
			return m, nil
		}
		m.Sessions = msg.sessions
		if m.Screen == ScreenSessions {
			if len(m.Sessions) == 0 {
				m.Cursor = 0
				m.Scroll = 0
			} else if m.Cursor >= len(m.Sessions) {
				m.Cursor = len(m.Sessions) - 1
				if m.Scroll > m.Cursor {
					m.Scroll = m.Cursor
				}
			}
		}
		return m, nil

	case sessionObservationsMsg:
		if msg.err != nil {
			m.ErrorMsg = msg.err.Error()
			return m, nil
		}
		m.SessionObservations = msg.observations
		m.Screen = ScreenSessionDetail
		m.Cursor = 0
		m.SessionDetailScroll = 0
		return m, nil

	case sessionDeletedMsg:
		m = m.resetSessionDeleteState()
		if msg.err != nil {
			m.ErrorMsg = sessionDeleteErrorMessage(msg.sessionID, msg.err)
			return m, nil
		}
		m.ErrorMsg = ""
		return m, loadRecentSessions(m.store)

	case setupInstallMsg:
		m.SetupInstalling = false
		if msg.err != nil {
			m.SetupDone = true
			m.SetupError = msg.err.Error()
			return m, nil
		}
		m.SetupResult = msg.result
		m.SetupError = ""
		// For claude-code, show allowlist prompt before marking done
		if msg.result != nil && msg.result.Agent == "claude-code" {
			m.SetupAllowlistPrompt = true
			return m, nil
		}
		m.SetupDone = true
		return m, nil

	case cloudConfigLoadedMsg:
		if msg.generation != m.CloudRequestGeneration || m.Screen != ScreenCloudConfig {
			return m, nil
		}
		if msg.err != nil {
			m.CloudConfigError = msg.err.Error()
			return m, nil
		}
		m.CloudConfigTokenSource = msg.tokenSource
		m.CloudConfigInput.SetValue(msg.serverURL)
		m.CloudConfigInput.Focus()
		return m, nil

	case cloudStatusLoadedMsg:
		if msg.generation != m.CloudRequestGeneration || m.Screen != ScreenCloudStatus {
			return m, nil
		}
		m.CloudStatusLoading = false
		if msg.err != nil {
			m.CloudStatusLastError = msg.err.Error()
			m.CloudStatusHealthError = ""
			return m, nil
		}
		m.CloudStatusServerURL = msg.serverURL
		m.CloudStatusLastSync = msg.lastSync
		m.CloudStatusPendingCount = msg.pendingCount
		m.CloudStatusLastError = msg.lastError
		m.CloudStatusHealthError = ""
		if msg.serverURL != "" && m.store != nil {
			token, _ := cloudconfig.EffectiveToken(m.store.DataDir())
			return m, pingCloudServer(cloudPingFromStatus, msg.generation, msg.serverURL, token)
		}
		return m, nil

	case cloudEnrollmentLoadedMsg:
		m.CloudEnrollmentLoading = false
		if msg.err != nil {
			m.CloudEnrollmentError = msg.err.Error()
			return m, nil
		}
		m.CloudEnrollmentItems = msg.items
		m.CloudEnrollmentError = ""
		if m.Screen == ScreenCloudEnrollment {
			if len(m.CloudEnrollmentItems) == 0 {
				m.Cursor = 0
			} else if m.Cursor >= len(m.CloudEnrollmentItems) {
				m.Cursor = len(m.CloudEnrollmentItems) - 1
			}
		}
		return m, nil

	case cloudEnrollmentToggledMsg:
		m.CloudEnrollmentLoading = false
		if msg.err != nil {
			m.CloudEnrollmentError = msg.err.Error()
			return m, nil
		}
		m.CloudEnrollmentLoading = true
		return m, loadCloudEnrollmentCmd(m.store)

	case cloudPingMsg:
		if msg.generation != m.CloudRequestGeneration {
			return m, nil
		}
		if msg.origin == cloudPingFromStatus {
			if m.Screen == ScreenCloudStatus && msg.serverURL == m.CloudStatusServerURL {
				m.CloudStatusHealth = msg.status
				if msg.err != nil {
					m.CloudStatusHealthError = msg.err.Error()
				} else {
					m.CloudStatusHealthError = ""
				}
			}
			return m, nil
		}
		if m.Screen != ScreenCloudConfig || msg.serverURL != m.CloudConfigInput.Value() {
			return m, nil
		}
		m.CloudConfigSaving = false
		m.CloudConfigPingStatus = msg.status
		if msg.err != nil {
			m.CloudConfigError = msg.err.Error()
			return m, nil
		}
		if m.CloudConfigTest {
			return m, nil
		}
		if msg.status != "reachable" && msg.status != "unauthorized" {
			m.CloudConfigError = "server is unreachable"
			return m, nil
		}
		if m.store == nil {
			m.CloudConfigError = "store is unavailable"
			return m, nil
		}
		if err := saveCloudServerURL(m.store.DataDir(), msg.serverURL); err != nil {
			m.CloudConfigError = err.Error()
			return m, nil
		}
		m.CloudConfigFocus = cloudConfigFocusSave
		m.CloudConfigInput.Blur()
		return m, nil

	case clipboardCopiedMsg:
		// Emit the OSC 52 sequence to stdout so the terminal copies the content,
		// set the feedback label, and schedule its removal after 2 seconds.
		m.CopyFeedback = "✓ Copied!"
		return m, tea.Batch(
			tea.Println(msg.sequence),
			clearFeedbackAfter(2*time.Second),
		)

	case clipboardClearMsg:
		m.CopyFeedback = ""
		return m, nil

	case spinner.TickMsg:
		// Only forward spinner ticks while an operation is in progress.
		if m.SetupInstalling || m.CloudConfigSaving {
			var cmd tea.Cmd
			m.SetupSpinner, cmd = m.SetupSpinner.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	return m, nil
}

// ─── Key Press Router ────────────────────────────────────────────────────────

func (m Model) handleKeyPress(key string) (tea.Model, tea.Cmd) {
	// Clear error on any keypress
	m.ErrorMsg = ""

	switch m.Screen {
	case ScreenDashboard:
		return m.handleDashboardKeys(key)
	case ScreenSearch:
		return m.handleSearchKeys(key)
	case ScreenSearchResults:
		return m.handleSearchResultsKeys(key)
	case ScreenRecent:
		return m.handleRecentKeys(key)
	case ScreenObservationDetail:
		return m.handleObservationDetailKeys(key)
	case ScreenTimeline:
		return m.handleTimelineKeys(key)
	case ScreenSessions:
		return m.handleSessionsKeys(key)
	case ScreenSessionDetail:
		return m.handleSessionDetailKeys(key)
	case ScreenSetup:
		return m.handleSetupKeys(key)
	case ScreenCloudSettings:
		return m.handleCloudSettingsKeys(key)
	case ScreenCloudConfig:
		return m.handleCloudConfigKeys(key)
	case ScreenCloudStatus:
		return m.handleCloudStatusKeys(key)
	case ScreenCloudEnrollment:
		return m.handleCloudEnrollmentKeys(key)
	}
	return m, nil
}

// ─── Dashboard ───────────────────────────────────────────────────────────────

var dashboardMenuItems = []string{
	"Search memories",
	"Recent observations",
	"Browse sessions",
	"Setup agent plugin",
	"Cloud sync settings",
	"Quit",
}

var cloudSettingsMenuItems = []string{
	"Configure server",
	"View status",
	"Enroll projects",
	"Back",
}

func (m Model) handleDashboardKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.Cursor > 0 {
			m.Cursor--
		}
	case "down", "j":
		if m.Cursor < len(dashboardMenuItems)-1 {
			m.Cursor++
		}
	case "enter", " ":
		return m.handleDashboardSelection()
	case "s", "/":
		m.PrevScreen = ScreenDashboard
		m.Screen = ScreenSearch
		m.Cursor = 0
		m.SearchInput.SetValue("")
		m.SearchInput.Focus()
		return m, nil
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) handleDashboardSelection() (tea.Model, tea.Cmd) {
	switch m.Cursor {
	case 0: // Search
		m.PrevScreen = ScreenDashboard
		m.Screen = ScreenSearch
		m.Cursor = 0
		m.SearchInput.SetValue("")
		m.SearchInput.Focus()
		return m, nil
	case 1: // Recent observations
		m.PrevScreen = ScreenDashboard
		m.Screen = ScreenRecent
		m.Cursor = 0
		m.Scroll = 0
		return m, loadRecentObservations(m.store)
	case 2: // Sessions
		m.PrevScreen = ScreenDashboard
		m.Screen = ScreenSessions
		m.Cursor = 0
		m.Scroll = 0
		return m, loadRecentSessions(m.store)
	case 3: // Setup
		m.PrevScreen = ScreenDashboard
		m.Screen = ScreenSetup
		m.Cursor = 0
		m.SetupAgents = setup.SupportedAgents()
		m.SetupResult = nil
		m.SetupError = ""
		m.SetupDone = false
		m.SetupInstalling = false
		m.SetupInstallingName = ""
		return m, nil
	case 4: // Cloud sync settings
		m.PrevScreen = ScreenDashboard
		m.Screen = ScreenCloudSettings
		m.Cursor = 0
		return m, nil
	case 5: // Quit
		return m, tea.Quit
	}
	return m, nil
}

// ─── Search Input ────────────────────────────────────────────────────────────

func (m Model) handleSearchInputKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		query := m.SearchInput.Value()
		if query != "" {
			m.SearchInput.Blur()
			return m, searchMemories(m.store, query)
		}
		return m, nil
	case "esc":
		m.SearchInput.Blur()
		m.Screen = ScreenDashboard
		m.Cursor = 0
		return m, nil
	}

	// Let the text input component handle everything else
	var cmd tea.Cmd
	m.SearchInput, cmd = m.SearchInput.Update(msg)
	return m, cmd
}

func (m Model) handleSearchKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		m.Screen = ScreenDashboard
		m.Cursor = 0
		return m, nil
	case "i", "/":
		m.SearchInput.Focus()
		return m, nil
	}
	return m, nil
}

// ─── Search Results ──────────────────────────────────────────────────────────

func (m Model) handleSearchResultsKeys(key string) (tea.Model, tea.Cmd) {
	visibleItems := (m.Height - 10) / 2 // 2 lines per observation item
	if visibleItems < 3 {
		visibleItems = 3
	}

	switch key {
	case "up", "k":
		if m.Cursor > 0 {
			m.Cursor--
			// Scroll up if cursor goes above visible area
			if m.Cursor < m.Scroll {
				m.Scroll = m.Cursor
			}
		}
	case "down", "j":
		if m.Cursor < len(m.SearchResults)-1 {
			m.Cursor++
			// Scroll down if cursor goes below visible area
			if m.Cursor >= m.Scroll+visibleItems {
				m.Scroll = m.Cursor - visibleItems + 1
			}
		}
	case "enter":
		if len(m.SearchResults) > 0 && m.Cursor < len(m.SearchResults) {
			obsID := m.SearchResults[m.Cursor].ID
			m.PrevScreen = ScreenSearchResults
			return m, loadObservationDetail(m.store, obsID)
		}
	case "c":
		if len(m.SearchResults) > 0 && m.Cursor < len(m.SearchResults) {
			return m, copyToClipboard(m.SearchResults[m.Cursor].Content)
		}
	case "t":
		// Timeline for selected result
		if len(m.SearchResults) > 0 && m.Cursor < len(m.SearchResults) {
			obsID := m.SearchResults[m.Cursor].ID
			m.PrevScreen = ScreenSearchResults
			return m, loadTimeline(m.store, obsID)
		}
	case "/", "s":
		m.PrevScreen = ScreenSearchResults
		m.Screen = ScreenSearch
		m.SearchInput.Focus()
		return m, nil
	case "esc", "q":
		m.PrevScreen = ScreenDashboard
		m.Screen = ScreenSearch
		m.Cursor = 0
		m.Scroll = 0
		m.SearchInput.Focus()
		return m, nil
	}
	return m, nil
}

// ─── Recent Observations ─────────────────────────────────────────────────────

func (m Model) handleRecentKeys(key string) (tea.Model, tea.Cmd) {
	visibleItems := (m.Height - 8) / 2 // 2 lines per observation item
	if visibleItems < 3 {
		visibleItems = 3
	}

	switch key {
	case "up", "k":
		if m.Cursor > 0 {
			m.Cursor--
			if m.Cursor < m.Scroll {
				m.Scroll = m.Cursor
			}
		}
	case "down", "j":
		if m.Cursor < len(m.RecentObservations)-1 {
			m.Cursor++
			if m.Cursor >= m.Scroll+visibleItems {
				m.Scroll = m.Cursor - visibleItems + 1
			}
		}
	case "enter":
		if len(m.RecentObservations) > 0 && m.Cursor < len(m.RecentObservations) {
			obsID := m.RecentObservations[m.Cursor].ID
			m.PrevScreen = ScreenRecent
			return m, loadObservationDetail(m.store, obsID)
		}
	case "c":
		if len(m.RecentObservations) > 0 && m.Cursor < len(m.RecentObservations) {
			return m, copyToClipboard(m.RecentObservations[m.Cursor].Content)
		}
	case "t":
		if len(m.RecentObservations) > 0 && m.Cursor < len(m.RecentObservations) {
			obsID := m.RecentObservations[m.Cursor].ID
			m.PrevScreen = ScreenRecent
			return m, loadTimeline(m.store, obsID)
		}
	case "esc", "q":
		m.Screen = ScreenDashboard
		m.Cursor = 0
		m.Scroll = 0
		return m, loadStats(m.store)
	}
	return m, nil
}

// ─── Observation Detail ──────────────────────────────────────────────────────

func (m Model) handleObservationDetailKeys(key string) (tea.Model, tea.Cmd) {
	m.DetailScroll = m.clampDetailScroll()
	switch key {
	case "up", "k":
		if m.DetailScroll > 0 {
			m.DetailScroll--
		}
	case "down", "j":
		if m.DetailScroll < m.observationDetailMaxScroll() {
			m.DetailScroll++
		}
	case "c":
		if m.SelectedObservation != nil {
			return m, copyToClipboard(m.SelectedObservation.Content)
		}
	case "t":
		// View timeline for this observation
		if m.SelectedObservation != nil {
			return m, loadTimeline(m.store, m.SelectedObservation.ID)
		}
	case "esc", "q":
		m.Screen = m.PrevScreen
		m.Cursor = 0
		m.DetailScroll = 0
		return m, m.refreshScreen(m.PrevScreen)
	}
	return m, nil
}

// ─── Timeline ────────────────────────────────────────────────────────────────

func (m Model) handleTimelineKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.Scroll > 0 {
			m.Scroll--
		}
	case "down", "j":
		m.Scroll++
	case "esc", "q":
		m.Screen = m.PrevScreen
		m.Cursor = 0
		m.Scroll = 0
		return m, m.refreshScreen(m.PrevScreen)
	}
	return m, nil
}

// ─── Sessions ────────────────────────────────────────────────────────────────

func (m Model) handleSessionsKeys(key string) (tea.Model, tea.Cmd) {
	switch m.SessionDeleteState {
	case SessionDeleteStateDeleting:
		return m, nil
	case SessionDeleteStatePrompt:
		switch key {
		case "y", "Y":
			if m.SessionDeleteID == "" {
				m = m.resetSessionDeleteState()
				return m, nil
			}
			sessionID := m.SessionDeleteID
			m.SessionDeleteState = SessionDeleteStateDeleting
			return m, deleteSession(m.store, sessionID)
		case "n", "N", "esc":
			m = m.resetSessionDeleteState()
			return m, nil
		}
		return m, nil
	}

	visibleItems := m.Height - 8
	if visibleItems < 5 {
		visibleItems = 5
	}

	switch key {
	case "up", "k":
		if m.Cursor > 0 {
			m.Cursor--
			if m.Cursor < m.Scroll {
				m.Scroll = m.Cursor
			}
		}
	case "down", "j":
		if m.Cursor < len(m.Sessions)-1 {
			m.Cursor++
			if m.Cursor >= m.Scroll+visibleItems {
				m.Scroll = m.Cursor - visibleItems + 1
			}
		}
	case "enter":
		if len(m.Sessions) > 0 && m.Cursor < len(m.Sessions) {
			m.SelectedSessionIdx = m.Cursor
			m.PrevScreen = ScreenSessions
			sessionID := m.Sessions[m.Cursor].ID
			return m, loadSessionObservations(m.store, sessionID)
		}
	case "d", "D":
		if len(m.Sessions) > 0 && m.Cursor < len(m.Sessions) {
			session := m.Sessions[m.Cursor]
			m.SessionDeleteState = SessionDeleteStatePrompt
			m.SessionDeleteID = session.ID
			m.SessionDeleteProject = session.Project
		}
	case "esc", "q":
		m.Screen = ScreenDashboard
		m.Cursor = 0
		m.Scroll = 0
		m = m.resetSessionDeleteState()
		return m, loadStats(m.store)
	}
	return m, nil
}

// ─── Session Detail ──────────────────────────────────────────────────────────

func (m Model) handleSessionDetailKeys(key string) (tea.Model, tea.Cmd) {
	visibleItems := (m.Height - 12) / 2 // 2 lines per observation item
	if visibleItems < 3 {
		visibleItems = 3
	}

	switch key {
	case "up", "k":
		if m.Cursor > 0 {
			m.Cursor--
			if m.Cursor < m.SessionDetailScroll {
				m.SessionDetailScroll = m.Cursor
			}
		}
	case "down", "j":
		if m.Cursor < len(m.SessionObservations)-1 {
			m.Cursor++
			if m.Cursor >= m.SessionDetailScroll+visibleItems {
				m.SessionDetailScroll = m.Cursor - visibleItems + 1
			}
		}
	case "enter":
		if len(m.SessionObservations) > 0 && m.Cursor < len(m.SessionObservations) {
			obsID := m.SessionObservations[m.Cursor].ID
			m.PrevScreen = ScreenSessionDetail
			return m, loadObservationDetail(m.store, obsID)
		}
	case "c":
		if len(m.SessionObservations) > 0 && m.Cursor < len(m.SessionObservations) {
			return m, copyToClipboard(m.SessionObservations[m.Cursor].Content)
		}
	case "t":
		if len(m.SessionObservations) > 0 && m.Cursor < len(m.SessionObservations) {
			obsID := m.SessionObservations[m.Cursor].ID
			m.PrevScreen = ScreenSessionDetail
			return m, loadTimeline(m.store, obsID)
		}
	case "esc", "q":
		m.Screen = ScreenSessions
		m.Cursor = m.SelectedSessionIdx
		m.SessionDetailScroll = 0
		return m, loadRecentSessions(m.store)
	}
	return m, nil
}

func (m Model) clampViewport() Model {
	switch m.Screen {
	case ScreenObservationDetail:
		m.DetailScroll = m.clampDetailScroll()
	}
	return m
}

// ─── Setup ───────────────────────────────────────────────────────────────────

func (m Model) handleSetupKeys(key string) (tea.Model, tea.Cmd) {
	// While installing, block all keys
	if m.SetupInstalling {
		return m, nil
	}

	// Allowlist prompt: y/n
	if m.SetupAllowlistPrompt {
		switch key {
		case "y", "Y":
			m.SetupAllowlistPrompt = false
			m.SetupDone = true
			if err := addClaudeCodeAllowlistFn(); err != nil {
				m.SetupAllowlistError = err.Error()
			} else {
				m.SetupAllowlistApplied = true
			}
			return m, nil
		case "n", "N", "esc":
			m.SetupAllowlistPrompt = false
			m.SetupDone = true
			return m, nil
		}
		return m, nil
	}

	// After install completed, any key goes back
	if m.SetupDone {
		switch key {
		case "esc", "q", "enter":
			m.Screen = ScreenDashboard
			m.Cursor = 0
			m.SetupDone = false
			m.SetupResult = nil
			m.SetupError = ""
			m.SetupAllowlistApplied = false
			m.SetupAllowlistError = ""
			return m, loadStats(m.store)
		}
		return m, nil
	}

	switch key {
	case "up", "k":
		if m.Cursor > 0 {
			m.Cursor--
		}
	case "down", "j":
		if m.Cursor < len(m.SetupAgents)-1 {
			m.Cursor++
		}
	case "enter":
		if len(m.SetupAgents) > 0 && m.Cursor < len(m.SetupAgents) {
			agent := m.SetupAgents[m.Cursor]
			m.SetupInstalling = true
			m.SetupInstallingName = agent.Name
			return m, tea.Batch(m.SetupSpinner.Tick, installAgent(agent.Name))
		}
	case "esc", "q":
		m.Screen = ScreenDashboard
		m.Cursor = 0
		return m, loadStats(m.store)
	}
	return m, nil
}

// ─── Cloud Settings ──────────────────────────────────────────────────────────

func (m Model) handleCloudSettingsKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.Cursor > 0 {
			m.Cursor--
		}
	case "down", "j":
		if m.Cursor < len(cloudSettingsMenuItems)-1 {
			m.Cursor++
		}
	case "enter", " ":
		switch m.Cursor {
		case 0:
			m.Screen = ScreenCloudConfig
			m.CloudConfigError = ""
			m.CloudConfigPingStatus = ""
			m.CloudConfigFocus = cloudConfigFocusInput
			if m.store == nil {
				m.CloudConfigError = "store is unavailable"
				return m, nil
			}
			m.CloudRequestGeneration++
			return m, loadCloudConfigCmd(m.store.DataDir(), m.CloudRequestGeneration)
		case 1:
			m.Screen = ScreenCloudStatus
			m.CloudRequestGeneration++
			m.CloudStatusLoading = true
			m.CloudStatusLastError = ""
			m.CloudStatusHealthError = ""
			return m, loadCloudStatusCmd(m.store, m.CloudRequestGeneration)
		case 2:
			m.Screen = ScreenCloudEnrollment
			m.Cursor = 0
			m.CloudEnrollmentLoading = true
			m.CloudEnrollmentError = ""
			return m, loadCloudEnrollmentCmd(m.store)
		case 3: // Back
			m.Screen = ScreenDashboard
			m.Cursor = 0
			return m, loadStats(m.store)
		}
	case "esc", "q":
		m.Screen = ScreenDashboard
		m.Cursor = 0
		return m, loadStats(m.store)
	}
	return m, nil
}

func (m Model) handleCloudConfigInputKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab":
		m.CloudConfigInput.Blur()
		m.CloudConfigFocus = nextCloudConfigFocus(m.CloudConfigFocus)
		return m, nil
	case "shift+tab":
		m.CloudConfigInput.Blur()
		m.CloudConfigFocus = previousCloudConfigFocus(m.CloudConfigFocus)
		if m.CloudConfigFocus == cloudConfigFocusInput {
			m.CloudConfigInput.Focus()
		}
		return m, nil
	case "enter":
		m.CloudConfigInput.Blur()
		m.CloudConfigFocus = cloudConfigFocusTest
		return m, nil
	case "esc":
		m.CloudConfigInput.Blur()
		m.Screen = ScreenCloudSettings
		m.Cursor = 0
		return m, nil
	}
	var cmd tea.Cmd
	m.CloudConfigInput, cmd = m.CloudConfigInput.Update(msg)
	return m, cmd
}

func (m Model) handleCloudConfigKeys(key string) (tea.Model, tea.Cmd) {
	if m.CloudConfigSaving {
		return m, nil
	}
	switch key {
	case "tab":
		m.CloudConfigFocus = nextCloudConfigFocus(m.CloudConfigFocus)
	case "shift+tab":
		m.CloudConfigFocus = previousCloudConfigFocus(m.CloudConfigFocus)
	case "up", "k":
		m.CloudConfigFocus = previousCloudConfigFocus(m.CloudConfigFocus)
	case "down", "j":
		m.CloudConfigFocus = nextCloudConfigFocus(m.CloudConfigFocus)
	case "i":
		m.CloudConfigFocus = cloudConfigFocusInput
		m.CloudConfigInput.Focus()
		return m, nil
	case "esc", "q":
		m.CloudConfigInput.Blur()
		m.Screen = ScreenCloudSettings
		m.Cursor = 0
		return m, nil
	case "enter", " ":
		switch m.CloudConfigFocus {
		case cloudConfigFocusInput:
			m.CloudConfigInput.Focus()
			return m, nil
		case cloudConfigFocusCancel:
			m.Screen = ScreenCloudSettings
			m.Cursor = 0
			return m, nil
		case cloudConfigFocusTest, cloudConfigFocusSave:
			serverURL, err := cloudconfig.ValidateServerURL(m.CloudConfigInput.Value())
			if err != nil {
				m.CloudConfigError = err.Error()
				return m, nil
			}
			if m.store == nil {
				m.CloudConfigError = "store is unavailable"
				return m, nil
			}
			token, _ := cloudconfig.EffectiveToken(m.store.DataDir())
			m.CloudConfigError = ""
			m.CloudConfigPingStatus = "checking"
			m.CloudConfigTest = m.CloudConfigFocus == cloudConfigFocusTest
			m.CloudConfigSaving = true
			m.CloudRequestGeneration++
			return m, tea.Batch(m.SetupSpinner.Tick, pingCloudServer(cloudPingFromConfig, m.CloudRequestGeneration, serverURL, token))
		}
	}
	m.CloudConfigInput.Blur()
	if m.CloudConfigFocus == cloudConfigFocusInput {
		m.CloudConfigInput.Focus()
	}
	return m, nil
}

func nextCloudConfigFocus(focus int) int {
	if focus >= cloudConfigFocusCancel {
		return cloudConfigFocusInput
	}
	return focus + 1
}

func previousCloudConfigFocus(focus int) int {
	if focus <= cloudConfigFocusInput {
		return cloudConfigFocusCancel
	}
	return focus - 1
}

func (m Model) handleCloudStatusKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "r":
		m.CloudRequestGeneration++
		m.CloudStatusLoading = true
		m.CloudStatusHealthError = ""
		return m, loadCloudStatusCmd(m.store, m.CloudRequestGeneration)
	case "esc", "q":
		m.Screen = ScreenCloudSettings
		m.Cursor = 1
	}
	return m, nil
}

func (m Model) handleCloudEnrollmentKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.Cursor > 0 {
			m.Cursor--
		}
	case "down", "j":
		if m.Cursor < len(m.CloudEnrollmentItems)-1 {
			m.Cursor++
		}
	case "enter", " ":
		if !m.CloudEnrollmentLoading && m.Cursor >= 0 && m.Cursor < len(m.CloudEnrollmentItems) {
			m.CloudEnrollmentLoading = true
			return m, toggleCloudEnrollmentCmd(m.store, m.CloudEnrollmentItems[m.Cursor])
		}
	case "r":
		m.CloudEnrollmentLoading = true
		return m, loadCloudEnrollmentCmd(m.store)
	case "esc", "q":
		m.Screen = ScreenCloudSettings
		m.Cursor = 2
	}
	return m, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (m Model) resetSessionDeleteState() Model {
	m.SessionDeleteState = SessionDeleteStateNone
	m.SessionDeleteID = ""
	m.SessionDeleteProject = ""
	return m
}

func sessionDeleteErrorMessage(sessionID string, err error) string {
	if errors.Is(err, store.ErrSessionHasObservations) {
		return fmt.Sprintf("Cannot delete session %q: it still has observations. Delete or move observations first.", sessionID)
	}
	if errors.Is(err, store.ErrSessionNotFound) {
		return fmt.Sprintf("Cannot delete session %q: session not found.", sessionID)
	}
	return fmt.Sprintf("Failed to delete session %q: %v", sessionID, err)
}

// refreshScreen returns the appropriate data-loading Cmd for a given screen.
// Used when navigating back so lists show fresh data from the DB.
func (m Model) refreshScreen(screen Screen) tea.Cmd {
	switch screen {
	case ScreenDashboard:
		return loadStats(m.store)
	case ScreenRecent:
		return loadRecentObservations(m.store)
	case ScreenSessions:
		return loadRecentSessions(m.store)
	default:
		return nil
	}
}
