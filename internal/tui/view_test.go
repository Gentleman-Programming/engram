package tui

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/setup"
	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/Gentleman-Programming/engram/internal/version"
)

func TestTruncateStr(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "unchanged", in: "short", max: 10, want: "short"},
		{name: "replaces newlines", in: "a\nb", max: 10, want: "a b"},
		{name: "truncated", in: "abcdefghijklmnopqrstuvwxyz", max: 5, want: "abcde..."},
		{name: "spanish accents", in: "Decisión de arquitectura", max: 8, want: "Decisión..."},
		{name: "emoji", in: "🐛🔧🚀✨🎉💡", max: 3, want: "🐛🔧🚀..."},
		{name: "mixed ascii and multibyte", in: "café☕latte", max: 5, want: "café☕..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateStr(tt.in, tt.max)
			if got != tt.want {
				t.Fatalf("truncateStr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderObservationListItem(t *testing.T) {
	m := New(nil, "")
	m.Cursor = 1
	project := "engram"

	line := m.renderObservationListItem(
		1,
		42,
		"bugfix",
		"Title here",
		"content line 1\ncontent line 2",
		"2026-01-01",
		&project,
		"active",
		nil,
		true,
	)

	if !strings.Contains(line, "▸") {
		t.Fatal("selected item should include cursor marker")
	}
	if !strings.Contains(line, "Title here") {
		t.Fatal("line should include title")
	}
	if !strings.Contains(line, "content line 1 content line 2") {
		t.Fatal("content preview should be rendered on second line")
	}
	if !strings.Contains(line, "engram") {
		t.Fatal("project label should be rendered when project is set")
	}
	if !strings.Contains(line, "pinned") {
		t.Fatal("pinned item should include pin indicator")
	}

	reviewAfter := "2026-01-01 00:00:00"
	staleLine := m.renderObservationListItem(0, 43, "decision", "Needs review", "content", "2026-01-01", &project, "needs_review", &reviewAfter, false)
	if !strings.Contains(staleLine, "needs_review") {
		t.Fatal("stale item should include needs_review badge")
	}
}

func TestViewCloudConfigRendersTokenSource(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudConfig
	m.CloudConfigServerURL = "https://cloud.example.com"
	m.CloudConfigTokenSource = TokenSourceNone

	out := m.View()
	if !strings.Contains(out, "Configure cloud server") {
		t.Fatal("view should render screen title")
	}
	if !strings.Contains(out, "Server URL") {
		t.Fatal("view should render server URL label")
	}
	if !strings.Contains(out, "Token:") {
		t.Fatal("view should render token label")
	}
	if !strings.Contains(out, TokenSourceNone) {
		t.Fatalf("view should render token source %q", TokenSourceNone)
	}

	m.CloudConfigTokenSource = TokenSourceEnv
	out = m.View()
	if !strings.Contains(out, TokenSourceEnv) {
		t.Fatalf("view should render env token source %q", TokenSourceEnv)
	}

	m.CloudConfigTokenSource = TokenSourceFile
	out = m.View()
	if !strings.Contains(out, TokenSourceFile) {
		t.Fatalf("view should render file token source %q", TokenSourceFile)
	}
}

func TestViewCloudConfigBranches(t *testing.T) {
	// Empty input value and non-input focus renders value style.
	m := New(nil, "")
	m.Screen = ScreenCloudConfig
	m.CloudConfigFocus = cloudConfigFocusTest
	m.CloudConfigInput.SetValue("")
	out := m.viewCloudConfig()
	if !strings.Contains(out, "Server URL") {
		t.Fatal("view should render URL label for empty input")
	}

	// Spinner branch during save.
	m.CloudConfigFocus = cloudConfigFocusSave
	m.CloudConfigInput.SetValue("https://cloud.example.com")
	m.CloudConfigSaving = true
	out = m.viewCloudConfig()
	if !strings.Contains(out, "Pinging server") {
		t.Fatalf("view should render spinner branch, got %q", out)
	}

	// Ping status branch.
	m.CloudConfigSaving = false
	m.CloudConfigPingStatus = "reachable"
	out = m.viewCloudConfig()
	if !strings.Contains(out, "Status: reachable") {
		t.Fatalf("view should render ping status, got %q", out)
	}

	// Token source hint hidden for env token.
	m.CloudConfigTokenSource = TokenSourceEnv
	out = m.viewCloudConfig()
	if strings.Contains(out, "Set ENGRAM_CLOUD_TOKEN") {
		t.Fatal("env token source should not show fallback hint")
	}

	// Token source hint shown for file token.
	m.CloudConfigTokenSource = TokenSourceFile
	out = m.viewCloudConfig()
	if !strings.Contains(out, "Set ENGRAM_CLOUD_TOKEN") {
		t.Fatal("file token source should show fallback hint")
	}

	// Token source hint shown when no token.
	m.CloudConfigTokenSource = TokenSourceNone
	out = m.viewCloudConfig()
	if !strings.Contains(out, "Set ENGRAM_CLOUD_TOKEN") {
		t.Fatal("no token source should show fallback hint")
	}
}

func TestViewCloudStatusRendersConfiguredState(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudStatus
	m.CloudStatusServerURL = "https://cloud.example.com"
	m.CloudStatusTokenSource = TokenSourceEnv
	m.CloudStatusHealth = "reachable"
	m.CloudStatusLastSync = "2026-07-07 12:00:00"
	m.CloudStatusPendingCount = 2
	m.CloudStatusLastError = "last fail"

	out := m.View()
	if !strings.Contains(out, "Cloud status") {
		t.Fatal("view should render screen title")
	}
	if !strings.Contains(out, "https://cloud.example.com") {
		t.Fatal("view should render server URL")
	}
	if !strings.Contains(out, "reachable") {
		t.Fatal("view should render health")
	}
	if !strings.Contains(out, TokenSourceEnv) {
		t.Fatal("view should render token source")
	}
	if !strings.Contains(out, "2026-07-07 12:00:00") {
		t.Fatal("view should render last sync")
	}
	if !strings.Contains(out, "2") {
		t.Fatal("view should render pending count")
	}
	if !strings.Contains(out, "last fail") {
		t.Fatal("view should render last error")
	}
}

func TestViewCloudStatusNotConfigured(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudStatus
	m.CloudStatusServerURL = ""

	out := m.View()
	if !strings.Contains(out, "Cloud status: not configured") {
		t.Fatalf("view should show not configured, got %q", out)
	}
}

func TestViewCloudStatusHealthAndTokenBranches(t *testing.T) {
	tests := []struct {
		name        string
		health      string
		tokenSource string
		pending     int64
		lastError   string
		want        []string
	}{
		{name: "reachable env", health: "reachable", tokenSource: TokenSourceEnv, pending: 0, lastError: "", want: []string{"reachable", TokenSourceEnv, "0"}},
		{name: "unreachable file error", health: "unreachable", tokenSource: TokenSourceFile, pending: 5, lastError: "boom", want: []string{"unreachable", TokenSourceFile, "5", "boom"}},
		{name: "unknown none", health: "unknown", tokenSource: TokenSourceNone, pending: 0, lastError: "", want: []string{"unknown", TokenSourceNone, "0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(nil, "")
			m.Screen = ScreenCloudStatus
			m.CloudStatusServerURL = "https://cloud.example.com"
			m.CloudStatusHealth = tt.health
			m.CloudStatusTokenSource = tt.tokenSource
			m.CloudStatusPendingCount = tt.pending
			m.CloudStatusLastError = tt.lastError
			out := m.View()
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Fatalf("output missing %q: %s", w, out)
				}
			}
		})
	}
}

