package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file holds the agent registry: a data-driven description of every agent
// engram knows how to set up. Adding a new agent is (almost) only a matter of
// appending an entry to agentAdapters() — the generic injectMCP / writeInstruction
// machinery below handles the actual file writes. Agents with bespoke needs
// (embedded plugins, package managers, CLI bootstrapping, TOML configs) provide a
// `custom` installer instead and keep their hand-written logic in setup.go.

// mcpFormat describes how an agent stores its MCP server registration on disk.
type mcpFormat int

const (
	// mcpServersObject stores servers under a top-level "mcpServers" object, each
	// entry shaped {command, args}. Used by Gemini, Antigravity, Windsurf, Qwen,
	// Kiro, and Cursor.
	mcpServersObject mcpFormat = iota
	// serversObject stores servers under a top-level "servers" object, each entry
	// shaped {type:"stdio", command, args}. Used by VS Code (Copilot).
	serversObject
	// opencodeObject stores servers under a top-level "mcp" object, each entry
	// shaped {type:"local", command:[bin, ...args], enabled:true}. Used by
	// OpenCode and Kilocode.
	opencodeObject
)

// instrStyle describes how an agent's instruction/prompt surface is written.
type instrStyle int

const (
	// markerBlock writes the protocol as a marker-delimited block inside a shared
	// file, replacing only the managed block on re-run and preserving user content.
	markerBlock instrStyle = iota
	// wholeFile writes a dedicated file fully owned by engram (overwrite on re-run).
	wholeFile
)

// engramMarkerBegin/End delimit the managed Memory Protocol block in shared
// instruction surfaces so re-running setup replaces only that block.
const engramMarkerBegin = "<!-- BEGIN ENGRAM MEMORY PROTOCOL — managed by engram setup -->"
const engramMarkerEnd = "<!-- END ENGRAM MEMORY PROTOCOL -->"

// instrSurface is one instruction/prompt file an agent reads for the Memory Protocol.
type instrSurface struct {
	path  func() string
	style instrStyle
	body  string
}

// agentAdapter is the registry entry for a single agent. A declarative adapter
// sets mcpPath/mcpFormat and instructions; a bespoke one sets custom (which fully
// owns the install and ignores the declarative fields).
type agentAdapter struct {
	slug         string
	description  string
	mcpPath      func() string  // nil when the agent registers MCP some other way
	mcpFormat    mcpFormat      // how the MCP entry is shaped (ignored when mcpPath is nil)
	instructions []instrSurface // zero or more instruction surfaces to write
	bootstrap    func() error   // optional post-setup hook (e.g. seed a sibling config)
	custom       func() (*Result, error)
	installDir   func() string // display path for SupportedAgents (defaults to mcpPath)
	postInstall  []string      // "next steps" lines shown after install (nil = handled specially)
}

// displayDir returns the path shown to the user in `engram setup --help`.
func (a agentAdapter) displayDir() string {
	switch {
	case a.installDir != nil:
		return a.installDir()
	case a.mcpPath != nil:
		return a.mcpPath()
	default:
		return ""
	}
}

// userHome centralizes home-directory resolution for every declarative path
// helper. It returns an error when no non-empty absolute home can be determined,
// so callers never silently fall back to a relative path (which would write
// agent config into the current working directory instead of the user config
// root). Path helpers ignore the error because installFromAdapter validates
// resolvability up front via this same function before any write happens.
func userHome() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("could not determine an absolute user home directory (got %q)", home)
	}
	return home, nil
}

// mcpTopKey returns the top-level JSON key under which servers are stored.
func mcpTopKey(format mcpFormat) string {
	switch format {
	case serversObject:
		return "servers"
	case opencodeObject:
		return "mcp"
	default:
		return "mcpServers"
	}
}

// mcpEntry builds the engram server entry in the shape the given format expects,
// always resolving the absolute binary path so headless/Windows subprocesses
// don't depend on PATH.
func mcpEntry(format mcpFormat) any {
	cmd := resolveEngramCommand()
	switch format {
	case opencodeObject:
		return map[string]any{
			"type":    "local",
			"command": []string{cmd, "mcp", "--tools=agent"},
			"enabled": true,
		}
	case serversObject:
		return map[string]any{
			"type":    "stdio",
			"command": cmd,
			"args":    []string{"mcp", "--tools=agent"},
		}
	default:
		return map[string]any{
			"command": cmd,
			"args":    []string{"mcp", "--tools=agent"},
		}
	}
}

