package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/setup"
	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/Gentleman-Programming/engram/internal/version"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func TestUpdateHandlesWindowSizeAndCtrlC(t *testing.T) {
	m := New(nil, "")

	updatedModel, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	updated := updatedModel.(Model)
	if updated.Width != 120 || updated.Height != 40 {
		t.Fatalf("size = %dx%d", updated.Width, updated.Height)
	}
	if cmd != nil {
		t.Fatal("window size update should not return command")
	}

	_, quitCmd := updated.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quitCmd == nil {
		t.Fatal("ctrl+c should return quit command")
	}
}

func TestUpdateSearchInputFocusedHandlesEscAndEnter(t *testing.T) {
	fx := newTestFixture(t)
	m := New(fx.store, "")
	m.Screen = ScreenSearch
	m.PrevScreen = ScreenDashboard
	m.SearchInput.Focus()

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := updatedModel.(Model)
	if updated.Screen != ScreenDashboard {
		t.Fatalf("screen = %v, want %v", updated.Screen, ScreenDashboard)
	}
	if updated.SearchInput.Focused() {
		t.Fatal("search input should blur on esc")
	}
	if cmd != nil {
		t.Fatal("esc should not return command")
	}

	m = New(fx.store, "")
	m.Screen = ScreenSearch
	m.PrevScreen = ScreenDashboard
	m.SearchInput.Focus()
	m.SearchInput.SetValue("needle")

	updatedModel, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = updatedModel.(Model)
	if updated.SearchInput.Focused() {
		t.Fatal("search input should blur on enter")
	}
	if cmd == nil {
		t.Fatal("enter with query should return search command")
	}
}

func TestUpdateSearchResultsAndDetailScreenTransitions(t *testing.T) {
	m := New(nil, "")
	m.Screen = ScreenSearchResults
	m.Height = 14
	m.SearchResults = []store.SearchResult{
		{Observation: store.Observation{ID: 1}},
		{Observation: store.Observation{ID: 2}},
		{Observation: store.Observation{ID: 3}},
		{Observation: store.Observation{ID: 4}},
	}
	m.Cursor = 2
	m.Scroll = 0

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated := updatedModel.(Model)
	if updated.Cursor != 3 {
		t.Fatalf("cursor = %d, want 3", updated.Cursor)
	}
	if updated.Scroll != 1 {
		t.Fatalf("scroll = %d, want 1", updated.Scroll)
	}

	updatedModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = updatedModel.(Model)
	if updated.Screen != ScreenSearch {
		t.Fatalf("screen = %v, want %v", updated.Screen, ScreenSearch)
	}
	if !updated.SearchInput.Focused() {
		t.Fatal("search input should be focused after leaving results")
	}
}

func TestUpdateObservationDetailEscRefreshesPrevScreen(t *testing.T) {
	fx := newTestFixture(t)
	m := New(fx.store, "")
	m.Screen = ScreenObservationDetail
	m.PrevScreen = ScreenRecent
	m.DetailScroll = 3

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := updatedModel.(Model)
	if updated.Screen != ScreenRecent {
		t.Fatalf("screen = %v, want %v", updated.Screen, ScreenRecent)
	}
	if updated.DetailScroll != 0 {
		t.Fatalf("detail scroll = %d, want 0", updated.DetailScroll)
	}
	if cmd == nil {
		t.Fatal("expected refresh command when leaving detail view")
	}
}

func TestUpdateSetupFlowAndSpinnerTick(t *testing.T) {
	fx := newTestFixture(t)
	m := New(fx.store, "")
	m.Screen = ScreenSetup
	m.SetupAgents = []setup.Agent{{Name: "opencode", Description: "OpenCode", InstallDir: "/tmp"}}
	m.SetupDone = true
	m.SetupResult = &setup.Result{Agent: "opencode", Destination: "/tmp", Files: 1}

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := updatedModel.(Model)
	if updated.Screen != ScreenDashboard {
		t.Fatalf("screen = %v, want %v", updated.Screen, ScreenDashboard)
	}
	if updated.SetupDone {
		t.Fatal("setup done should reset after leaving setup screen")
	}
	if cmd == nil {
		t.Fatal("expected stats refresh command when leaving setup")
	}

	updated.SetupInstalling = false
	_, tickCmd := updated.Update(spinner.TickMsg{})
	if tickCmd != nil {
		t.Fatal("spinner tick should be ignored when not installing")
	}

	updated.SetupInstalling = true
	_, tickCmd = updated.Update(spinner.TickMsg{})
	if tickCmd == nil {
		t.Fatal("spinner tick should be forwarded while installing")
	}
}

func TestHandleDashboardAndSearchKeyPaths(t *testing.T) {
	fx := newTestFixture(t)
	m := New(fx.store, "")

	updatedModel, _ := m.handleDashboardKeys("s")
	updated := updatedModel.(Model)
	if updated.Screen != ScreenSearch || !updated.SearchInput.Focused() {
		t.Fatal("dashboard shortcut should open focused search")
	}

	m = New(fx.store, "")
	m.Cursor = 1
	updatedModel, cmd := m.handleDashboardSelection()
	updated = updatedModel.(Model)
	if updated.Screen != ScreenRecent {
		t.Fatalf("screen = %v, want %v", updated.Screen, ScreenRecent)
	}
	if cmd == nil {
		t.Fatal("recent selection should load observations")
	}

	m = New(fx.store, "")
	m.Cursor = 2
	updatedModel, cmd = m.handleDashboardSelection()
	updated = updatedModel.(Model)
	if updated.Screen != ScreenSessions {
		t.Fatalf("screen = %v, want %v", updated.Screen, ScreenSessions)
	}
	if cmd == nil {
		t.Fatal("sessions selection should load sessions")
	}

	m = New(fx.store, "")
	m.Cursor = 3
	updatedModel, cmd = m.handleDashboardSelection()
	updated = updatedModel.(Model)
	if updated.Screen != ScreenTrash {
		t.Fatalf("screen = %v, want %v", updated.Screen, ScreenTrash)
	}
	if cmd == nil {
		t.Fatal("recycle bin selection should load trash observations")
	}

	m = New(fx.store, "")
	m.Cursor = 4
	updatedModel, cmd = m.handleDashboardSelection()
	updated = updatedModel.(Model)
	if updated.Screen != ScreenSetup || len(updated.SetupAgents) == 0 {
		t.Fatal("setup selection should initialize setup screen")
	}
	if cmd != nil {
		t.Fatal("setup selection should not return command")
	}

	m = New(nil, "")
	m.Screen = ScreenSearch
	m.PrevScreen = ScreenDashboard
	updatedModel, _ = m.handleSearchKeys("esc")
	updated = updatedModel.(Model)
	if updated.Screen != ScreenDashboard {
		t.Fatalf("screen = %v, want %v", updated.Screen, ScreenDashboard)
	}

	m = New(nil, "")
	m.Screen = ScreenSearch
	updatedModel, _ = m.handleSearchKeys("/")
	updated = updatedModel.(Model)
	if !updated.SearchInput.Focused() {
		t.Fatal("search key should focus search input")
	}
}

