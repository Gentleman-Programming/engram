package tui

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func TestViewObservationDetailWrapping(t *testing.T) {
	m := Model{
		Width:  40,
		Height: 20,
		Screen: ScreenObservationDetail,
		SelectedObservation: &store.Observation{
			ID:      1,
			Type:    "note",
			Title:   "Test",
			Content: "This is a very long line of text that should definitely be wrapped when the width is only forty characters wide.",
		},
	}

	view := m.viewObservationDetail()

	// The line "This is a very long line of text that should definitely be wrapped when the width is only forty characters wide."
	// is much longer than 40 chars. With wrapWidth = Width - 6 = 34, it should split.

	lines := strings.Split(view, "\n")
	contentStarted := false
	contentLines := 0

	for _, line := range lines {
		if strings.Contains(line, "Content") {
			contentStarted = true
			continue
		}
		if contentStarted && strings.TrimSpace(line) != "" && !strings.Contains(line, "scroll") {
			contentLines++
		}
	}

	if contentLines <= 1 {
		t.Errorf("Expected content to be wrapped into multiple lines, but got %d content lines", contentLines)
	}
}