func TestViewRouterAndErrorRendering(t *testing.T) {
	m := New(nil, "")
	m.Screen = Screen(999)
	m.ErrorMsg = "boom"

	out := m.View()
	if !strings.Contains(out, "Unknown screen") {
		t.Fatal("unknown screen fallback text missing")
	}
	if !strings.Contains(out, "Error: boom") {
		t.Fatal("error message should be appended to view")
	}
}

func TestViewCloudEnrollmentRendersProjects(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudEnrollment
	m.CloudEnrollmentItems = []cloudEnrollmentItem{
		{project: "sias-app", enrolled: true},
		{project: "dotfiles", enrolled: false},
	}
	m.Cursor = 1

	out := m.viewCloudEnrollment()
	if !strings.Contains(out, "Enroll projects") {
		t.Fatal("view should render screen title")
	}
	if !strings.Contains(out, "sias-app") {
		t.Fatal("view should render first project")
	}
	if !strings.Contains(out, "dotfiles") {
		t.Fatal("view should render second project")
	}
	if !strings.Contains(out, "[enrolled]") {
		t.Fatal("view should render enrolled badge")
	}
	if !strings.Contains(out, "[not enrolled]") {
		t.Fatal("view should render not enrolled badge")
	}
}

func TestViewCloudEnrollmentEmptyState(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudEnrollment
	m.CloudEnrollmentItems = nil

	out := m.viewCloudEnrollment()
	if !strings.Contains(out, "No projects") {
		t.Fatalf("view should show empty state, got %q", out)
	}
	if !strings.Contains(out, "esc back") {
		t.Fatal("empty state should include esc back hint")
	}
}