func TestHandleRecentTimelineSessionsAndDetailKeyPaths(t *testing.T) {
	fx := newTestFixture(t)
	m := New(fx.store, "")
	m.Height = 14
	m.Screen = ScreenRecent
	m.RecentObservations = []store.Observation{{ID: fx.obsID}, {ID: fx.secondObs}, {ID: 33}, {ID: 44}}
	m.Cursor = 2

	updatedModel, _ := m.handleRecentKeys("down")
	updated := updatedModel.(Model)
	if updated.Cursor != 3 || updated.Scroll != 1 {
		t.Fatalf("cursor/scroll = %d/%d, want 3/1", updated.Cursor, updated.Scroll)
	}

	updatedModel, cmd := updated.handleRecentKeys("enter")
	updated = updatedModel.(Model)
	if updated.PrevScreen != ScreenRecent || cmd == nil {
		t.Fatal("recent enter should request observation detail")
	}

	updatedModel, cmd = updated.handleRecentKeys("t")
	updated = updatedModel.(Model)
	if updated.PrevScreen != ScreenRecent || cmd == nil {
		t.Fatal("recent t should request timeline")
	}

	updatedModel, cmd = updated.handleRecentKeys("esc")
	updated = updatedModel.(Model)
	if updated.Screen != ScreenDashboard || cmd == nil {
		t.Fatal("recent esc should return dashboard and refresh stats")
	}

	m = New(fx.store, "")
	m.Screen = ScreenTimeline
	m.PrevScreen = ScreenDashboard
	m.Scroll = 2
	updatedModel, _ = m.handleTimelineKeys("up")
	updated = updatedModel.(Model)
	if updated.Scroll != 1 {
		t.Fatalf("scroll = %d, want 1", updated.Scroll)
	}
	updatedModel, cmd = updated.handleTimelineKeys("esc")
	updated = updatedModel.(Model)
	if updated.Screen != ScreenDashboard || cmd == nil {
		t.Fatal("timeline esc should return previous screen and refresh")
	}

	m = New(fx.store, "")
	m.Height = 12
	m.Screen = ScreenSessions
	m.Sessions = []store.SessionSummary{{ID: fx.sessionID}, {ID: fx.otherSession}, {ID: "s3"}, {ID: "s4"}, {ID: "s5"}, {ID: "s6"}}
	m.Cursor = 4
	updatedModel, _ = m.handleSessionsKeys("down")
	updated = updatedModel.(Model)
	if updated.Cursor != 5 || updated.Scroll != 1 {
		t.Fatalf("cursor/scroll = %d/%d, want 5/1", updated.Cursor, updated.Scroll)
	}
	updated.Cursor = 0
	updatedModel, cmd = updated.handleSessionsKeys("enter")
	updated = updatedModel.(Model)
	if updated.SelectedSessionIdx != 0 || updated.PrevScreen != ScreenSessions || cmd == nil {
		t.Fatal("sessions enter should load selected session observations")
	}

	m = New(fx.store, "")
	m.Height = 18
	m.Screen = ScreenSessionDetail
	m.SelectedSessionIdx = 0
	m.SessionObservations = []store.Observation{{ID: fx.obsID}, {ID: fx.secondObs}, {ID: 3}, {ID: 4}}
	m.Cursor = 2
	updatedModel, _ = m.handleSessionDetailKeys("down")
	updated = updatedModel.(Model)
	if updated.Cursor != 3 || updated.SessionDetailScroll != 1 {
		t.Fatalf("cursor/detailScroll = %d/%d, want 3/1", updated.Cursor, updated.SessionDetailScroll)
	}
	updatedModel, cmd = updated.handleSessionDetailKeys("enter")
	updated = updatedModel.(Model)
	if updated.PrevScreen != ScreenSessionDetail || cmd == nil {
		t.Fatal("session detail enter should load observation detail")
	}
	updatedModel, cmd = updated.handleSessionDetailKeys("t")
	updated = updatedModel.(Model)
	if updated.PrevScreen != ScreenSessionDetail || cmd == nil {
		t.Fatal("session detail t should load timeline")
	}
	updatedModel, cmd = updated.handleSessionDetailKeys("esc")
	updated = updatedModel.(Model)
	if updated.Screen != ScreenSessions || cmd == nil {
		t.Fatal("session detail esc should return to sessions and refresh list")
	}
}

func TestTUIDeleteRestoreAndPurgeKeysForObservationLists(t *testing.T) {
	deletedAt := "2026-06-08 10:00:00"

	t.Run("active observations delete without confirmation", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			key    string
			model  func(testFixture) Model
			handle func(Model, string) (tea.Model, tea.Cmd)
		}{
			{
				name: "search results d",
				key:  "d",
				model: func(fx testFixture) Model {
					m := New(fx.store, "")
					m.Screen = ScreenSearchResults
					m.SearchResults = []store.SearchResult{{Observation: store.Observation{ID: fx.obsID}}}
					return m
				},
				handle: func(m Model, key string) (tea.Model, tea.Cmd) {
					return m.handleSearchResultsKeys(key)
				},
			},
			{
				name: "recent delete",
				key:  "delete",
				model: func(fx testFixture) Model {
					m := New(fx.store, "")
					m.Screen = ScreenRecent
					m.RecentObservations = []store.Observation{{ID: fx.obsID}}
					return m
				},
				handle: func(m Model, key string) (tea.Model, tea.Cmd) {
					return m.handleRecentKeys(key)
				},
			},
			{
				name: "session detail d",
				key:  "d",
				model: func(fx testFixture) Model {
					m := New(fx.store, "")
					m.Screen = ScreenSessionDetail
					m.SessionObservations = []store.Observation{{ID: fx.obsID}}
					return m
				},
				handle: func(m Model, key string) (tea.Model, tea.Cmd) {
					return m.handleSessionDetailKeys(key)
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fx := newTestFixture(t)
				m := tc.model(fx)

				updatedModel, cmd := tc.handle(m, tc.key)
				updated := updatedModel.(Model)
				if cmd == nil {
					t.Fatal("active delete should return delete command")
				}
				if updated.Screen == ScreenConfirmDelete || updated.Confirm.Action != confirmNone {
					t.Fatalf("active delete opened confirmation: screen=%v confirm=%+v", updated.Screen, updated.Confirm)
				}
				msg := cmd()
				if _, ok := msg.(observationDeletedMsg); !ok {
					t.Fatalf("delete command message type = %T", msg)
				}
			})
		}
	})

	t.Run("deleted observations delete opens purge confirmation", func(t *testing.T) {
		for _, tc := range []struct {
			name         string
			key          string
			returnScreen Screen
			model        func(testFixture) Model
			handle       func(Model, string) (tea.Model, tea.Cmd)
		}{
			{
				name:         "search results delete",
				key:          "delete",
				returnScreen: ScreenSearchResults,
				model: func(fx testFixture) Model {
					m := New(fx.store, "")
					m.Screen = ScreenSearchResults
					m.SearchResults = []store.SearchResult{{Observation: store.Observation{ID: fx.secondObs, DeletedAt: &deletedAt}}}
					return m
				},
				handle: func(m Model, key string) (tea.Model, tea.Cmd) {
					return m.handleSearchResultsKeys(key)
				},
			},
			{
				name:         "recent d",
				key:          "d",
				returnScreen: ScreenRecent,
				model: func(fx testFixture) Model {
					m := New(fx.store, "")
					m.Screen = ScreenRecent
					m.RecentObservations = []store.Observation{{ID: fx.secondObs, DeletedAt: &deletedAt}}
					return m
				},
				handle: func(m Model, key string) (tea.Model, tea.Cmd) {
					return m.handleRecentKeys(key)
				},
			},
			{
				name:         "session detail delete",
				key:          "delete",
				returnScreen: ScreenSessionDetail,
				model: func(fx testFixture) Model {
					m := New(fx.store, "")
					m.Screen = ScreenSessionDetail
					m.SessionObservations = []store.Observation{{ID: fx.secondObs, DeletedAt: &deletedAt}}
					return m
				},
				handle: func(m Model, key string) (tea.Model, tea.Cmd) {
					return m.handleSessionDetailKeys(key)
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fx := newTestFixture(t)
				m := tc.model(fx)

				updatedModel, cmd := tc.handle(m, tc.key)
				updated := updatedModel.(Model)
				if cmd != nil {
					t.Fatal("deleted observation delete should not return command before confirmation")
				}
				if updated.Screen != ScreenConfirmDelete {
					t.Fatalf("screen = %v, want %v", updated.Screen, ScreenConfirmDelete)
				}
				if updated.Confirm.Action != confirmPurgeObservation || updated.Confirm.ObservationID != fx.secondObs || updated.Confirm.ReturnScreen != tc.returnScreen {
					t.Fatalf("confirm = %+v", updated.Confirm)
				}
			})
		}
	})

	t.Run("restore only runs for deleted observations", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			model  func(testFixture, *string) Model
			handle func(Model, string) (tea.Model, tea.Cmd)
		}{
			{
				name: "search results",
				model: func(fx testFixture, deletedAt *string) Model {
					m := New(fx.store, "")
					m.Screen = ScreenSearchResults
					m.SearchResults = []store.SearchResult{{Observation: store.Observation{ID: fx.secondObs, DeletedAt: deletedAt}}}
					return m
				},
				handle: func(m Model, key string) (tea.Model, tea.Cmd) {
					return m.handleSearchResultsKeys(key)
				},
			},
			{
				name: "recent",
				model: func(fx testFixture, deletedAt *string) Model {
					m := New(fx.store, "")
					m.Screen = ScreenRecent
					m.RecentObservations = []store.Observation{{ID: fx.secondObs, DeletedAt: deletedAt}}
					return m
				},
				handle: func(m Model, key string) (tea.Model, tea.Cmd) {
					return m.handleRecentKeys(key)
				},
			},
			{
				name: "session detail",
				model: func(fx testFixture, deletedAt *string) Model {
					m := New(fx.store, "")
					m.Screen = ScreenSessionDetail
					m.SessionObservations = []store.Observation{{ID: fx.secondObs, DeletedAt: deletedAt}}
					return m
				},
				handle: func(m Model, key string) (tea.Model, tea.Cmd) {
					return m.handleSessionDetailKeys(key)
				},
			},
		} {
			t.Run(tc.name+" deleted", func(t *testing.T) {
				fx := newTestFixture(t)
				if err := fx.store.DeleteObservation(fx.secondObs, false); err != nil {
					t.Fatalf("DeleteObservation: %v", err)
				}
				m := tc.model(fx, &deletedAt)

				_, cmd := tc.handle(m, "r")
				if cmd == nil {
					t.Fatal("restore should return command for deleted observation")
				}
				msg := cmd()
				if _, ok := msg.(observationRestoredMsg); !ok {
					t.Fatalf("restore command message type = %T", msg)
				}
			})

			t.Run(tc.name+" active", func(t *testing.T) {
				fx := newTestFixture(t)
				m := tc.model(fx, nil)

				updatedModel, cmd := tc.handle(m, "r")
				updated := updatedModel.(Model)
				if cmd != nil {
					t.Fatal("restore should no-op for active observation")
				}
				if updated.Screen == ScreenConfirmDelete || updated.Confirm.Action != confirmNone {
					t.Fatalf("active restore changed confirmation state: screen=%v confirm=%+v", updated.Screen, updated.Confirm)
				}
			})
		}
	})
}

