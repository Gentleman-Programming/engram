package timeutil

import (
	"os"
	"time"
)

// defaultLayout is used by FormatLocal when callers do not care about display style.
const defaultLayout = "2006-01-02 15:04:05"

// FormatLocal converts a UTC timestamp string to the configured display timezone using
// the default layout ("2006-01-02 15:04:05"). When ENGRAM_TIMEZONE is unset or invalid
// it falls back to system local time. Unparseable input is returned as-is.
//
// Accepted input layouts: "2006-01-02 15:04:05" (SQLite style), time.RFC3339, time.RFC3339Nano.
func FormatLocal(utc string) string {
	return FormatLocalWithLayout(utc, defaultLayout)
}

// FormatLocalWithLayout is the layout-aware variant of FormatLocal. It applies the same
// timezone conversion rules and accepts the same input formats, but renders the output
// with the caller's layout. Use this when a UI surface (e.g. the dashboard) needs a
// specific display style while still honoring ENGRAM_TIMEZONE.
//
// If the input cannot be parsed in any of the accepted layouts, the original string is
// returned unchanged so callers never lose data on malformed timestamps.
func FormatLocalWithLayout(utc, layout string) string {
	for _, in := range []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
	} {
		if t, err := time.Parse(in, utc); err == nil {
			return toConfiguredLocal(t.UTC()).Format(layout)
		}
	}
	return utc
}

func toConfiguredLocal(t time.Time) time.Time {
	tz := os.Getenv("ENGRAM_TIMEZONE")
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return t.In(loc)
		}
	}
	// Fallback to system local
	return t.Local()
}