func TestViewCloudEnrollmentLongName(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudEnrollment
	m.CloudEnrollmentItems = []cloudEnrollmentItem{
		{project: strings.Repeat("a", 200), enrolled: false},
	}
	m.Cursor = 0

	out := m.viewCloudEnrollment()
	if !strings.Contains(out, "[not enrolled]") {
		t.Fatal("view should render badge for long project name")
	}
}

func TestViewCloudEnrollmentErrorSurfaced(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenCloudEnrollment
	m.CloudEnrollmentError = "database locked"

	out := m.viewCloudEnrollment()
	if !strings.Contains(out, "database locked") {
		t.Fatal("view should render enrollment error")
	}
}

func TestViewSearchResultsAndScrollIndicator(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenSearchResults
	m.Height = 14
	m.SearchQuery = "needle"
	m.SearchResults = []store.SearchResult{
		{Observation: store.Observation{ID: 1, Type: "bugfix", Title: "one", Content: "a", CreatedAt: "2026-01-01"}},
		{Observation: store.Observation{ID: 2, Type: "bugfix", Title: "two", Content: "b", CreatedAt: "2026-01-01"}},
		{Observation: store.Observation{ID: 3, Type: "bugfix", Title: "three", Content: "c", CreatedAt: "2026-01-01"}},
		{Observation: store.Observation{ID: 4, Type: "bugfix", Title: "four", Content: "d", CreatedAt: "2026-01-01"}},
	}

	out := m.viewSearchResults()
	if !strings.Contains(out, "Search: \"needle\"") {
		t.Fatal("search header missing")
	}
	if !strings.Contains(out, "showing 1-3 of 4") {
		t.Fatal("scroll indicator missing for overflowing list")
	}

	m.SearchResults = nil
	out = m.viewSearchResults()
	if !strings.Contains(out, "No memories found") {
		t.Fatal("empty result state missing")
	}
}

func TestViewSetupBranches(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenSetup

	m.SetupInstalling = true
	m.SetupInstallingName = "opencode"
	out := m.viewSetup()
	if !strings.Contains(out, "Installing opencode plugin") {
		t.Fatal("installing state should render progress line")
	}

	m.SetupInstalling = false
	m.SetupDone = true
	m.SetupResult = &setup.Result{Agent: "opencode", Destination: "/tmp/plugins", Files: 2}
	out = m.viewSetup()
	if !strings.Contains(out, "Installed opencode plugin") {
		t.Fatal("success state should render install result")
	}
	if !strings.Contains(out, "Next Steps") {
		t.Fatal("success state should render post-install instructions")
	}

	m.SetupResult = nil
	m.SetupError = "permission denied"
	out = m.viewSetup()
	if !strings.Contains(out, "Installation failed") {
		t.Fatal("error state should render failure message")
	}
}

