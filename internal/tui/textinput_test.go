package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTextInputCtrlVPastesClipboardLazily(t *testing.T) {
	original := readInputClipboard
	called := false
	readInputClipboard = func() (string, error) {
		called = true
		return "one\ntwo", nil
	}
	t.Cleanup(func() { readInputClipboard = original })

	input := newTextInput()
	input.Focus()
	updated, cmd := input.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if cmd == nil {
		t.Fatal("ctrl+v command is nil")
	}
	if updated.Value() != "" {
		t.Fatalf("value before clipboard command = %q, want empty", updated.Value())
	}
	if called {
		t.Fatal("clipboard reader ran before the paste command")
	}

	updated, cmd = updated.Update(cmd())
	if !called {
		t.Fatal("clipboard reader did not run for the paste command")
	}
	if cmd != nil {
		t.Fatal("clipboard result returned an unexpected command")
	}
	if updated.Value() != "one two" {
		t.Fatalf("value after clipboard paste = %q, want %q", updated.Value(), "one two")
	}
}

func TestTextInputPastedTerminalInputRespectsCharacterLimit(t *testing.T) {
	input := newTextInput()
	input.CharLimit = 4

	updated, cmd := input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("alpha"), Paste: true})
	if cmd == nil {
		t.Fatal("terminal paste command is nil")
	}
	if updated.Value() != "alph" {
		t.Fatalf("value = %q, want %q", updated.Value(), "alph")
	}
}
