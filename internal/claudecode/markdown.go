package claudecode

import (
	"fmt"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// MemoryFileFormat generates the markdown format used by Claude Code's
// memory system.
//
// Format:
//   ---
//   name: {title}
//   description: {content preview}
//   type: {type}
//   originSessionId: {session_id}
//   ---
//   ## {title}
//
//   {content}
func MemoryFileFormat(obs store.Observation) string {
	var sb strings.Builder

	topicKey := ""
	if obs.TopicKey != nil {
		topicKey = *obs.TopicKey
	}
	project := ""
	if obs.Project != nil {
		project = *obs.Project
	}

	// ── YAML Frontmatter ──────────────────────────────────────────────────────
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", escapeYaml(obs.Title))
	// Description: first 200 chars of content as preview
	desc := obs.Content
	if len(desc) > 200 {
		desc = desc[:200] + "..."
	}
	fmt.Fprintf(&sb, "description: %s\n", escapeYaml(desc))
	fmt.Fprintf(&sb, "type: %s\n", obs.Type)
	if obs.SessionID != "" {
		fmt.Fprintf(&sb, "originSessionId: %s\n", obs.SessionID)
	}
	if project != "" {
		fmt.Fprintf(&sb, "project: %s\n", project)
	}
	if topicKey != "" {
		fmt.Fprintf(&sb, "topic_key: %s\n", topicKey)
	}
	sb.WriteString("---\n")

	// ── Title as H2 ─────────────────────────────────────────────────────────
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "## %s\n", obs.Title)
	sb.WriteString("\n")

	// ── Content Body ─────────────────────────────────────────────────────────
	sb.WriteString(obs.Content)
	sb.WriteString("\n")

	return sb.String()
}

// MemoryIndexFormat generates the MEMORY.md index file that lists all
// memory files for a project.
func MemoryIndexFormat(projectName string, entries []MemoryIndexEntry) string {
	var sb strings.Builder

	sb.WriteString("# Memory Index\n")
	sb.WriteString("\n")

	for _, e := range entries {
		desc := e.Description
		if len(desc) > 100 {
			desc = desc[:100] + "..."
		}
		fmt.Fprintf(&sb, "- [%s](%s) — %s\n", e.Title, e.Filename, desc)
	}

	return sb.String()
}

// MemoryIndexEntry represents a single entry in the MEMORY.md index.
type MemoryIndexEntry struct {
	Title       string
	Filename    string
	Description string
}

// escapeYaml escapes a string for safe inclusion in YAML frontmatter.
// It handles double quotes, colons, and newlines.
func escapeYaml(s string) string {
	// If the string contains characters that need escaping, use double quotes
	// and escape internal double quotes
	if strings.ContainsAny(s, "\":\n") {
		escaped := strings.ReplaceAll(s, "\"", "\\\"")
		escaped = strings.ReplaceAll(escaped, "\n", " ")
		return "\"" + escaped + "\""
	}
	return s
}
