package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// ─── Update ──────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
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
		return m.handleKeyPress(msg.String())

	// ─── Data loaded messages ────────────────────────────────────────────
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
	}
	return m, nil
}

// ─── Dashboard ───────────────────────────────────────────────────────────────

var dashboardMenuItems = []string{
	"Search memories",
	"Recent observations",
	"Browse sessions",
	"Quit",
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
	case 3: // Quit
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
		m.Screen = m.PrevScreen
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
		m.Screen = m.PrevScreen
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
		m.Screen = ScreenSearch
		m.Cursor = 0
		m.Scroll = 0
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
		return m, nil
	}
	return m, nil
}

// ─── Observation Detail ──────────────────────────────────────────────────────

func (m Model) handleObservationDetailKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.DetailScroll > 0 {
			m.DetailScroll--
		}
	case "down", "j":
		m.DetailScroll++
	case "t":
		// View timeline for this observation
		if m.SelectedObservation != nil {
			return m, loadTimeline(m.store, m.SelectedObservation.ID)
		}
	case "esc", "q":
		m.Screen = m.PrevScreen
		m.Cursor = 0
		m.DetailScroll = 0
		return m, nil
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
		return m, nil
	}
	return m, nil
}

// ─── Sessions ────────────────────────────────────────────────────────────────

func (m Model) handleSessionsKeys(key string) (tea.Model, tea.Cmd) {
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
	case "esc", "q":
		m.Screen = ScreenDashboard
		m.Cursor = 0
		m.Scroll = 0
		return m, nil
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
		return m, nil
	}
	return m, nil
}
