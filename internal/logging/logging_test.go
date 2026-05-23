package logging

import (
	"log/slog"
	"os"
	"testing"
)

func TestInitDefaults(t *testing.T) {
	os.Unsetenv("ENGRAM_LOG_LEVEL")
	os.Unsetenv("ENGRAM_LOG_FORMAT")

	Init()

	l := slog.Default()
	h := l.Handler()
	if _, ok := h.(*slog.TextHandler); !ok {
		t.Errorf("default handler = %T, want *slog.TextHandler", h)
	}
}

func TestInitJSON(t *testing.T) {
	os.Setenv("ENGRAM_LOG_FORMAT", "json")
	defer os.Unsetenv("ENGRAM_LOG_FORMAT")

	Init()

	l := slog.Default()
	h := l.Handler()
	if _, ok := h.(*slog.JSONHandler); !ok {
		t.Errorf("handler = %T, want *slog.JSONHandler", h)
	}
}

func TestInitLevelDebug(t *testing.T) {
	os.Setenv("ENGRAM_LOG_LEVEL", "debug")
	defer os.Unsetenv("ENGRAM_LOG_LEVEL")
	os.Unsetenv("ENGRAM_LOG_FORMAT")

	Init()

	l := slog.Default()
	h := l.Handler()
	th, ok := h.(*slog.TextHandler)
	if !ok {
		t.Fatalf("handler = %T, want *slog.TextHandler", h)
	}
	_ = th
	if !th.Enabled(nil, slog.LevelDebug) {
		t.Error("handler should be enabled at LevelDebug")
	}
}
