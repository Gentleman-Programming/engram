// Package format provides shared formatting logic for engram data.
// Both the local store and the cloud server use these functions
// to produce identical output regardless of backend.
package format

import (
	"fmt"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/types"
)

// Context formats sessions, observations, and prompts into a markdown string
// suitable for providing memory context to AI agents.
// Returns empty string if all inputs are empty.
func Context(sessions []types.SessionSummary, observations []types.Observation, prompts []types.Prompt) string {
	if len(sessions) == 0 && len(observations) == 0 && len(prompts) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Memory from Previous Sessions\n\n")

	if len(sessions) > 0 {
		b.WriteString("### Recent Sessions\n")
		for _, sess := range sessions {
			summary := ""
			if sess.Summary != nil {
				summary = fmt.Sprintf(": %s", Truncate(*sess.Summary, 200))
			}
			fmt.Fprintf(&b, "- **%s** (%s)%s [%d observations]\n",
				sess.Project, sess.StartedAt, summary, sess.ObservationCount)
		}
		b.WriteString("\n")
	}

	if len(prompts) > 0 {
		b.WriteString("### Recent User Prompts\n")
		for _, p := range prompts {
			fmt.Fprintf(&b, "- %s: %s\n", p.CreatedAt, Truncate(p.Content, 200))
		}
		b.WriteString("\n")
	}

	if len(observations) > 0 {
		b.WriteString("### Recent Observations\n")
		for _, obs := range observations {
			fmt.Fprintf(&b, "- [%s] **%s**: %s\n",
				obs.Type, obs.Title, Truncate(obs.Content, 300))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// Truncate shortens a string to max runes, appending "..." if truncated.
func Truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