func TestTUITrashKeysRestoreAndPurge(t *testing.T) {
	deletedAt := "2026-06-08 10:00:00"
	fx := newTestFixture(t)
	if err := fx.store.DeleteObservation(fx.obsID, false); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}

	m := New(fx.store, "")
	m.Screen = ScreenTrash
	m.Height = 14
	m.TrashObservations = []store.Observation{
		{ID: fx.obsID, DeletedAt: &deletedAt},
		{ID: fx.secondObs, DeletedAt: &deletedAt},
		{ID: 33, DeletedAt: &deletedAt},
		{ID: 44, DeletedAt: &deletedAt},
	}
	m.Cursor = 2

	updatedModel, _ := m.handleTrashKeys("down")
	updated := updatedModel.(Model)
	if updated.Cursor != 3 || updated.Scroll != 1 {
		t.Fatalf("trash down cursor/scroll = %d/%d, want 3/1", updated.Cursor, updated.Scroll)
	}
	updatedModel, _ = updated.handleTrashKeys("up")
	updated = updatedModel.(Model)
	if updated.Cursor != 2 || updated.Scroll != 1 {
		t.Fatalf("trash up cursor/scroll = %d/%d, want 2/1", updated.Cursor, updated.Scroll)
	}

	m.Cursor = 0
	updatedModel, cmd := m.handleTrashKeys("r")
	updated = updatedModel.(Model)
	if cmd == nil {
		t.Fatal("trash r should return restore command")
	}
	if updated.Screen != ScreenTrash {
		t.Fatalf("trash r screen = %v, want %v", updated.Screen, ScreenTrash)
	}
	if _, ok := cmd().(observationRestoredMsg); !ok {
		t.Fatalf("trash restore command message type unexpected")
	}

	updatedModel, cmd = m.handleTrashKeys("delete")
	updated = updatedModel.(Model)
	if cmd != nil {
		t.Fatal("trash delete should wait for confirmation")
	}
	if updated.Screen != ScreenConfirmDelete || updated.Confirm.Action != confirmPurgeObservation || updated.Confirm.ReturnScreen != ScreenTrash {
		t.Fatalf("trash delete confirmation = screen %v confirm %+v", updated.Screen, updated.Confirm)
	}

	updatedModel, cmd = m.handleTrashKeys("enter")
	updated = updatedModel.(Model)
	if updated.PrevScreen != ScreenTrash || cmd == nil {
		t.Fatal("trash enter should request detail with previous screen set to trash")
	}

	updatedModel, cmd = m.handleTrashKeys("esc")
	updated = updatedModel.(Model)
	if updated.Screen != ScreenDashboard || updated.Cursor != 0 || updated.Scroll != 0 || cmd == nil {
		t.Fatal("trash esc should return dashboard and refresh stats")
	}
	m.Screen = ScreenTrash
	updatedModel, cmd = m.handleTrashKeys("q")
	updated = updatedModel.(Model)
	if updated.Screen != ScreenDashboard || cmd == nil {
		t.Fatal("trash q should return dashboard and refresh stats")
	}
}

