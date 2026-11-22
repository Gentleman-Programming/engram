package plugin

import (
	"embed"
	"io/fs"
)

//go:embed all:claude-code
var claudeAssets embed.FS

// ClaudeCodeAssets contains the full Claude plugin template shipped with core.
func ClaudeCodeAssets() fs.FS {
	files, err := fs.Sub(claudeAssets, "claude-code")
	if err != nil {
		panic(err)
	}
	return files
}