// injectMCP registers the engram MCP server in the JSON config at path using the
// given format, preserving any other servers and top-level keys. Idempotent:
// re-running updates the engram entry in place. Parent dirs are created as needed.
func injectMCP(path string, format mcpFormat) error {
	config, err := readJSONConfig(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	topKey := mcpTopKey(format)
	servers := make(map[string]json.RawMessage)
	if raw, ok := config[topKey]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("parse %s block in %s: %w", topKey, path, err)
		}
		// Unmarshalling a JSON null leaves the map nil; re-init so the engram
		// assignment below doesn't panic on configs like "mcpServers": null.
		if servers == nil {
			servers = make(map[string]json.RawMessage)
		}
	}

	entryJSON, err := jsonMarshalFn(mcpEntry(format))
	if err != nil {
		return fmt.Errorf("marshal engram entry: %w", err)
	}
	servers["engram"] = json.RawMessage(entryJSON)

	blockJSON, err := jsonMarshalFn(servers)
	if err != nil {
		return fmt.Errorf("marshal %s block: %w", topKey, err)
	}
	config[topKey] = json.RawMessage(blockJSON)

	return writeJSONConfig(path, config)
}

// upsertMarkerBlock writes body delimited by begin/end markers into the file at
// path. If the markers already exist the managed block is replaced in place;
// otherwise it is appended, preserving existing user content. CRLF is normalized
// to LF. The file (and its parent dir) is created when missing.
func upsertMarkerBlock(path, begin, end, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}
	block := begin + "\n\n" + body + "\n" + end + "\n"

	existing, err := readFileFn(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := writeFileFn(path, []byte(block), 0644); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}

	text := strings.ReplaceAll(string(existing), "\r\n", "\n")
	start := strings.Index(text, begin)
	stop := strings.Index(text, end)
	if start != -1 && stop != -1 && stop > start {
		stop += len(end)
		text = text[:start] + strings.TrimRight(block, "\n") + text[stop:]
	} else {
		text = strings.TrimRight(text, "\n") + "\n\n" + block
	}

	if err := writeFileFn(path, []byte(text), 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// writeInstruction writes a single instruction surface according to its style.
func writeInstruction(ins instrSurface) error {
	path := ins.path()
	if ins.style == wholeFile {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return fmt.Errorf("create dir for %s: %w", path, err)
		}
		if err := writeFileFn(path, []byte(ins.body), 0644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		return nil
	}
	return upsertMarkerBlock(path, engramMarkerBegin, engramMarkerEnd, ins.body)
}

// installFromAdapter runs a declarative adapter's install steps: MCP registration,
// instruction surfaces, then an optional bootstrap hook. Bespoke adapters delegate
// entirely to their custom installer.
func installFromAdapter(a agentAdapter) (*Result, error) {
	if a.custom != nil {
		return a.custom()
	}

	// Declarative path helpers build their destinations from the user home dir.
	// Validate once here so an unresolvable home fails the install instead of
	// silently writing config into the current working directory.
	if _, err := userHome(); err != nil {
		return nil, err
	}

	files := 0
	if a.mcpPath != nil {
		if err := injectMCP(a.mcpPath(), a.mcpFormat); err != nil {
			return nil, err
		}
		files++
	}
	for _, ins := range a.instructions {
		if err := writeInstruction(ins); err != nil {
			return nil, err
		}
		files++
	}
	if a.bootstrap != nil {
		if err := a.bootstrap(); err != nil {
			return nil, err
		}
	}

	dest := ""
	if a.mcpPath != nil {
		dest = filepath.Dir(a.mcpPath())
	}
	return &Result{Agent: a.slug, Destination: dest, Files: files}, nil
}

// PostInstallSteps returns the human-facing "next steps" lines for an agent, or
// nil when the agent's post-install messaging is handled specially by the CLI
// (opencode's conditional TUI note, claude-code's interactive allowlist prompt).
func PostInstallSteps(agent string) []string {
	for _, a := range agentAdapters() {
		if a.slug == agent {
			return a.postInstall
		}
	}
	return nil
}

// supportedSlugs returns the registry slugs in registration order.
func supportedSlugs() []string {
	adapters := agentAdapters()
	slugs := make([]string, 0, len(adapters))
	for _, a := range adapters {
		slugs = append(slugs, a.slug)
	}
	return slugs
}