func TestViewDashboardSearchAndRecent(t *testing.T) {
	m := New(nil, "")
	m.Cursor = 1
	m.Stats = &store.Stats{
		TotalSessions:     3,
		TotalObservations: 7,
		TotalPrompts:      2,
		Projects:          []string{"a", "b", "c", "d", "e", "f"},
	}

	out := m.viewDashboard()
	if !strings.Contains(out, "engram") || !strings.Contains(out, "Actions") {
		t.Fatal("dashboard should include header and actions")
	}
	if !strings.Contains(out, "...and 1 more projects") {
		t.Fatal("dashboard should show overflow projects indicator")
	}

	m.UpdateStatus = version.StatusUpdateAvailable
	m.UpdateMsg = "Update available: 1.10.7 -> 1.10.8"
	out = m.viewDashboard()
	if !strings.Contains(out, "Update available") {
		t.Fatal("dashboard should render update banner")
	}

	m.UpdateStatus = version.StatusCheckFailed
	m.UpdateMsg = "Could not check for updates: GitHub took too long to respond."
	out = m.viewDashboard()
	if !strings.Contains(out, "Could not check for updates") {
		t.Fatal("dashboard should render update failure banner")
	}

	m.Stats = nil
	out = m.viewDashboard()
	if !strings.Contains(out, "Loading stats") {
		t.Fatal("dashboard should render loading state when stats are nil")
	}

	m.Screen = ScreenSearch
	out = m.viewSearch()
	if !strings.Contains(out, "Search Memories") {
		t.Fatal("search view should render title")
	}

	m.Height = 14
	m.RecentObservations = []store.Observation{
		{ID: 1, Type: "bugfix", Title: "one", Content: "a", CreatedAt: "2026-01-01"},
		{ID: 2, Type: "bugfix", Title: "two", Content: "b", CreatedAt: "2026-01-01"},
		{ID: 3, Type: "bugfix", Title: "three", Content: "c", CreatedAt: "2026-01-01"},
		{ID: 4, Type: "bugfix", Title: "four", Content: "d", CreatedAt: "2026-01-01"},
	}
	out = m.viewRecent()
	if !strings.Contains(out, "Recent Observations") {
		t.Fatal("recent view should render title")
	}
	if !strings.Contains(out, "showing 1-3 of 4") {
		t.Fatal("recent view should render scroll indicator when needed")
	}

	m.RecentObservations = nil
	out = m.viewRecent()
	if !strings.Contains(out, "No observations yet") {
		t.Fatal("recent view should render empty state")
	}

	// Force minimum visible items branch
	m.Height = 8
	m.RecentObservations = []store.Observation{{ID: 1, Type: "bugfix", Title: "one", Content: "a", CreatedAt: "2026-01-01"}}
	out = m.viewRecent()
	if !strings.Contains(out, "Recent Observations") {
		t.Fatal("recent view should still render when height is very small")
	}
}

