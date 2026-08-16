package setup

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed plugins/droid/scripts/*
var droidScriptsFS embed.FS

const droidMarketplace = "Gentleman-Programming/engram"

// droidMCPPath returns the user-level Droid MCP config path.
func droidMCPPath() string {
	home, _ := userHomeDir()
	return filepath.Join(home, ".factory", "mcp.json")
}

// droidHooksPath returns the user-level Droid hooks config path.
func droidHooksPath() string {
	home, _ := userHomeDir()
	return filepath.Join(home, ".factory", "hooks.json")
}

// droidHooksDir returns the directory where engram hook scripts are extracted.
func droidHooksDir() string {
	home, _ := userHomeDir()
	return filepath.Join(home, ".factory", "hooks", "engram")
}

// installDroid sets up Engram for the Droid CLI.
//
// It performs four steps:
//   1. Registers the engram MCP server in ~/.factory/mcp.json with the absolute
//      binary path so the subprocess never depends on PATH.
//   2. Extracts the UserPromptSubmit hook scripts to ~/.factory/hooks/engram/ so
//      they live at a stable, user-controlled path.
//   3. Writes a UserPromptSubmit entry to ~/.factory/hooks.json. This is required
//      because Droid (like Claude Code) does not execute UserPromptSubmit hooks
//      that are declared inside a plugin; they must be user-level hooks.
//   4. Installs the Engram plugin from the GitHub marketplace so Droid gets the
//      SessionStart, Stop, PreCompact, SubagentStop hooks and the Memory Protocol
//      skill.
func installDroid() (*Result, error) {
	if _, err := lookPathFn("droid"); err != nil {
		return nil, fmt.Errorf("droid CLI not found in PATH — install Droid first: https://docs.factory.ai/droid-cli/overview")
	}

	files := 0

	// Step 1: MCP registration.
	if err := injectDroidMCPFn(); err != nil {
		return nil, fmt.Errorf("register engram MCP server: %w", err)
	}
	files++

	// Step 2: Extract hook scripts to a stable user-level path.
	if err := extractDroidHookScriptsFn(); err != nil {
		return nil, fmt.Errorf("extract hook scripts: %w", err)
	}
	files++

	// Step 3: User-level UserPromptSubmit hook.
	if err := writeDroidUserPromptSubmitHookFn(); err != nil {
		return nil, fmt.Errorf("write UserPromptSubmit hook: %w", err)
	}
	files++

	// Step 4: Install the plugin via Droid marketplace.
	// This is best-effort: the plugin provides SessionStart/Stop/PreCompact/
	// SubagentStop hooks and the Memory Protocol skill, but the core memory
	// functionality already works via the MCP registration and user hook above.
	if err := installDroidPluginFn(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install Engram Droid plugin: %v\n", err)
		fmt.Fprintf(os.Stderr, "  Memory tools are still available via MCP. You can install the plugin manually later with:\n")
		fmt.Fprintf(os.Stderr, "    droid plugin marketplace add https://github.com/%s\n", droidMarketplace)
		fmt.Fprintf(os.Stderr, "    droid plugin install engram@engram --scope user\n")
	}

	return &Result{
		Agent:       "droid",
		Destination: filepath.Dir(droidMCPPath()),
		Files:       files,
	}, nil
}

// injectDroidMCP registers the engram MCP server in ~/.factory/mcp.json.
// Droid's user-level MCP config uses a top-level "mcpServers" object with
// "type": "stdio" entries.
func injectDroidMCP() error {
	path := droidMCPPath()
	config, err := readJSONConfig(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	servers := make(map[string]json.RawMessage)
	if raw, ok := config["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("parse mcpServers block in %s: %w", path, err)
		}
		if servers == nil {
			servers = make(map[string]json.RawMessage)
		}
	}

	cmd := resolveEngramCommand()
	entry := map[string]any{
		"type":    "stdio",
		"command": cmd,
		"args":    []string{"mcp", "--tools=agent"},
	}
	entryJSON, err := jsonMarshalFn(entry)
	if err != nil {
		return fmt.Errorf("marshal engram entry: %w", err)
	}
	servers["engram"] = json.RawMessage(entryJSON)

	blockJSON, err := jsonMarshalFn(servers)
	if err != nil {
		return fmt.Errorf("marshal mcpServers block: %w", err)
	}
	config["mcpServers"] = json.RawMessage(blockJSON)

	return writeJSONConfig(path, config)
}

// extractDroidHookScripts copies the embedded Droid hook scripts to
// ~/.factory/hooks/engram/ so the user-level hooks.json can reference a stable
// absolute path.
func extractDroidHookScripts() error {
	dir := droidHooksDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}

	return fs.WalkDir(droidScriptsFS, "plugins/droid/scripts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		data, err := droidScriptsFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}

		dest := filepath.Join(dir, filepath.Base(path))
		if err := writeFileFn(dest, data, 0755); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		return nil
	})
}

// writeDroidUserPromptSubmitHook writes (or updates) the UserPromptSubmit hook
// in ~/.factory/hooks.json to call the extracted engram script.
func writeDroidUserPromptSubmitHook() error {
	path := droidHooksPath()

	var config map[string]json.RawMessage
	data, err := readFileFn(path)
	if err != nil {
		if os.IsNotExist(err) {
			config = make(map[string]json.RawMessage)
		} else {
			return fmt.Errorf("read hooks config: %w", err)
		}
	} else {
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("parse hooks config: %w", err)
		}
	}

	var hooks map[string]json.RawMessage
	if raw, exists := config["hooks"]; exists {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return fmt.Errorf("parse hooks block: %w", err)
		}
		if hooks == nil {
			hooks = make(map[string]json.RawMessage)
		}
	} else {
		hooks = make(map[string]json.RawMessage)
	}

	scriptPath := filepath.Join(droidHooksDir(), "user-prompt-submit.sh")
	engramHook := []map[string]any{
		{
			"hooks": []map[string]any{
				{
					"type":    "command",
					"command": scriptPath,
					"timeout": 10,
				},
			},
		},
	}

	hookJSON, err := jsonMarshalFn(engramHook)
	if err != nil {
		return fmt.Errorf("marshal UserPromptSubmit hook: %w", err)
	}
	hooks["UserPromptSubmit"] = json.RawMessage(hookJSON)

	hooksJSON, err := jsonMarshalFn(hooks)
	if err != nil {
		return fmt.Errorf("marshal hooks block: %w", err)
	}
	config["hooks"] = json.RawMessage(hooksJSON)

	return writeJSONConfig(path, config)
}

// installDroidPlugin adds the Engram marketplace and installs the plugin.
func installDroidPlugin() error {
	// Add marketplace (idempotent).
	addOut, err := runCommand("droid", "plugin", "marketplace", "add", "https://github.com/"+droidMarketplace)
	addOutputStr := strings.TrimSpace(string(addOut))
	if err != nil {
		// If marketplace is already added, that's fine.
		if !strings.Contains(addOutputStr, "already") && !strings.Contains(addOutputStr, "exists") {
			return fmt.Errorf("marketplace add failed: %s", addOutputStr)
		}
	}

	// Install the plugin.
	installOut, err := runCommand("droid", "plugin", "install", "engram@engram", "--scope", "user")
	installOutputStr := strings.TrimSpace(string(installOut))
	if err != nil {
		if !strings.Contains(installOutputStr, "already") && !strings.Contains(installOutputStr, "installed") {
			return fmt.Errorf("plugin install failed: %s", installOutputStr)
		}
	}

	return nil
}
