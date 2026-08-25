package tui

import (
	"encoding/base64"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── Messages ─────────────────────────────────────────────────────────────────

// clipboardCopiedMsg is sent after building the OSC 52 sequence to copy.
// The sequence field carries the raw escape sequence so Update can emit it.
type clipboardCopiedMsg struct {
	sequence string
}

// clipboardClearMsg is sent after the 2-second feedback timer expires.
type clipboardClearMsg struct{}

// ─── OSC 52 helpers ──────────────────────────────────────────────────────────

// osc52Sequence returns the OSC 52 terminal escape sequence that asks the
// terminal to write content to the system clipboard.
//
// Format: ESC ] 52 ; c ; <base64> BEL
func osc52Sequence(content string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(content))
	return fmt.Sprintf("\x1b]52;c;%s\x07", b64)
}

// copyToClipboard returns a Cmd that builds the OSC 52 sequence for content
// and wraps it in a clipboardCopiedMsg.
func copyToClipboard(content string) tea.Cmd {
	return func() tea.Msg {
		return clipboardCopiedMsg{sequence: osc52Sequence(content)}
	}
}

// clearFeedbackAfter returns a Cmd that sends clipboardClearMsg after d.
func clearFeedbackAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(_ time.Time) tea.Msg {
		return clipboardClearMsg{}
	})
}