func TestViewObservationDetailTimelineSessionsAndSessionDetail(t *testing.T) {
	m := New(nil, "")
	m.Height = 22

	out := m.viewObservationDetail()
	if !strings.Contains(out, "Loading") {
		t.Fatal("detail view should render loading state when observation is nil")
	}

	tool := "bash"
	project := "engram"
	m.SelectedObservation = &store.Observation{
		ID:        42,
		Type:      "decision",
		Title:     "Architecture decision",
		SessionID: "session-1",
		CreatedAt: "2026-01-01",
		ToolName:  &tool,
		Project:   &project,
		Content:   strings.Repeat("line\n", 20),
		Pinned:    true,
	}
	reviewAfter := "2027-02-03 04:05:06"
	m.SelectedObservation.ReviewAfter = &reviewAfter
	m.DetailScroll = 99
	out = m.viewObservationDetail()
	if !strings.Contains(out, "Observation #42") || !strings.Contains(out, "Content") {
		t.Fatal("detail view should render metadata and content section")
	}
	if !strings.Contains(out, "State:") || !strings.Contains(out, "active") || !strings.Contains(out, "Review:") || !strings.Contains(out, "2027-02-03") {
		t.Fatal("detail view should render lifecycle state and review date")
	}
	if !strings.Contains(out, "Pinned:") || !strings.Contains(out, "true") {
		t.Fatal("detail view should render pinned state")
	}
	if !strings.Contains(out, "line") {
		t.Fatal("detail view should render content lines")
	}

	out = m.viewTimeline()
	if !strings.Contains(out, "Loading") {
		t.Fatal("timeline should render loading state when nil")
	}

	m.Timeline = &store.TimelineResult{
		Focus:        store.Observation{ID: 42, Type: "decision", Title: "focus", Content: "focus content"},
		Before:       []store.TimelineEntry{{ID: 40, Type: "bugfix", Title: "before title"}},
		After:        []store.TimelineEntry{{ID: 43, Type: "pattern", Title: "after title"}},
		SessionInfo:  &store.Session{ID: "session-1", Project: "engram"},
		TotalInRange: 3,
	}
	out = m.viewTimeline()
	if !strings.Contains(out, "Timeline") || !strings.Contains(out, "Before") || !strings.Contains(out, "After") {
		t.Fatal("timeline should render focus and before/after sections")
	}

	m.Sessions = nil
	out = m.viewSessions()
	if !strings.Contains(out, "No sessions yet") {
		t.Fatal("sessions view should render empty state")
	}

	summary := "session summary"
	m.Height = 14
	m.Sessions = []store.SessionSummary{
		{ID: "s1", Project: "engram", StartedAt: "2026-01-01", Summary: &summary, ObservationCount: 2},
		{ID: "s2", Project: "engram", StartedAt: "2026-01-02", ObservationCount: 1},
		{ID: "s3", Project: "engram", StartedAt: "2026-01-03", ObservationCount: 1},
		{ID: "s4", Project: "engram", StartedAt: "2026-01-04", ObservationCount: 1},
		{ID: "s5", Project: "engram", StartedAt: "2026-01-05", ObservationCount: 1},
		{ID: "s6", Project: "engram", StartedAt: "2026-01-06", ObservationCount: 1},
		{ID: "s7", Project: "engram", StartedAt: "2026-01-07", ObservationCount: 1},
	}
	out = m.viewSessions()
	if !strings.Contains(out, "Sessions") || !strings.Contains(out, "showing 1-6 of 7") {
		t.Fatal("sessions view should render list and scroll indicator")
	}

	// Force minimum visible items branch
	m.Height = 2
	out = m.viewSessions()
	if !strings.Contains(out, "Sessions") {
		t.Fatal("sessions view should render when height is very small")
	}

	m.SelectedSessionIdx = 99
	out = m.viewSessionDetail()
	if !strings.Contains(out, "Session not found") {
		t.Fatal("session detail should guard invalid index")
	}

	m.SelectedSessionIdx = 0
	m.SessionObservations = nil
	out = m.viewSessionDetail()
	if !strings.Contains(out, "No observations in this session") {
		t.Fatal("session detail should render empty observations state")
	}

	m.Height = 16
	m.SessionObservations = []store.Observation{
		{ID: 1, Type: "bugfix", Title: "one", Content: "a", CreatedAt: "2026-01-01"},
		{ID: 2, Type: "bugfix", Title: "two", Content: "b", CreatedAt: "2026-01-01"},
		{ID: 3, Type: "bugfix", Title: "three", Content: "c", CreatedAt: "2026-01-01"},
		{ID: 4, Type: "bugfix", Title: "four", Content: "d", CreatedAt: "2026-01-01"},
	}
	out = m.viewSessionDetail()
	if !strings.Contains(out, "Observations (4)") {
		t.Fatal("session detail should show observations heading")
	}
}

func TestViewRouterCoversAllScreens(t *testing.T) {
	m := New(nil, "")
	m.Stats = &store.Stats{}
	m.SearchResults = []store.SearchResult{{Observation: store.Observation{ID: 1, Type: "bugfix", Title: "t", Content: "c", CreatedAt: "now"}}}
	m.SearchQuery = "q"
	m.RecentObservations = []store.Observation{{ID: 1, Type: "bugfix", Title: "t", Content: "c", CreatedAt: "now"}}
	m.SelectedObservation = &store.Observation{ID: 1, Type: "bugfix", Title: "t", Content: "c", CreatedAt: "now", SessionID: "s1"}
	m.Timeline = &store.TimelineResult{Focus: store.Observation{ID: 1, Type: "bugfix", Title: "t", Content: "c"}, TotalInRange: 1}
	m.Sessions = []store.SessionSummary{{ID: "s1", Project: "engram", StartedAt: "now", ObservationCount: 1}}
	m.SelectedSessionIdx = 0
	m.SessionObservations = []store.Observation{{ID: 1, Type: "bugfix", Title: "t", Content: "c", CreatedAt: "now"}}
	m.SetupAgents = []setup.Agent{{Name: "opencode", Description: "OpenCode", InstallDir: "/tmp"}}
	m.Height = 20

	tests := []struct {
		screen Screen
		want   string
	}{
		{screen: ScreenDashboard, want: "Actions"},
		{screen: ScreenSearch, want: "Search Memories"},
		{screen: ScreenSearchResults, want: "Search:"},
		{screen: ScreenRecent, want: "Recent Observations"},
		{screen: ScreenObservationDetail, want: "Observation #"},
		{screen: ScreenTimeline, want: "Timeline"},
		{screen: ScreenSessions, want: "Sessions"},
		{screen: ScreenSessionDetail, want: "Session:"},
		{screen: ScreenSetup, want: "Setup"},
		{screen: ScreenCloudSettings, want: "Cloud sync settings"},
		{screen: ScreenCloudConfig, want: "Configure cloud server"},
		{screen: ScreenCloudStatus, want: "Cloud status"},
		{screen: ScreenCloudEnrollment, want: "Enroll projects"},
	}

	for _, tt := range tests {
		m.Screen = tt.screen
		out := m.View()
		if !strings.Contains(out, tt.want) {
			t.Fatalf("screen %v output missing %q", tt.screen, tt.want)
		}
	}
}

