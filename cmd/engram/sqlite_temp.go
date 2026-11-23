package main

import "strings"

// usesLocalSQLite mirrors the existing dispatch spelling: main and cmdCloud
// are case-sensitive; only cmdCloudUpgrade normalizes its nested command.
// It scopes the new bootstrap, not existing validation or orphan migration.
func usesLocalSQLite(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "run", "serve", "mcp", "tui", "search", "save", "delete", "timeline", "conflicts", "doctor", "context", "stats", "export", "import", "sync", "obsidian-export", "projects":
		return true
	case "cloud":
		if len(args) < 2 {
			return false
		}
		switch args[1] {
		case "status", "enroll", "unenroll", "attest-prompt-source":
			return true
		case "upgrade":
			if len(args) >= 3 {
				switch strings.ToLower(strings.TrimSpace(args[2])) {
				case "doctor", "repair", "bootstrap", "remirror", "status", "rollback":
					return true
				}
			}
		}
	}
	return false
}