func TestTUIConfirmDeleteKeys(t *testing.T) {
	for _, key := range []string{"y", "Y", "enter"} {
		t.Run("purge "+key, func(t *testing.T) {
			fx := newTestFixture(t)
			if err := fx.store.DeleteObservation(fx.obsID, false); err != nil {
				t.Fatalf("DeleteObservation: %v", err)
			}
			m := New(fx.store, "")
			m.Screen = ScreenConfirmDelete
			m.Confirm = confirmState{
				Action:        confirmPurgeObservation,
				ObservationID: fx.obsID,
				ReturnScreen:  ScreenTrash,
			}

			_, cmd := m.handleConfirmDeleteKeys(key)
			if cmd == nil {
				t.Fatalf("%s should return purge command", key)
			}
			if _, ok := cmd().(observationPurgedMsg); !ok {
				t.Fatalf("%s command should purge observation", key)
			}
		})

		t.Run("delete session "+key, func(t *testing.T) {
			fx := newTestFixture(t)
			m := New(fx.store, "")
			m.Screen = ScreenConfirmDelete
			m.Confirm = confirmState{
				Action:       confirmDeleteSession,
				SessionID:    fx.otherSession,
				ReturnScreen: ScreenSessions,
			}

			_, cmd := m.handleConfirmDeleteKeys(key)
			if cmd == nil {
				t.Fatalf("%s should return session cascade command", key)
			}
			if _, ok := cmd().(sessionDeletedMsg); !ok {
				t.Fatalf("%s command should delete session cascade", key)
			}
		})
	}

	for _, key := range []string{"n", "N", "esc", "q"} {
		t.Run("cancel "+key, func(t *testing.T) {
			m := New(nil, "")
			m.Screen = ScreenConfirmDelete
			m.Confirm = confirmState{
				Action:        confirmPurgeObservation,
				ObservationID: 12,
				ReturnScreen:  ScreenTrash,
			}

			updatedModel, cmd := m.handleConfirmDeleteKeys(key)
			updated := updatedModel.(Model)
			if cmd != nil {
				t.Fatalf("%s cancel should not return command", key)
			}
			if updated.Screen != ScreenTrash {
				t.Fatalf("%s cancel screen = %v, want %v", key, updated.Screen, ScreenTrash)
			}
			if updated.Confirm != (confirmState{}) {
				t.Fatalf("%s cancel should clear confirm state: %+v", key, updated.Confirm)
			}
		})
	}

	t.Run("operation result returns from confirmation before refresh", func(t *testing.T) {
		fx := newTestFixture(t)
		m := New(fx.store, "")
		m.Screen = ScreenConfirmDelete
		m.Confirm = confirmState{Action: confirmPurgeObservation, ReturnScreen: ScreenTrash, ObservationID: fx.obsID}

		updatedModel, cmd := m.Update(observationPurgedMsg{id: fx.obsID})
		updated := updatedModel.(Model)
		if updated.Screen != ScreenTrash || updated.Confirm != (confirmState{}) || cmd == nil {
			t.Fatalf("purge result should return to trash, clear confirm, and refresh: screen=%v confirm=%+v cmd=%v", updated.Screen, updated.Confirm, cmd)
		}

		m.Screen = ScreenConfirmDelete
		m.Confirm = confirmState{Action: confirmDeleteSession, ReturnScreen: ScreenSessions, SessionID: fx.sessionID}
		updatedModel, cmd = m.Update(sessionDeletedMsg{sessionID: fx.sessionID})
		updated = updatedModel.(Model)
		if updated.Screen != ScreenSessions || updated.Confirm != (confirmState{}) || cmd == nil {
			t.Fatalf("session delete result should return to sessions, clear confirm, and refresh: screen=%v confirm=%+v cmd=%v", updated.Screen, updated.Confirm, cmd)
		}
	})
}

func TestTUIDeleteNonEmptySessionRequiresConfirmation(t *testing.T) {
	fx := newTestFixture(t)
	m := New(fx.store, "")
	m.Screen = ScreenSessions
	m.Sessions = []store.SessionSummary{
		{ID: fx.sessionID, ObservationCount: 2},
		{ID: fx.otherSession, ObservationCount: 0},
	}

	updatedModel, cmd := m.handleSessionsKeys("d")
	updated := updatedModel.(Model)
	if cmd != nil {
		t.Fatal("session delete should not run command before confirmation")
	}
	if updated.Screen != ScreenConfirmDelete || updated.Confirm.Action != confirmDeleteSession || updated.Confirm.SessionID != fx.sessionID {
		t.Fatalf("non-empty session confirmation = screen %v confirm %+v", updated.Screen, updated.Confirm)
	}
	if updated.Confirm.ReturnScreen != ScreenSessions || updated.Confirm.ObservationCount != 2 {
		t.Fatalf("non-empty session return/count = %v/%d", updated.Confirm.ReturnScreen, updated.Confirm.ObservationCount)
	}
	if !strings.Contains(updated.Confirm.Body, "permanently delete all 2 memories") || !strings.Contains(updated.Confirm.Body, "cannot be recovered") {
		t.Fatalf("warning body = %q", updated.Confirm.Body)
	}
	if _, err := fx.store.GetSession(fx.sessionID); err != nil {
		t.Fatalf("session was deleted before confirmation: %v", err)
	}

	m.Cursor = 1
	updatedModel, cmd = m.handleSessionsKeys("delete")
	updated = updatedModel.(Model)
	if cmd != nil {
		t.Fatal("empty session delete should also wait for confirmation")
	}
	if updated.Screen != ScreenConfirmDelete || updated.Confirm.SessionID != fx.otherSession || updated.Confirm.ObservationCount != 0 {
		t.Fatalf("empty session confirmation = screen %v confirm %+v", updated.Screen, updated.Confirm)
	}

	_, cmd = updated.handleConfirmDeleteKeys("enter")
	if cmd == nil {
		t.Fatal("empty session confirmation should return cascade command")
	}
	if _, ok := cmd().(sessionDeletedMsg); !ok {
		t.Fatalf("empty session confirmation command should cascade delete")
	}
}

func TestRefreshScreen(t *testing.T) {
	m := New(newTestFixture(t).store, "")

	if cmd := m.refreshScreen(ScreenDashboard); cmd == nil {
		t.Fatal("dashboard refresh should return stats command")
	}
	if cmd := m.refreshScreen(ScreenRecent); cmd == nil {
		t.Fatal("recent refresh should return observations command")
	}
	if cmd := m.refreshScreen(ScreenSessions); cmd == nil {
		t.Fatal("sessions refresh should return sessions command")
	}
	if cmd := m.refreshScreen(ScreenTrash); cmd == nil {
		t.Fatal("trash refresh should return trash observations command")
	}
	if cmd := m.refreshScreen(ScreenSearch); cmd != nil {
		t.Fatal("search refresh should not return command")
	}
}

