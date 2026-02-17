package setup

// Sync embedded plugin copies from the source of truth (plugin/ directory).
// Run: go generate ./internal/setup/
//go:generate sh -c "rm -rf plugins/opencode plugins/claude-code && mkdir -p plugins/opencode plugins/claude-code && cp ../../plugin/opencode/engram.ts plugins/opencode/ && cp -r ../../plugin/claude-code/.claude-plugin ../../plugin/claude-code/.mcp.json ../../plugin/claude-code/hooks ../../plugin/claude-code/scripts ../../plugin/claude-code/skills plugins/claude-code/"
