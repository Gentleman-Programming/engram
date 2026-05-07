package timeutil

import (
	"os"
	"time"
)

// FormatLocal converts a UTC timestamp string to local time based on the ENGRAM_TIMEZONE
// environment variable. If the variable is not set or invalid, it falls back to system local time.
// It tries to parse various time formats and returns the formatted string as "2006-01-02 15:04:05".
func FormatLocal(utc string) string {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
	} {
		if t, err := time.Parse(layout, utc); err == nil {
			return toConfiguredLocal(t.UTC()).Format("2006-01-02 15:04:05")
		}
	}
	return utc // unparseable — return as-is
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