func TestViewSetupRemainingBranches(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenSetup
	m.SetupAgents = []setup.Agent{
		{Name: "claude-code", Description: "Claude Code", InstallDir: "/tmp/claude"},
		{Name: "opencode", Description: "OpenCode", InstallDir: "/tmp/opencode"},
	}

	out := m.viewSetup()
	if !strings.Contains(out, "Select an agent to set up") || !strings.Contains(out, "Install to") {
		t.Fatal("setup selection mode should render options and install paths")
	}

	m.SetupInstalling = true
	m.SetupInstallingName = "claude-code"
	out = m.viewSetup()
	if !strings.Contains(out, "Running claude plugin marketplace add + install") {
		t.Fatal("setup installing should render claude-code specific progress text")
	}

	m.SetupInstalling = false
	m.SetupDone = true
	m.SetupError = ""
	m.SetupResult = &setup.Result{Agent: "claude-code", Destination: "/tmp/claude", Files: 0}
	out = m.viewSetup()
	if !strings.Contains(out, "Verify with: claude plugin list") {
		t.Fatal("setup success for claude-code should render next steps")
	}

	m.SetupResult = nil
	m.SetupError = ""
	out = m.viewSetup()
	if !strings.Contains(out, "enter/esc back to dashboard") {
		t.Fatal("setup done without result/error should still render return help")
	}
}

func TestViewSetupAllowlistPrompt(t *testing.T) {
	t.Run("renders allowlist prompt", func(t *testing.T) {
		m := New(nil, "")
		m.Screen = ScreenSetup
		m.SetupAllowlistPrompt = true
		m.SetupResult = &setup.Result{Agent: "claude-code", Destination: "claude plugin system"}

		out := m.viewSetup()
		if !strings.Contains(out, "Installed claude-code plugin") {
			t.Fatal("prompt should show install success")
		}
		if !strings.Contains(out, "Permissions Allowlist") {
			t.Fatal("prompt should show allowlist heading")
		}
		if !strings.Contains(out, "settings.json") {
			t.Fatal("prompt should mention settings.json")
		}
		if !strings.Contains(out, "[y] Yes") || !strings.Contains(out, "[n] No") {
			t.Fatal("prompt should show y/n options")
		}
	})

	t.Run("renders applied state", func(t *testing.T) {
		m := New(nil, "")
		m.Screen = ScreenSetup
		m.SetupDone = true
		m.SetupResult = &setup.Result{Agent: "claude-code", Destination: "claude plugin system"}
		m.SetupAllowlistApplied = true

		out := m.viewSetup()
		if !strings.Contains(out, "tools added to allowlist") {
			t.Fatal("should show allowlist success")
		}
	})

	t.Run("renders error state", func(t *testing.T) {
		m := New(nil, "")
		m.Screen = ScreenSetup
		m.SetupDone = true
		m.SetupResult = &setup.Result{Agent: "claude-code", Destination: "claude plugin system"}
		m.SetupAllowlistError = "permission denied"

		out := m.viewSetup()
		if !strings.Contains(out, "Allowlist update failed") {
			t.Fatal("should show allowlist error")
		}
		if !strings.Contains(out, "permission denied") {
			t.Fatal("should show error message")
		}
	})
}