func TestUpdateDataMessageBranches(t *testing.T) {
	m := New(nil, "")

	updatedModel, _ := m.Update(updateCheckMsg{result: version.CheckResult{Status: version.StatusCheckFailed, Message: "Could not check for updates: GitHub took too long to respond."}})
	updated := updatedModel.(Model)
	if updated.UpdateStatus != version.StatusCheckFailed || updated.UpdateMsg != "Could not check for updates: GitHub took too long to respond." {
		t.Fatal("update check failure should persist status and message")
	}

	updatedModel, _ = m.Update(statsLoadedMsg{err: errors.New("stats err")})
	updated = updatedModel.(Model)
	if updated.ErrorMsg != "stats err" {
		t.Fatalf("error = %q", updated.ErrorMsg)
	}

	stats := &store.Stats{TotalSessions: 2}
	updatedModel, _ = m.Update(statsLoadedMsg{stats: stats})
	updated = updatedModel.(Model)
	if updated.Stats == nil || updated.Stats.TotalSessions != 2 {
		t.Fatal("stats should be set from successful message")
	}

	updatedModel, _ = m.Update(searchResultsMsg{err: errors.New("search err")})
	updated = updatedModel.(Model)
	if updated.ErrorMsg != "search err" {
		t.Fatalf("error = %q", updated.ErrorMsg)
	}

	results := []store.SearchResult{{Observation: store.Observation{ID: 9}}}
	updatedModel, _ = m.Update(searchResultsMsg{results: results, query: "needle"})
	updated = updatedModel.(Model)
	if updated.Screen != ScreenSearchResults || updated.Cursor != 0 || updated.Scroll != 0 {
		t.Fatal("search results message should switch to results screen and reset cursor/scroll")
	}

	updatedModel, _ = m.Update(recentObservationsMsg{err: errors.New("recent err")})
	updated = updatedModel.(Model)
	if updated.ErrorMsg != "recent err" {
		t.Fatalf("error = %q", updated.ErrorMsg)
	}

	obsList := []store.Observation{{ID: 1}}
	updatedModel, _ = m.Update(recentObservationsMsg{observations: obsList})
	updated = updatedModel.(Model)
	if len(updated.RecentObservations) != 1 {
		t.Fatal("recent observations should be updated")
	}

	updatedModel, _ = m.Update(observationDetailMsg{err: errors.New("detail err")})
	updated = updatedModel.(Model)
	if updated.ErrorMsg != "detail err" {
		t.Fatalf("error = %q", updated.ErrorMsg)
	}

	selected := &store.Observation{ID: 99}
	updatedModel, _ = m.Update(observationDetailMsg{observation: selected})
	updated = updatedModel.(Model)
	if updated.Screen != ScreenObservationDetail || updated.DetailScroll != 0 {
		t.Fatal("observation detail message should open detail screen and reset scroll")
	}

	updatedModel, _ = m.Update(timelineMsg{err: errors.New("timeline err")})
	updated = updatedModel.(Model)
	if updated.ErrorMsg != "timeline err" {
		t.Fatalf("error = %q", updated.ErrorMsg)
	}

	tl := &store.TimelineResult{Focus: store.Observation{ID: 99}}
	updatedModel, _ = m.Update(timelineMsg{timeline: tl})
	updated = updatedModel.(Model)
	if updated.Screen != ScreenTimeline || updated.Scroll != 0 {
		t.Fatal("timeline message should open timeline and reset scroll")
	}

	updatedModel, _ = m.Update(recentSessionsMsg{err: errors.New("sessions err")})
	updated = updatedModel.(Model)
	if updated.ErrorMsg != "sessions err" {
		t.Fatalf("error = %q", updated.ErrorMsg)
	}

	sessions := []store.SessionSummary{{ID: "s1"}}
	updatedModel, _ = m.Update(recentSessionsMsg{sessions: sessions})
	updated = updatedModel.(Model)
	if len(updated.Sessions) != 1 {
		t.Fatal("sessions should be updated")
	}

	updatedModel, _ = m.Update(sessionObservationsMsg{err: errors.New("session detail err")})
	updated = updatedModel.(Model)
	if updated.ErrorMsg != "session detail err" {
		t.Fatalf("error = %q", updated.ErrorMsg)
	}

	sessionObs := []store.Observation{{ID: 1}, {ID: 2}}
	updatedModel, _ = m.Update(sessionObservationsMsg{observations: sessionObs})
	updated = updatedModel.(Model)
	if updated.Screen != ScreenSessionDetail || updated.Cursor != 0 || updated.SessionDetailScroll != 0 {
		t.Fatal("session observations message should open session detail and reset cursor/scroll")
	}

	m = New(nil, "")
	m.Screen = ScreenDashboard
	m.Cursor = 4
	m.Scroll = 3
	updatedModel, _ = m.Update(trashObservationsMsg{observations: obsList})
	updated = updatedModel.(Model)
	if updated.Screen != ScreenDashboard {
		t.Fatalf("delayed trash message should not change screen, got %v", updated.Screen)
	}
	if updated.Cursor != 4 || updated.Scroll != 3 {
		t.Fatalf("delayed trash message changed cursor/scroll to %d/%d", updated.Cursor, updated.Scroll)
	}
	if len(updated.TrashObservations) != 1 {
		t.Fatal("trash observations should still update when message arrives off-screen")
	}

	m.Screen = ScreenTrash
	m.Cursor = 5
	m.Scroll = 2
	updatedModel, _ = m.Update(trashObservationsMsg{observations: obsList})
	updated = updatedModel.(Model)
	if updated.Screen != ScreenTrash || updated.Cursor != 0 || updated.Scroll != 0 {
		t.Fatal("trash message on trash screen should keep trash screen and reset cursor/scroll")
	}

	m = New(newTestFixture(t).store, "")
	m.Screen = ScreenRecent
	m.ErrorMsg = "old error"
	updatedModel, cmd := m.Update(observationDeletedMsg{id: 1})
	updated = updatedModel.(Model)
	if updated.ErrorMsg != "" || cmd == nil {
		t.Fatal("successful observation delete message should clear error and refresh current screen")
	}

	m.Screen = ScreenTrash
	m.ErrorMsg = "old error"
	updatedModel, cmd = m.Update(observationRestoredMsg{id: 1})
	updated = updatedModel.(Model)
	if updated.ErrorMsg != "" || cmd == nil {
		t.Fatal("successful observation restore message should clear error and refresh current screen")
	}

	m.Screen = ScreenTrash
	m.ErrorMsg = "old error"
	updatedModel, cmd = m.Update(observationPurgedMsg{id: 1})
	updated = updatedModel.(Model)
	if updated.ErrorMsg != "" || cmd == nil {
		t.Fatal("successful observation purge message should clear error and refresh current screen")
	}

	m.Screen = ScreenSessions
	m.ErrorMsg = "old error"
	updatedModel, cmd = m.Update(sessionDeletedMsg{sessionID: "s1"})
	updated = updatedModel.(Model)
	if updated.ErrorMsg != "" || cmd == nil {
		t.Fatal("successful session delete message should clear error and refresh current screen")
	}

	updated.SetupInstalling = true
	updatedModel, _ = updated.Update(setupInstallMsg{err: errors.New("setup err")})
	updated = updatedModel.(Model)
	if updated.SetupInstalling || !updated.SetupDone || updated.SetupError != "setup err" {
		t.Fatal("setup error should end install and surface setup error")
	}

	updated.SetupInstalling = true
	setupRes := &setup.Result{Agent: "opencode", Destination: "/tmp", Files: 2}
	updatedModel, _ = updated.Update(setupInstallMsg{result: setupRes})
	updated = updatedModel.(Model)
	if updated.SetupInstalling || !updated.SetupDone || updated.SetupResult == nil || updated.SetupError != "" {
		t.Fatal("setup success should persist result and clear errors")
	}

	unchangedModel, cmd := updated.Update(struct{ X int }{X: 1})
	if cmd != nil {
		t.Fatal("unknown message should not return command")
	}
	if unchangedModel.(Model).Screen != updated.Screen {
		t.Fatal("unknown message should keep state unchanged")
	}
}

func TestHandleKeyPressRouterAndClearsError(t *testing.T) {
	m := New(nil, "")
	m.ErrorMsg = "old error"

	for _, screen := range []Screen{
		ScreenDashboard,
		ScreenSearch,
		ScreenSearchResults,
		ScreenRecent,
		ScreenObservationDetail,
		ScreenTimeline,
		ScreenSessions,
		ScreenSessionDetail,
		ScreenTrash,
		ScreenSetup,
	} {
		m.Screen = screen
		m.ErrorMsg = "old error"
		updatedModel, _ := m.handleKeyPress("x")
		updated := updatedModel.(Model)
		if updated.ErrorMsg != "" {
			t.Fatalf("screen %v should clear error on key press", screen)
		}
	}
}

func TestHandleDashboardKeysAndSelectionRemainingBranches(t *testing.T) {
	m := New(nil, "")

	m.Cursor = 0
	updatedModel, _ := m.handleDashboardKeys("up")
	if updatedModel.(Model).Cursor != 0 {
		t.Fatal("cursor should stay at top boundary")
	}

	m.Cursor = len(dashboardMenuItems) - 1
	updatedModel, _ = m.handleDashboardKeys("down")
	if updatedModel.(Model).Cursor != len(dashboardMenuItems)-1 {
		t.Fatal("cursor should stay at bottom boundary")
	}

	m.Cursor = 5
	_, cmd := m.handleDashboardKeys(" ")
	if cmd == nil {
		t.Fatal("space on quit item should return quit command")
	}

	_, cmd = m.handleDashboardKeys("q")
	if cmd == nil {
		t.Fatal("q should return quit command")
	}

	m.Cursor = 0
	updatedModel, _ = m.handleDashboardSelection()
	updated := updatedModel.(Model)
	if updated.Screen != ScreenSearch || !updated.SearchInput.Focused() {
		t.Fatal("cursor 0 selection should open search")
	}

	m.Cursor = 5
	_, cmd = m.handleDashboardSelection()
	if cmd == nil {
		t.Fatal("cursor 5 selection should quit")
	}

	m.Cursor = 99
	updatedModel, cmd = m.handleDashboardSelection()
	if updatedModel.(Model).Cursor != 99 || cmd != nil {
		t.Fatal("out-of-range dashboard selection should be no-op")
	}
}

