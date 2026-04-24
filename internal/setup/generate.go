package setup

// Sync embedded plugin copies from the source of truth (plugin/ directory).
// OpenCode and Pi use offline embedded assets.
// Run: go generate ./internal/setup/
//go:generate sh -c "rm -rf plugins/opencode plugins/pi && mkdir -p plugins/opencode plugins/pi/extensions plugins/pi/skills/engram && cp ../../plugin/opencode/engram.ts plugins/opencode/ && cp ../../plugin/pi/package.json plugins/pi/ && cp ../../plugin/pi/extensions/engram.ts plugins/pi/extensions/ && cp ../../plugin/pi/skills/engram/SKILL.md plugins/pi/skills/engram/"
