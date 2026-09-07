package tui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// textInput is the single-line input used by the TUI. Clipboard discovery is
// intentionally deferred until the user requests a clipboard paste.
type textInput struct {
	Placeholder string
	CharLimit   int
	Width       int

	value   []rune
	cursor  int
	focused bool
}

type textInputPasteMsg struct {
	content string
	err     error
}

var readInputClipboard = readSystemClipboard

func newTextInput() textInput {
	return textInput{}
}

func (m *textInput) SetValue(value string) {
	m.value = sanitizeInput([]rune(value))
	if m.CharLimit > 0 && len(m.value) > m.CharLimit {
		m.value = m.value[:m.CharLimit]
	}
	m.cursor = len(m.value)
}

func (m textInput) Value() string {
	return string(m.value)
}

func (m *textInput) Focus() tea.Cmd {
	m.focused = true
	return nil
}

func (m *textInput) Blur() {
	m.focused = false
}

func (m textInput) Focused() bool {
	return m.focused
}

func (m textInput) Update(msg tea.Msg) (textInput, tea.Cmd) {
	if !m.focused {
		return m, nil
	}

	switch msg := msg.(type) {
	case textInputPasteMsg:
		if msg.err == nil {
			m.insert([]rune(msg.content))
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+v":
			return m, func() tea.Msg {
				content, err := readInputClipboard()
				return textInputPasteMsg{content: content, err: err}
			}
		case "left", "ctrl+b":
			m.moveCursor(-1)
		case "right", "ctrl+f":
			m.moveCursor(1)
		case "home", "ctrl+a":
			m.cursor = 0
		case "end", "ctrl+e":
			m.cursor = len(m.value)
		case "backspace", "ctrl+h":
			m.deleteBefore()
		case "delete", "ctrl+d":
			m.deleteAfter()
		case "ctrl+u":
			m.value = m.value[m.cursor:]
			m.cursor = 0
		case "ctrl+k":
			m.value = m.value[:m.cursor]
		case "ctrl+w":
			m.deleteWordBefore()
		case "alt+d":
			m.deleteWordAfter()
		default:
			m.insert(msg.Runes)
			return m, func() tea.Msg { return nil }
		}
	}
	return m, nil
}

func (m textInput) View() string {
	value := m.value
	cursor := m.cursor
	if m.Width > 0 && len(value) > m.Width {
		start := max(0, cursor-m.Width+1)
		end := min(len(value), start+m.Width)
		value = value[start:end]
		cursor -= start
	}

	if len(value) == 0 && m.Placeholder != "" {
		if m.focused {
			return "> " + lipgloss.NewStyle().Reverse(true).Render(string([]rune(m.Placeholder)[0])) + string([]rune(m.Placeholder)[1:])
		}
		return "> " + m.Placeholder
	}

	before := string(value[:cursor])
	after := string(value[cursor:])
	if !m.focused {
		return "> " + before + after
	}
	if after == "" {
		return "> " + before + lipgloss.NewStyle().Reverse(true).Render(" ")
	}
	first, rest := []rune(after)[0], []rune(after)[1:]
	return "> " + before + lipgloss.NewStyle().Reverse(true).Render(string(first)) + string(rest)
}

func (m *textInput) insert(value []rune) {
	value = sanitizeInput(value)
	if m.CharLimit > 0 {
		remaining := m.CharLimit - len(m.value)
		if remaining <= 0 {
			return
		}
		if len(value) > remaining {
			value = value[:remaining]
		}
	}
	m.value = append(m.value[:m.cursor], append(value, m.value[m.cursor:]...)...)
	m.cursor += len(value)
}

func (m *textInput) moveCursor(delta int) {
	m.cursor = min(len(m.value), max(0, m.cursor+delta))
}

func (m *textInput) deleteBefore() {
	if m.cursor == 0 {
		return
	}
	m.value = append(m.value[:m.cursor-1], m.value[m.cursor:]...)
	m.cursor--
}

func (m *textInput) deleteAfter() {
	if m.cursor >= len(m.value) {
		return
	}
	m.value = append(m.value[:m.cursor], m.value[m.cursor+1:]...)
}

func (m *textInput) deleteWordBefore() {
	for m.cursor > 0 && unicode.IsSpace(m.value[m.cursor-1]) {
		m.deleteBefore()
	}
	for m.cursor > 0 && !unicode.IsSpace(m.value[m.cursor-1]) {
		m.deleteBefore()
	}
}

func (m *textInput) deleteWordAfter() {
	for m.cursor < len(m.value) && unicode.IsSpace(m.value[m.cursor]) {
		m.deleteAfter()
	}
	for m.cursor < len(m.value) && !unicode.IsSpace(m.value[m.cursor]) {
		m.deleteAfter()
	}
}

func sanitizeInput(value []rune) []rune {
	sanitized := make([]rune, 0, len(value))
	for _, r := range value {
		switch r {
		case '\t', '\n', '\r':
			sanitized = append(sanitized, ' ')
		default:
			if !unicode.IsControl(r) {
				sanitized = append(sanitized, r)
			}
		}
	}
	return sanitized
}

func readSystemClipboard() (string, error) {
	for _, command := range clipboardCommands() {
		output, err := exec.Command(command[0], command[1:]...).Output()
		if err == nil {
			return string(output), nil
		}
	}
	return "", fmt.Errorf("no supported clipboard command is available")
}

func clipboardCommands() [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"pbpaste"}}
	case "windows":
		return [][]string{{"powershell.exe", "Get-Clipboard"}}
	default:
		commands := make([][]string, 0, 5)
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			commands = append(commands, []string{"wl-paste", "--no-newline"})
		}
		return append(commands,
			[]string{"xclip", "-out", "-selection", "clipboard"},
			[]string{"xsel", "--output", "--clipboard"},
			[]string{"termux-clipboard-get"},
			[]string{"powershell.exe", "Get-Clipboard"},
		)
	}
}