func TestHandleSearchResultsAndObservationDetailRemainingBranches(t *testing.T) {
	m := New(nil, "")
	m.Height = 16
	m.Screen = ScreenSearchResults

	updatedModel, _ := m.handleSearchResultsKeys("enter")
	updated := updatedModel.(Model)
	if updated.Screen != ScreenSearchResults {
		t.Fatal("enter with no results should keep current screen")
	}

	updatedModel, _ = m.handleSearchResultsKeys("t")
	updated = updatedModel.(Model)
	if updated.Screen != ScreenSearchResults {
		t.Fatal("timeline key with no results should keep current screen")
	}

	m.SearchResults = []store.SearchResult{{Observation: store.Observation{ID: 1}}}
	m.Cursor = 0
	updatedModel, cmd := m.handleSearchResultsKeys("enter")
	updated = updatedModel.(Model)
	if updated.PrevScreen != ScreenSearchResults || cmd == nil {
		t.Fatal("enter with selection should request detail command")
	}

	updatedModel, cmd = m.handleSearchResultsKeys("t")
	updated = updatedModel.(Model)
	if updated.PrevScreen != ScreenSearchResults || cmd == nil {
		t.Fatal("t with selection should request timeline command")
	}

	updatedModel, _ = m.handleSearchResultsKeys("/")
	updated = updatedModel.(Model)
	if updated.Screen != ScreenSearch || !updated.SearchInput.Focused() {
		t.Fatal("slash should switch to focused search input")
	}

	m.Screen = ScreenObservationDetail
	m.SelectedObservation = &store.Observation{ID: 5}
	updatedModel, _ = m.handleObservationDetailKeys("down")
	updated = updatedModel.(Model)
	if updated.DetailScroll != 1 {
		t.Fatal("down should increase detail scroll")
	}
	updatedModel, _ = updated.handleObservationDetailKeys("up")
	if updatedModel.(Model).DetailScroll != 0 {
		t.Fatal("up should decrease detail scroll")
	}
	_, cmd = updated.handleObservationDetailKeys("t")
	if cmd == nil {
		t.Fatal("timeline key with selected observation should return command")
	}

	m.SelectedObservation = nil
	_, cmd = m.handleObservationDetailKeys("t")
	if cmd != nil {
		t.Fatal("timeline key without selected observation should not return command")
	}
}

func TestSearchEscapeFlowNoLoop(t *testing.T) {
	// Verifies the full escape chain never loops back:
	// Dashboard → Search → Results → ObsDetail → Esc → Results → Esc → Search → Esc → Dashboard

	m := New(nil, "")
	m.Height = 20
	m.SearchResults = []store.SearchResult{{Observation: store.Observation{ID: 42}}}

	// Step 1: from SearchResults, enter ObservationDetail — PrevScreen = ScreenSearchResults
	m.Screen = ScreenSearchResults
	m.Cursor = 0
	updatedModel, _ := m.handleSearchResultsKeys("enter")
	m = updatedModel.(Model)
	if m.PrevScreen != ScreenSearchResults {
		t.Fatalf("after enter, PrevScreen should be ScreenSearchResults, got %v", m.PrevScreen)
	}

	// Step 2: from ObservationDetail, Esc → back to SearchResults (via PrevScreen)
	m.Screen = ScreenObservationDetail
	m.SelectedObservation = &store.Observation{ID: 42}
	updatedModel, _ = m.handleObservationDetailKeys("esc")
	m = updatedModel.(Model)
	if m.Screen != ScreenSearchResults {
		t.Fatalf("esc from ObservationDetail should go to ScreenSearchResults, got %v", m.Screen)
	}

	// Step 3: from SearchResults, Esc → back to ScreenSearch, PrevScreen reset to Dashboard
	m.Screen = ScreenSearchResults
	updatedModel, _ = m.handleSearchResultsKeys("esc")
	m = updatedModel.(Model)
	if m.Screen != ScreenSearch {
		t.Fatalf("esc from SearchResults should go to ScreenSearch, got %v", m.Screen)
	}
	if m.PrevScreen != ScreenDashboard {
		t.Fatalf("esc from SearchResults should reset PrevScreen to ScreenDashboard, got %v", m.PrevScreen)
	}

	// Step 4: from Search (no input focused), Esc → always Dashboard, never loops
	m.Screen = ScreenSearch
	updatedModel, _ = m.handleSearchKeys("esc")
	m = updatedModel.(Model)
	if m.Screen != ScreenDashboard {
		t.Fatalf("esc from Search should always go to ScreenDashboard, got %v", m.Screen)
	}

	// Step 5: from Search input focused, Esc → always Dashboard, never loops
	m.Screen = ScreenSearch
	m.PrevScreen = ScreenSearchResults // simulate stale PrevScreen — must NOT be used
	m.SearchInput.Focus()
	updatedModel, _ = m.handleSearchInputKeys(tea.KeyMsg{Type: tea.KeyEscape})
	m = updatedModel.(Model)
	if m.Screen != ScreenDashboard {
		t.Fatalf("esc from SearchInput should always go to ScreenDashboard regardless of PrevScreen, got %v", m.Screen)
	}
}

func TestSearchInputClearedOnEnterFromDashboard(t *testing.T) {
	// Verifies the search input is cleared each time search is opened from dashboard
	m := New(nil, "")
	m.Screen = ScreenDashboard
	m.SearchInput.SetValue("old query")

	// Open via keyboard shortcut "s"
	updatedModel, _ := m.handleDashboardKeys("s")
	m = updatedModel.(Model)
	if m.SearchInput.Value() != "" {
		t.Fatalf("search input should be cleared when opening search, got %q", m.SearchInput.Value())
	}

	// Open via dashboard selection (menu item 0)
	m.Screen = ScreenDashboard
	m.SearchInput.SetValue("another stale query")
	m.Cursor = 0
	updatedModel, _ = m.handleDashboardSelection()
	m = updatedModel.(Model)
	if m.SearchInput.Value() != "" {
		t.Fatalf("search input should be cleared on dashboard selection, got %q", m.SearchInput.Value())
	}
}

