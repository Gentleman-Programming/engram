// Package claudecode implements bidirectional sync between Engram and
// Claude Code's native memory folder.
//
// Claude Code stores memories as markdown files in:
//   ~/.claude/projects/{project-slug}/memory/
//
// Each project has a MEMORY.md (index) and project_*.md files.
//
// This package provides:
//   - Export: Engram observations → Claude Code memory .md files
//   - Import: Claude Code memory .md files → Engram observations
//
// Bidirectional sync ensures that users who switch between Claude Code
// native and other agents (OpenCode, VS Code, etc.) have unified memory.
package claudecode
