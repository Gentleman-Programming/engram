package tui

import (
	"errors"
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
	input.Focus()

	updated, cmd := input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("alpha"), Paste: true})
	if cmd == nil {
		t.Fatal("terminal paste command is nil")
	}
	if updated.Value() != "alph" {
		t.Fatalf("value = %q, want %q", updated.Value(), "alph")
	}
}

func TestTextInputIgnoresUpdatesWhileBlurred(t *testing.T) {
	input := newTextInput()
	input.SetValue("saved")

	for _, msg := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")},
		textInputPasteMsg{content: "changed"},
	} {
		updated, cmd := input.Update(msg)
		if cmd != nil || updated.Value() != "saved" {
			t.Fatalf("blurred update = (%q, %v), want (saved, nil)", updated.Value(), cmd)
		}
	}

	input.Focus()
	updated, cmd := input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if cmd == nil || updated.Value() != "savedx" {
		t.Fatalf("focused input = (%q, %v), want (savedx, non-nil)", updated.Value(), cmd)
	}
}

func TestTextInputClipboardFailureLeavesValueUnchanged(t *testing.T) {
	original := readInputClipboard
	readInputClipboard = func() (string, error) { return "", errors.New("clipboard unavailable") }
	t.Cleanup(func() { readInputClipboard = original })

	input := newTextInput()
	input.SetValue("saved")
	input.Focus()
	updated, cmd := input.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if cmd == nil {
		t.Fatal("ctrl+v command is nil")
	}
	updated, _ = updated.Update(cmd())
	if updated.Value() != "saved" {
		t.Fatalf("value after failed clipboard paste = %q, want saved", updated.Value())
	}
}

func TestTextInputSanitizesControlRunes(t *testing.T) {
	input := newTextInput()
	input.SetValue("a\tb\r\nc\x1bd\u0085e")
	if input.Value() != "a b  cde" {
		t.Fatalf("sanitized value = %q, want %q", input.Value(), "a b  cde")
	}
}

func TestTextInputCursorAndDeletionBoundaries(t *testing.T) {
	input := newTextInput()
	input.SetValue("abc")
	input.Focus()
	input, _ = input.Update(tea.KeyMsg{Type: tea.KeyLeft})
	input, _ = input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	if input.Value() != "abXc" {
		t.Fatalf("cursor insertion = %q, want abXc", input.Value())
	}

	input.SetValue("abc")
	for _, msg := range []tea.KeyMsg{{Type: tea.KeyHome}, {Type: tea.KeyBackspace}, {Type: tea.KeyEnd}, {Type: tea.KeyDelete}} {
		input, _ = input.Update(msg)
	}
	if input.Value() != "abc" {
		t.Fatalf("boundary deletions = %q, want abc", input.Value())
	}

	input.SetValue("abc")
	input, _ = input.Update(tea.KeyMsg{Type: tea.KeyHome})
	input, _ = input.Update(tea.KeyMsg{Type: tea.KeyRight})
	input, _ = input.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if input.Value() != "ac" {
		t.Fatalf("delete at cursor = %q, want ac", input.Value())
	}
}