func TestHandleSessionsAndSetupRemainingBranches(t *testing.T) {
	fx := newTestFixture(t)
	m := New(fx.store, "")
	m.Height = 12
	m.Sessions = []store.SessionSummary{{ID: "s1"}, {ID: "s2"}, {ID: "s3"}, {ID: "s4"}, {ID: "s5"}, {ID: "s6"}}

	m.Cursor = 0
	updatedModel, _ := m.handleSessionsKeys("up")
	if updatedModel.(Model).Cursor != 0 {
		t.Fatal("sessions up at top should stay at zero")
	}

	m.Cursor = len(m.Sessions) - 1
	updatedModel, _ = m.handleSessionsKeys("down")
	if updatedModel.(Model).Cursor != len(m.Sessions)-1 {
		t.Fatal("sessions down at bottom should stay at last item")
	}

	m.Sessions = nil
	_, cmd := m.handleSessionsKeys("enter")
	if cmd != nil {
		t.Fatal("sessions enter with no sessions should not return command")
	}

	m = New(fx.store, "")
	m.Screen = ScreenSetup
	m.SetupInstalling = true
	updatedModel, cmd = m.handleSetupKeys("esc")
	updated := updatedModel.(Model)
	if updated.Screen != ScreenSetup || cmd != nil {
		t.Fatal("setup should ignore keys while installing")
	}

	m.SetupInstalling = false
	m.SetupDone = true
	updatedModel, cmd = m.handleSetupKeys("x")
	updated = updatedModel.(Model)
	if updated.Screen != ScreenSetup || cmd != nil {
		t.Fatal("setup done with non-exit key should do nothing")
	}

	m.SetupDone = false
	m.SetupAgents = []setup.Agent{{Name: "opencode"}, {Name: "claude-code"}}
	m.Cursor = 0
	updatedModel, _ = m.handleSetupKeys("up")
	if updatedModel.(Model).Cursor != 0 {
		t.Fatal("setup up at top should stay at zero")
	}
	updatedModel, _ = m.handleSetupKeys("down")
	if updatedModel.(Model).Cursor != 1 {
		t.Fatal("setup down should move cursor")
	}
	updatedModel, _ = updatedModel.(Model).handleSetupKeys("up")
	if updatedModel.(Model).Cursor != 0 {
		t.Fatal("setup up with cursor>0 should decrement cursor")
	}

	original := installAgentFn
	t.Cleanup(func() { installAgentFn = original })
	installAgentFn = func(name string) (*setup.Result, error) {
		return &setup.Result{Agent: name, Destination: "/tmp", Files: 1}, nil
	}

	m.Cursor = 1
	updatedModel, cmd = m.handleSetupKeys("enter")
	updated = updatedModel.(Model)
	if !updated.SetupInstalling || updated.SetupInstallingName != "claude-code" || cmd == nil {
		t.Fatal("setup enter with valid agent should start install and return batch command")
	}

	m.SetupInstalling = false
	m.SetupAgents = nil
	_, cmd = m.handleSetupKeys("enter")
	if cmd != nil {
		t.Fatal("setup enter with no agents should not return command")
	}
}

func TestAdditionalKeyAliasAndBoundaryBranches(t *testing.T) {
	fx := newTestFixture(t)
	m := New(fx.store, "")

	// handleKeyPress default branch (unknown screen)
	m.Screen = Screen(999)
	updatedModel, cmd := m.handleKeyPress("x")
	if cmd != nil || updatedModel.(Model).Screen != Screen(999) {
		t.Fatal("unknown screen keypress should be a no-op")
	}

	// Search input remaining branches
	m = New(fx.store, "")
	m.Screen = ScreenSearch
	m.PrevScreen = ScreenDashboard
	m.SearchInput.Focus()
	updatedModel, cmd = m.handleSearchInputKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || updatedModel.(Model).Screen != ScreenSearch {
		t.Fatal("enter with empty search input should not run command")
	}
	updatedModel, cmd = m.handleSearchInputKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if cmd == nil || updatedModel.(Model).SearchInput.Value() == "" {
		t.Fatal("non-control key should update search input")
	}

	// Dashboard alias branch
	m = New(nil, "")
	updatedModel, _ = m.handleDashboardKeys("/")
	if updatedModel.(Model).Screen != ScreenSearch {
		t.Fatal("dashboard slash should open search")
	}

	// Search results aliases and boundaries
	m = New(fx.store, "")
	m.Height = 14
	m.SearchResults = []store.SearchResult{{Observation: store.Observation{ID: 1}}, {Observation: store.Observation{ID: 2}}, {Observation: store.Observation{ID: 3}}, {Observation: store.Observation{ID: 4}}}
	m.Cursor = 0
	updatedModel, _ = m.handleSearchResultsKeys("up")
	if updatedModel.(Model).Cursor != 0 {
		t.Fatal("search results up at top should stay at zero")
	}
	m.Cursor = len(m.SearchResults) - 1
	updatedModel, _ = m.handleSearchResultsKeys("down")
	if updatedModel.(Model).Cursor != len(m.SearchResults)-1 {
		t.Fatal("search results down at bottom should stay at last")
	}
	updatedModel, _ = m.handleSearchResultsKeys("s")
	if updatedModel.(Model).Screen != ScreenSearch {
		t.Fatal("search results s should jump to search")
	}

	// Recent aliases/boundaries
	m = New(fx.store, "")
	m.Height = 14
	m.RecentObservations = []store.Observation{{ID: 1}}
	m.Cursor = 0
	updatedModel, _ = m.handleRecentKeys("up")
	if updatedModel.(Model).Cursor != 0 {
		t.Fatal("recent up at top should stay at zero")
	}
	m.Cursor = 0
	updatedModel, _ = m.handleRecentKeys("t")
	if updatedModel.(Model).PrevScreen != ScreenRecent {
		t.Fatal("recent timeline key should set previous screen")
	}

	// Timeline down branch
	m = New(nil, "")
	updatedModel, _ = m.handleTimelineKeys("down")
	if updatedModel.(Model).Scroll != 1 {
		t.Fatal("timeline down should increment scroll")
	}

	// Sessions esc/q branches
	m = New(fx.store, "")
	m.Screen = ScreenSessions
	updatedModel, cmd = m.handleSessionsKeys("q")
	if updatedModel.(Model).Screen != ScreenDashboard || cmd == nil {
		t.Fatal("sessions q should return to dashboard and refresh")
	}

	// Session detail boundary and q branch
	m = New(fx.store, "")
	m.Height = 16
	m.Screen = ScreenSessionDetail
	m.SelectedSessionIdx = 0
	m.SessionObservations = []store.Observation{{ID: 1}}
	updatedModel, _ = m.handleSessionDetailKeys("up")
	if updatedModel.(Model).Cursor != 0 {
		t.Fatal("session detail up at top should stay at zero")
	}
	updatedModel, cmd = m.handleSessionDetailKeys("q")
	if updatedModel.(Model).Screen != ScreenSessions || cmd == nil {
		t.Fatal("session detail q should go back to sessions and refresh")
	}

	// Setup q/esc branch with no install state
	m = New(fx.store, "")
	m.Screen = ScreenSetup
	updatedModel, cmd = m.handleSetupKeys("q")
	if updatedModel.(Model).Screen != ScreenDashboard || cmd == nil {
		t.Fatal("setup q should return dashboard and refresh stats")
	}
}

func TestNavigationScrollAndSelectionBranches(t *testing.T) {
	fx := newTestFixture(t)

	// Dashboard increment/decrement and enter path
	m := New(fx.store, "")
	m.Cursor = 1
	updatedModel, _ := m.handleDashboardKeys("up")
	if updatedModel.(Model).Cursor != 0 {
		t.Fatal("dashboard up should decrement cursor when above zero")
	}
	updatedModel, _ = updatedModel.(Model).handleDashboardKeys("down")
	if updatedModel.(Model).Cursor != 1 {
		t.Fatal("dashboard down should increment cursor when not at bottom")
	}
	_, cmd := updatedModel.(Model).handleDashboardKeys("enter")
	if cmd == nil {
		t.Fatal("dashboard enter on recent item should return loader command")
	}

	// Search results scroll-up branch
	m = New(fx.store, "")
	m.Height = 14
	m.SearchResults = []store.SearchResult{{Observation: store.Observation{ID: 1}}, {Observation: store.Observation{ID: 2}}, {Observation: store.Observation{ID: 3}}}
	m.Cursor = 2
	m.Scroll = 2
	updatedModel, _ = m.handleSearchResultsKeys("up")
	updated := updatedModel.(Model)
	if updated.Cursor != 1 || updated.Scroll != 1 {
		t.Fatalf("search results up should update cursor/scroll, got %d/%d", updated.Cursor, updated.Scroll)
	}

	// Recent scroll-up branch
	m = New(fx.store, "")
	m.Height = 14
	m.RecentObservations = []store.Observation{{ID: 1}, {ID: 2}, {ID: 3}}
	m.Cursor = 2
	m.Scroll = 2
	updatedModel, _ = m.handleRecentKeys("up")
	updated = updatedModel.(Model)
	if updated.Cursor != 1 || updated.Scroll != 1 {
		t.Fatalf("recent up should update cursor/scroll, got %d/%d", updated.Cursor, updated.Scroll)
	}

	// Sessions scroll-up branch
	m = New(fx.store, "")
	m.Height = 12
	m.Sessions = []store.SessionSummary{{ID: "s1"}, {ID: "s2"}, {ID: "s3"}}
	m.Cursor = 2
	m.Scroll = 2
	updatedModel, _ = m.handleSessionsKeys("up")
	updated = updatedModel.(Model)
	if updated.Cursor != 1 || updated.Scroll != 1 {
		t.Fatalf("sessions up should update cursor/scroll, got %d/%d", updated.Cursor, updated.Scroll)
	}

	// Session detail scroll-up branch
	m = New(fx.store, "")
	m.Height = 16
	m.SessionObservations = []store.Observation{{ID: 1}, {ID: 2}, {ID: 3}}
	m.Cursor = 2
	m.SessionDetailScroll = 2
	updatedModel, _ = m.handleSessionDetailKeys("up")
	updated = updatedModel.(Model)
	if updated.Cursor != 1 || updated.SessionDetailScroll != 1 {
		t.Fatalf("session detail up should update cursor/detail scroll, got %d/%d", updated.Cursor, updated.SessionDetailScroll)
	}
}

