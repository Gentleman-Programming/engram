package setup

// Sync embedded plugin copies from the source of truth (plugin/ directory).
// Only OpenCode needs embedding — Claude Code is installed via marketplace.
// Run: go generate ./internal/setup/
//go:generate sh -c "rm -rf plugins/opencode && mkdir -p plugins/opencode && cp ../../plugin/opencode/engram.ts plugins/opencode/"
//go:generate sh -c "rm -rf plugins/opencode-v2 && mkdir -p plugins/opencode-v2 && cp ../../plugin/opencode-v2/engram.ts plugins/opencode-v2/"
