package logging

import (
	"log/slog"
	"os"
	"strings"
)

func Init() {
	level := parseLevel()
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if parseFormat() == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	slog.SetDefault(slog.New(handler))
}

func parseLevel() slog.Level {
	switch strings.ToLower(os.Getenv("ENGRAM_LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func parseFormat() string {
	if strings.EqualFold(os.Getenv("ENGRAM_LOG_FORMAT"), "json") {
		return "json"
	}
	return "text"
}