func TestSetupAllowlistPromptFlow(t *testing.T) {
	t.Run("claude-code install shows allowlist prompt", func(t *testing.T) {
		fx := newTestFixture(t)
		m := New(fx.store, "")
		m.Screen = ScreenSetup
		m.SetupInstalling = true

		result := &setup.Result{Agent: "claude-code", Destination: "claude plugin system", Files: 0}
		updatedModel, _ := m.Update(setupInstallMsg{result: result})
		updated := updatedModel.(Model)

		if updated.SetupDone {
			t.Fatal("claude-code install should NOT set SetupDone yet")
		}
		if !updated.SetupAllowlistPrompt {
			t.Fatal("claude-code install should set SetupAllowlistPrompt")
		}
		if updated.SetupResult == nil || updated.SetupResult.Agent != "claude-code" {
			t.Fatal("result should be set")
		}
	})

	t.Run("non-claude-code install skips allowlist prompt", func(t *testing.T) {
		fx := newTestFixture(t)
		m := New(fx.store, "")
		m.Screen = ScreenSetup
		m.SetupInstalling = true

		result := &setup.Result{Agent: "opencode", Destination: "/tmp", Files: 1}
		updatedModel, _ := m.Update(setupInstallMsg{result: result})
		updated := updatedModel.(Model)

		if !updated.SetupDone {
			t.Fatal("opencode install should set SetupDone directly")
		}
		if updated.SetupAllowlistPrompt {
			t.Fatal("opencode install should NOT show allowlist prompt")
		}
	})

	t.Run("pressing y applies allowlist", func(t *testing.T) {
		oldFn := addClaudeCodeAllowlistFn
		t.Cleanup(func() { addClaudeCodeAllowlistFn = oldFn })

		called := false
		addClaudeCodeAllowlistFn = func() error {
			called = true
			return nil
		}

		m := New(nil, "")
		m.Screen = ScreenSetup
		m.SetupAllowlistPrompt = true
		m.SetupResult = &setup.Result{Agent: "claude-code"}

		updatedModel, _ := m.handleSetupKeys("y")
		updated := updatedModel.(Model)

		if !called {
			t.Fatal("pressing y should call addClaudeCodeAllowlistFn")
		}
		if !updated.SetupDone {
			t.Fatal("pressing y should set SetupDone")
		}
		if updated.SetupAllowlistPrompt {
			t.Fatal("pressing y should clear allowlist prompt")
		}
		if !updated.SetupAllowlistApplied {
			t.Fatal("pressing y should set SetupAllowlistApplied")
		}
	})

	t.Run("pressing y with error shows error", func(t *testing.T) {
		oldFn := addClaudeCodeAllowlistFn
		t.Cleanup(func() { addClaudeCodeAllowlistFn = oldFn })

		addClaudeCodeAllowlistFn = func() error {
			return errors.New("permission denied")
		}

		m := New(nil, "")
		m.Screen = ScreenSetup
		m.SetupAllowlistPrompt = true
		m.SetupResult = &setup.Result{Agent: "claude-code"}

		updatedModel, _ := m.handleSetupKeys("y")
		updated := updatedModel.(Model)

		if !updated.SetupDone {
			t.Fatal("pressing y with error should still set SetupDone")
		}
		if updated.SetupAllowlistApplied {
			t.Fatal("should not be applied on error")
		}
		if updated.SetupAllowlistError != "permission denied" {
			t.Fatalf("expected error message, got %q", updated.SetupAllowlistError)
		}
	})

	t.Run("pressing n skips allowlist", func(t *testing.T) {
		oldFn := addClaudeCodeAllowlistFn
		t.Cleanup(func() { addClaudeCodeAllowlistFn = oldFn })

		called := false
		addClaudeCodeAllowlistFn = func() error {
			called = true
			return nil
		}

		m := New(nil, "")
		m.Screen = ScreenSetup
		m.SetupAllowlistPrompt = true
		m.SetupResult = &setup.Result{Agent: "claude-code"}

		updatedModel, _ := m.handleSetupKeys("n")
		updated := updatedModel.(Model)

		if called {
			t.Fatal("pressing n should NOT call addClaudeCodeAllowlistFn")
		}
		if !updated.SetupDone {
			t.Fatal("pressing n should set SetupDone")
		}
		if updated.SetupAllowlistPrompt {
			t.Fatal("pressing n should clear allowlist prompt")
		}
	})

	t.Run("pressing esc skips allowlist", func(t *testing.T) {
		m := New(nil, "")
		m.Screen = ScreenSetup
		m.SetupAllowlistPrompt = true
		m.SetupResult = &setup.Result{Agent: "claude-code"}

		updatedModel, _ := m.handleSetupKeys("esc")
		updated := updatedModel.(Model)

		if !updated.SetupDone {
			t.Fatal("pressing esc should set SetupDone")
		}
		if updated.SetupAllowlistPrompt {
			t.Fatal("pressing esc should clear allowlist prompt")
		}
	})

	t.Run("other keys during prompt are ignored", func(t *testing.T) {
		m := New(nil, "")
		m.Screen = ScreenSetup
		m.SetupAllowlistPrompt = true
		m.SetupResult = &setup.Result{Agent: "claude-code"}

		updatedModel, cmd := m.handleSetupKeys("x")
		updated := updatedModel.(Model)

		if !updated.SetupAllowlistPrompt {
			t.Fatal("unknown key should not change prompt state")
		}
		if cmd != nil {
			t.Fatal("unknown key should not return command")
		}
	})

	t.Run("done reset clears allowlist state", func(t *testing.T) {
		m := New(nil, "")
		m.Screen = ScreenSetup
		m.SetupDone = true
		m.SetupResult = &setup.Result{Agent: "claude-code"}
		m.SetupAllowlistApplied = true
		m.SetupAllowlistError = "old error"

		updatedModel, _ := m.handleSetupKeys("enter")
		updated := updatedModel.(Model)

		if updated.Screen != ScreenDashboard {
			t.Fatal("enter on done should go to dashboard")
		}
		if updated.SetupAllowlistApplied {
			t.Fatal("should clear SetupAllowlistApplied")
		}
		if updated.SetupAllowlistError != "" {
			t.Fatal("should clear SetupAllowlistError")
		}
	})
}
