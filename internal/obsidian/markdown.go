package obsidian

import (
	"fmt"
	"strings"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// ObservationToMarkdown converts a store.Observation into an Obsidian-compatible
// markdown string with YAML frontmatter, an H1 title, the content body,
// and a wikilinks footer section.
func ObservationToMarkdown(obs store.Observation) string {
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
	fmt.Fprintf(&sb, "id: %d\n", obs.ID)
	fmt.Fprintf(&sb, "type: %s\n", obs.Type)
	fmt.Fprintf(&sb, "project: %s\n", project)
	fmt.Fprintf(&sb, "scope: %s\n", obs.Scope)
	if topicKey == "" {
		sb.WriteString("topic_key: \"\"\n")
	} else {
		fmt.Fprintf(&sb, "topic_key: %s\n", topicKey)
	}
	fmt.Fprintf(&sb, "session_id: %s\n", obs.SessionID)
	fmt.Fprintf(&sb, "created_at: %q\n", obs.CreatedAt)
	fmt.Fprintf(&sb, "updated_at: %q\n", obs.UpdatedAt)
	fmt.Fprintf(&sb, "revision_count: %d\n", obs.RevisionCount)
	fmt.Fprintf(&sb, "tags:\n  - %s\n", project)
	if obs.Type != "" {
		fmt.Fprintf(&sb, "  - %s\n", obs.Type)
	}
	fmt.Fprintf(&sb, "aliases:\n  - %q\n", obs.Title)
	sb.WriteString("---\n")

	// ── Title as H1 ───────────────────────────────────────────────────────────
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "# %s\n", obs.Title)
	sb.WriteString("\n")

	// ── Content Body ──────────────────────────────────────────────────────────
	sb.WriteString(obs.Content)
	sb.WriteString("\n")

	// ── Wikilinks Footer ──────────────────────────────────────────────────────
	wikilinks := buildWikilinks(obs.SessionID, topicKey)
	if len(wikilinks) > 0 {
		sb.WriteString("\n---\n")
		for _, link := range wikilinks {
			sb.WriteString(link)
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// buildWikilinks returns the wikilink lines for a given session ID and topic key.
// Session wikilink: [[session-{session_id}]] (only when session_id != "")
// Topic wikilink: [[topic-{prefix}]] (only when topic_key != "")
// where prefix = everything before the last "/" in topic_key, with "/" → "--"
func buildWikilinks(sessionID, topicKey string) []string {
	var links []string

	if sessionID != "" {
		links = append(links, fmt.Sprintf("*Session*: [[session-%s]]", sessionID))
	}

	if topicKey != "" {
		prefix := topicPrefix(topicKey)
		// Replace "/" with "--" for filesystem-safe wikilink names
		prefixSafe := strings.ReplaceAll(prefix, "/", "--")
		links = append(links, fmt.Sprintf("*Topic*: [[topic-%s]]", prefixSafe))
	}

	return links
}

// topicPrefix extracts the prefix from a topic_key.
// For "auth/jwt" → "auth"
// For "sdd/obsidian-plugin/explore" → "sdd/obsidian-plugin"
// For "standalone" → "standalone"
func topicPrefix(topicKey string) string {
	idx := strings.LastIndex(topicKey, "/")
	if idx < 0 {
		return topicKey
	}
	return topicKey[:idx]
}
