// Engram — Persistent memory for AI coding agents.
//
// Usage:
//
//	engram serve          Start HTTP + MCP server
//	engram mcp            Start MCP server only (stdio transport)
//	engram search <query> Search memories from CLI
//	engram save           Save a memory from CLI
//	engram context        Show recent context
//	engram stats          Show memory stats
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Gentleman-Programming/engram/internal/mcp"
	"github.com/Gentleman-Programming/engram/internal/obsidian"
	"github.com/Gentleman-Programming/engram/internal/project"
	"github.com/Gentleman-Programming/engram/internal/server"
	"github.com/Gentleman-Programming/engram/internal/setup"
	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
	"github.com/Gentleman-Programming/engram/internal/tui"
	versioncheck "github.com/Gentleman-Programming/engram/internal/version"

	tea "github.com/charmbracelet/bubbletea"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// version is set via ldflags at build time by goreleaser.
// Falls back to "dev" for local builds; init() tries Go module info first.
var version = "dev"

func init() {
	if version != "dev" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = strings.TrimPrefix(info.Main.Version, "v")
	}
}

var (
	storeNew      = store.New
	newHTTPServer = server.New
	startHTTP     = (*server.Server).Start

	newMCPServer           = mcp.NewServer
	newMCPServerWithTools  = mcp.NewServerWithTools
	newMCPServerWithConfig = mcp.NewServerWithConfig
	resolveMCPTools        = mcp.ResolveTools
	serveMCP               = mcpserver.ServeStdio

	// detectProject is injectable for testing; wraps project.DetectProject.
	detectProject = project.DetectProject

	newTUIModel   = func(s *store.Store) tui.Model { return tui.New(s, version) }
	newTeaProgram = tea.NewProgram
	runTeaProgram = (*tea.Program).Run

	checkForUpdates = versioncheck.CheckLatest

	setupSupportedAgents        = setup.SupportedAgents
	setupInstallAgent           = setup.Install
	setupAddClaudeCodeAllowlist = setup.AddClaudeCodeAllowlist
	scanInputLine               = fmt.Scanln

	storeSearch = func(s *store.Store, query string, opts store.SearchOptions) ([]store.SearchResult, error) {
		return s.Search(query, opts)
	}
	storeAddObservation = func(s *store.Store, p store.AddObservationParams) (int64, error) { return s.AddObservation(p) }
	storeTimeline       = func(s *store.Store, observationID int64, before, after int) (*store.TimelineResult, error) {
		return s.Timeline(observationID, before, after)
	}
	storeFormatContext = func(s *store.Store, project, scope string) (string, error) { return s.FormatContext(project, scope) }
	storeStats         = func(s *store.Store) (*store.Stats, error) { return s.Stats() }
	storeExport        = func(s *store.Store) (*store.ExportData, error) { return s.Export() }
	jsonMarshalIndent  = json.MarshalIndent

	syncStatus = func(sy *engramsync.Syncer) (localChunks int, remoteChunks int, pendingImport int, err error) {
		return sy.Status()
	}
	syncImport = func(sy *engramsync.Syncer) (*engramsync.ImportResult, error) { return sy.Import() }
	syncExport = func(sy *engramsync.Syncer, createdBy, project string) (*engramsync.SyncResult, error) {
		return sy.Export(createdBy, project)
	}

	exitFunc = os.Exit

	stdinScanner = func() *bufio.Scanner { return bufio.NewScanner(os.Stdin) }
	userHomeDir  = os.UserHomeDir

	// newObsidianExporter is injectable for testing.
	newObsidianExporter = obsidian.NewExporter

	// newObsidianWatcher is injectable for testing.
	newObsidianWatcher = obsidian.NewWatcher
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		exitFunc(1)
	}

	// Check for updates on every invocation.
	if result := checkForUpdates(version); result.Status != versioncheck.StatusUpToDate && result.Message != "" {
		fmt.Fprintln(os.Stderr, result.Message)
		fmt.Fprintln(os.Stderr)
	}

	cfg, cfgErr := store.DefaultConfig()
	if cfgErr != nil {
		// Fallback: try to resolve home directory from environment variables
		// that os.UserHomeDir() might have missed (e.g. MCP subprocesses on
		// Windows where %USERPROFILE% is not propagated).
		if home := resolveHomeFallback(); home != "" {
			log.Printf("[engram] UserHomeDir failed, using fallback: %s", home)
			cfg = store.FallbackConfig(filepath.Join(home, ".engram"))
		} else {
			fatal(cfgErr)
		}
	}

	// Allow overriding data dir via env
	if dir := os.Getenv("ENGRAM_DATA_DIR"); dir != "" {
		cfg.DataDir = dir
	}

	// Migrate orphaned databases that ended up in wrong locations
	// (e.g. drive root on Windows due to previous bug).
	migrateOrphanedDB(cfg.DataDir)

	switch os.Args[1] {
	case "serve":
		cmdServe(cfg)
	case "mcp":
		cmdMCP(cfg)
	case "tui":
		cmdTUI(cfg)
	case "search":
		cmdSearch(cfg)
	case "save":
		cmdSave(cfg)
	case "timeline":
		cmdTimeline(cfg)
	case "context":
		cmdContext(cfg)
	case "stats":
		cmdStats(cfg)
	case "export":
		cmdExport(cfg)
	case "import":
		cmdImport(cfg)
	case "sync":
		cmdSync(cfg)
	case "obsidian-export":
		cmdObsidianExport(cfg)
	case "projects":
		cmdProjects(cfg)
	case "cloud":
		cmdCloud(cfg)
	case "setup":
		cmdSetup()
	case "version", "--version", "-v":
		fmt.Printf("engram %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		exitFunc(1)
	}
}

// ─── Commands ────────────────────────────────────────────────────────────────

func cmdServe(cfg store.Config) {
	port := 7437 // "ENGR" on phone keypad vibes
	if p := os.Getenv("ENGRAM_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	// Allow: engram serve 8080
	if len(os.Args) > 2 {
		if n, err := strconv.Atoi(os.Args[2]); err == nil {
			port = n
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	srv := newHTTPServer(s, port)

	// Graceful shutdown on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[engram] shutting down...")
		exitFunc(0)
	}()

	if err := startHTTP(srv); err != nil {
		fatal(err)
	}
}

func cmdMCP(cfg store.Config) {
	// Parse --tools and --project flags
	toolsFilter := ""
	projectOverride := ""
	for i := 2; i < len(os.Args); i++ {
		if strings.HasPrefix(os.Args[i], "--tools=") {
			toolsFilter = strings.TrimPrefix(os.Args[i], "--tools=")
		} else if os.Args[i] == "--tools" && i+1 < len(os.Args) {
			toolsFilter = os.Args[i+1]
			i++
		} else if strings.HasPrefix(os.Args[i], "--project=") {
			projectOverride = strings.TrimPrefix(os.Args[i], "--project=")
		} else if os.Args[i] == "--project" && i+1 < len(os.Args) {
			projectOverride = os.Args[i+1]
			i++
		}
	}

	// Project detection chain: --project flag → ENGRAM_PROJECT env → git detection
	detectedProject := projectOverride
	if detectedProject == "" {
		detectedProject = os.Getenv("ENGRAM_PROJECT")
	}
	if detectedProject == "" {
		if cwd, err := os.Getwd(); err == nil {
			detectedProject = detectProject(cwd)
		}
	}
	// Always normalize (lowercase + trim)
	detectedProject, _ = store.NormalizeProject(detectedProject)

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	mcpCfg := mcp.MCPConfig{
		DefaultProject: detectedProject,
	}

	allowlist := resolveMCPTools(toolsFilter)
	mcpSrv := newMCPServerWithConfig(s, mcpCfg, allowlist)

	if err := serveMCP(mcpSrv); err != nil {
		fatal(err)
	}
}

func cmdTUI(cfg store.Config) {
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	model := newTUIModel(s)
	p := newTeaProgram(model)
	if _, err := runTeaProgram(p); err != nil {
		fatal(err)
	}
}

func cmdSearch(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: engram search <query> [--type TYPE] [--project PROJECT] [--scope SCOPE] [--limit N]")
		exitFunc(1)
	}

	// Collect the query (everything that's not a flag)
	var queryParts []string
	opts := store.SearchOptions{Limit: 10}

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--type":
			if i+1 < len(os.Args) {
				opts.Type = os.Args[i+1]
				i++
			}
		case "--project":
			if i+1 < len(os.Args) {
				opts.Project = os.Args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(os.Args) {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil {
					opts.Limit = n
				}
				i++
			}
		case "--scope":
			if i+1 < len(os.Args) {
				opts.Scope = os.Args[i+1]
				i++
			}
		default:
			queryParts = append(queryParts, os.Args[i])
		}
	}

	query := strings.Join(queryParts, " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "error: search query is required")
		exitFunc(1)
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	results, err := storeSearch(s, query, opts)
	if err != nil {
		fatal(err)
		return
	}

	if len(results) == 0 {
		fmt.Printf("No memories found for: %q\n", query)
		return
	}

	fmt.Printf("Found %d memories:\n\n", len(results))
	for i, r := range results {
		project := ""
		if r.Project != nil {
			project = fmt.Sprintf(" | project: %s", *r.Project)
		}
		fmt.Printf("[%d] #%d (%s) — %s\n    %s\n    %s%s | scope: %s\n\n",
			i+1, r.ID, r.Type, r.Title,
			truncate(r.Content, 300),
			r.CreatedAt, project, r.Scope)
	}
}

func cmdSave(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: engram save <title> <content> [--type TYPE] [--project PROJECT] [--scope SCOPE] [--topic TOPIC_KEY]")
		exitFunc(1)
	}

	title := os.Args[2]
	content := os.Args[3]
	typ := "manual"
	project := ""
	scope := "project"
	topicKey := ""

	for i := 4; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--type":
			if i+1 < len(os.Args) {
				typ = os.Args[i+1]
				i++
			}
		case "--project":
			if i+1 < len(os.Args) {
				project = os.Args[i+1]
				i++
			}
		case "--scope":
			if i+1 < len(os.Args) {
				scope = os.Args[i+1]
				i++
			}
		case "--topic":
			if i+1 < len(os.Args) {
				topicKey = os.Args[i+1]
				i++
			}
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	sessionID := "manual-save"
	if project != "" {
		sessionID = "manual-save-" + project
	}
	s.CreateSession(sessionID, project, "")
	id, err := storeAddObservation(s, store.AddObservationParams{
		SessionID: sessionID,
		Type:      typ,
		Title:     title,
		Content:   content,
		Project:   project,
		Scope:     scope,
		TopicKey:  topicKey,
	})
	if err != nil {
		fatal(err)
	}

	fmt.Printf("Memory saved: #%d %q (%s)\n", id, title, typ)
}

func cmdTimeline(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: engram timeline <observation_id> [--before N] [--after N]")
		exitFunc(1)
	}

	obsID, err := strconv.ParseInt(os.Args[2], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid observation id %q\n", os.Args[2])
		exitFunc(1)
	}

	before, after := 5, 5
	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--before":
			if i+1 < len(os.Args) {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil {
					before = n
				}
				i++
			}
		case "--after":
			if i+1 < len(os.Args) {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil {
					after = n
				}
				i++
			}
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	result, err := storeTimeline(s, obsID, before, after)
	if err != nil {
		fatal(err)
	}

	// Session header
	if result.SessionInfo != nil {
		summary := ""
		if result.SessionInfo.Summary != nil {
			summary = fmt.Sprintf(" — %s", truncate(*result.SessionInfo.Summary, 100))
		}
		fmt.Printf("Session: %s (%s)%s\n", result.SessionInfo.Project, result.SessionInfo.StartedAt, summary)
		fmt.Printf("Total observations in session: %d\n\n", result.TotalInRange)
	}

	// Before
	if len(result.Before) > 0 {
		fmt.Println("─── Before ───")
		for _, e := range result.Before {
			fmt.Printf("  #%d [%s] %s — %s\n", e.ID, e.Type, e.Title, truncate(e.Content, 150))
		}
		fmt.Println()
	}

	// Focus
	fmt.Printf(">>> #%d [%s] %s <<<\n", result.Focus.ID, result.Focus.Type, result.Focus.Title)
	fmt.Printf("    %s\n", truncate(result.Focus.Content, 500))
	fmt.Printf("    %s\n\n", result.Focus.CreatedAt)

	// After
	if len(result.After) > 0 {
		fmt.Println("─── After ───")
		for _, e := range result.After {
			fmt.Printf("  #%d [%s] %s — %s\n", e.ID, e.Type, e.Title, truncate(e.Content, 150))
		}
	}
}

func cmdContext(cfg store.Config) {
	project := ""
	scope := ""

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--scope":
			if i+1 < len(os.Args) {
				scope = os.Args[i+1]
				i++
			}
		default:
			if project == "" {
				project = os.Args[i]
			}
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	ctx, err := storeFormatContext(s, project, scope)
	if err != nil {
		fatal(err)
	}

	if ctx == "" {
		fmt.Println("No previous session memories found.")
		return
	}

	fmt.Print(ctx)
}

func cmdStats(cfg store.Config) {
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	stats, err := storeStats(s)
	if err != nil {
		fatal(err)
	}

	projects := "none yet"
	if len(stats.Projects) > 0 {
		projects = strings.Join(stats.Projects, ", ")
	}

	fmt.Printf("Engram Memory Stats\n")
	fmt.Printf("  Sessions:     %d\n", stats.TotalSessions)
	fmt.Printf("  Observations: %d\n", stats.TotalObservations)
	fmt.Printf("  Prompts:      %d\n", stats.TotalPrompts)
	fmt.Printf("  Projects:     %s\n", projects)
	fmt.Printf("  Database:     %s/engram.db\n", cfg.DataDir)
}

func cmdExport(cfg store.Config) {
	outFile := "engram-export.json"
	if len(os.Args) > 2 {
		outFile = os.Args[2]
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	data, err := storeExport(s)
	if err != nil {
		fatal(err)
	}

	out, err := jsonMarshalIndent(data, "", "  ")
	if err != nil {
		fatal(err)
	}

	if err := os.WriteFile(outFile, out, 0644); err != nil {
		fatal(err)
	}

	fmt.Printf("Exported to %s\n", outFile)
	fmt.Printf("  Sessions:     %d\n", len(data.Sessions))
	fmt.Printf("  Observations: %d\n", len(data.Observations))
	fmt.Printf("  Prompts:      %d\n", len(data.Prompts))
}

func cmdImport(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: engram import <file.json>")
		exitFunc(1)
	}

	inFile := os.Args[2]
	raw, err := os.ReadFile(inFile)
	if err != nil {
		fatal(fmt.Errorf("read %s: %w", inFile, err))
	}

	var data store.ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		fatal(fmt.Errorf("parse %s: %w", inFile, err))
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	result, err := s.Import(&data)
	if err != nil {
		fatal(err)
	}

	fmt.Printf("Imported from %s\n", inFile)
	fmt.Printf("  Sessions:     %d\n", result.SessionsImported)
	fmt.Printf("  Observations: %d\n", result.ObservationsImported)
	fmt.Printf("  Prompts:      %d\n", result.PromptsImported)
}

func cmdSync(cfg store.Config) {
	// Parse flags
	doImport := false
	doStatus := false
	doAll := false
	project := ""
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--import":
			doImport = true
		case "--status":
			doStatus = true
		case "--all":
			doAll = true
		case "--project":
			if i+1 < len(os.Args) {
				project = os.Args[i+1]
				i++
			}
		}
	}

	// Default project using git detection (so sync only exports
	// memories for THIS project, not everything in the global DB).
	// --all skips project filtering entirely — exports everything.
	if !doAll && project == "" {
		if cwd, err := os.Getwd(); err == nil {
			project = detectProject(cwd)
		}
	}

	syncDir := ".engram"

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	sy := engramsync.NewLocal(s, syncDir)

	if doStatus {
		local, remote, pending, err := syncStatus(sy)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("Sync status:\n")
		fmt.Printf("  Local chunks:    %d\n", local)
		fmt.Printf("  Remote chunks:   %d\n", remote)
		fmt.Printf("  Pending import:  %d\n", pending)
		return
	}

	if doImport {
		result, err := syncImport(sy)
		if err != nil {
			fatal(err)
		}

		if result.ChunksImported == 0 {
			fmt.Println("No new chunks to import.")
			if result.ChunksSkipped > 0 {
				fmt.Printf("  (%d chunks already imported)\n", result.ChunksSkipped)
			}
			return
		}

		fmt.Printf("Imported %d new chunk(s) from .engram/\n", result.ChunksImported)
		fmt.Printf("  Sessions:     %d\n", result.SessionsImported)
		fmt.Printf("  Observations: %d\n", result.ObservationsImported)
		fmt.Printf("  Prompts:      %d\n", result.PromptsImported)
		if result.ChunksSkipped > 0 {
			fmt.Printf("  Skipped:      %d (already imported)\n", result.ChunksSkipped)
		}
		return
	}

	// Export: DB → new chunk
	username := engramsync.GetUsername()
	if doAll {
		fmt.Println("Exporting ALL memories (all projects)...")
	} else {
		fmt.Printf("Exporting memories for project %q...\n", project)
	}
	result, err := syncExport(sy, username, project)
	if err != nil {
		fatal(err)
	}

	if result.IsEmpty {
		if doAll {
			fmt.Println("Nothing new to sync — all memories already exported.")
		} else {
			fmt.Printf("Nothing new to sync for project %q — all memories already exported.\n", project)
		}
		return
	}

	fmt.Printf("Created chunk %s\n", result.ChunkID)
	fmt.Printf("  Sessions:     %d\n", result.SessionsExported)
	fmt.Printf("  Observations: %d\n", result.ObservationsExported)
	fmt.Printf("  Prompts:      %d\n", result.PromptsExported)
	fmt.Println()
	fmt.Println("Add to git:")
	fmt.Printf("  git add .engram/ && git commit -m \"sync engram memories\"\n")
}

// storeAdapter wraps *store.Store to satisfy obsidian.StoreReader.
// The real store.Stats() returns (*store.Stats, error); the interface expects *store.Stats.
type storeAdapter struct{ s *store.Store }

func (a *storeAdapter) Export() (*store.ExportData, error) { return a.s.Export() }
func (a *storeAdapter) Stats() *store.Stats {
	st, _ := a.s.Stats()
	return st
}

func cmdObsidianExport(cfg store.Config) {
	// Parse flags
	var (
		vault       string
		project     string
		limit       int
		since       string
		force       bool
		graphConfig string
		watch       bool
		interval    string
	)

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--vault":
			if i+1 < len(os.Args) {
				vault = os.Args[i+1]
				i++
			}
		case "--project":
			if i+1 < len(os.Args) {
				project = os.Args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(os.Args) {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil {
					limit = n
				}
				i++
			}
		case "--since":
			if i+1 < len(os.Args) {
				since = os.Args[i+1]
				i++
			}
		case "--force":
			force = true
		case "--graph-config":
			if i+1 < len(os.Args) {
				graphConfig = os.Args[i+1]
				i++
			}
		case "--watch":
			watch = true
		case "--interval":
			if i+1 < len(os.Args) {
				interval = os.Args[i+1]
				i++
			}
		default:
			fmt.Fprintf(os.Stderr, "engram: unknown flag: %s\n", os.Args[i])
			exitFunc(1)
		}
	}

	if vault == "" {
		fmt.Fprintln(os.Stderr, "error: flag --vault is required")
		exitFunc(1)
	}

	// Default --graph-config to "preserve"
	if graphConfig == "" {
		graphConfig = string(obsidian.GraphConfigPreserve)
	}

	graphMode, err := obsidian.ParseGraphConfigMode(graphConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid --graph-config value: %s (accepted: preserve, force, skip)\n", graphConfig)
		exitFunc(1)
	}

	// Validate --interval requires --watch
	if interval != "" && !watch {
		fmt.Fprintln(os.Stderr, "error: --interval requires --watch")
		exitFunc(1)
	}

	// Parse and validate --interval (default 10m when --watch is set)
	var watchInterval time.Duration
	if watch {
		intervalStr := interval
		if intervalStr == "" {
			intervalStr = "10m"
		}
		d, parseErr := time.ParseDuration(intervalStr)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "error: invalid --interval value %q: %v\n", intervalStr, parseErr)
			exitFunc(1)
		}
		if d < time.Minute {
			fmt.Fprintf(os.Stderr, "error: --interval must be at least 1m (minimum), got %v\n", d)
			exitFunc(1)
		}
		watchInterval = d
	}

	exportCfg := obsidian.ExportConfig{
		VaultPath:   vault,
		Project:     project,
		Limit:       limit,
		Force:       force,
		GraphConfig: graphMode,
	}

	if since != "" {
		// Try common date formats: full RFC3339, date-only (YYYY-MM-DD)
		var sinceTime time.Time
		var parseErr error
		for _, layout := range []string{time.RFC3339, "2006-01-02"} {
			sinceTime, parseErr = time.Parse(layout, since)
			if parseErr == nil {
				break
			}
		}
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "error: invalid --since value %q (expected YYYY-MM-DD or RFC3339)\n", since)
			exitFunc(1)
		}
		exportCfg.Since = sinceTime
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	exp := newObsidianExporter(&storeAdapter{s: s}, exportCfg)

	if watch {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		w := newObsidianWatcher(obsidian.WatcherConfig{
			Exporter: exp,
			Interval: watchInterval,
			Logf:     log.Printf,
		})

		if w != nil {
			if runErr := w.Run(ctx); runErr != nil {
				log.Printf("[engram] shutting down watch mode: %v", runErr)
			} else {
				log.Printf("[engram] shutting down watch mode")
			}
		}
		exitFunc(0)
		return
	}

	result, err := exp.Export()
	if err != nil {
		fatal(err)
	}

	fmt.Printf("Obsidian export complete\n")
	fmt.Printf("  Created: %d\n", result.Created)
	fmt.Printf("  Updated: %d\n", result.Updated)
	fmt.Printf("  Deleted: %d\n", result.Deleted)
	fmt.Printf("  Skipped: %d\n", result.Skipped)
	fmt.Printf("  Hubs:    %d\n", result.HubsCreated)
	if len(result.Errors) > 0 {
		fmt.Fprintf(os.Stderr, "  Errors: %d\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "    - %v\n", e)
		}
	}
}

func cmdProjects(cfg store.Config) {
	// Route: engram projects list | engram projects consolidate [--all] [--dry-run]
	subCmd := "list"
	if len(os.Args) > 2 {
		subCmd = os.Args[2]
	}
	switch subCmd {
	case "consolidate":
		cmdProjectsConsolidate(cfg)
	case "prune":
		cmdProjectsPrune(cfg)
	case "list", "":
		cmdProjectsList(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown projects subcommand: %s\n", subCmd)
		fmt.Fprintln(os.Stderr, "usage: engram projects list")
		fmt.Fprintln(os.Stderr, "       engram projects consolidate [--all] [--dry-run]")
		fmt.Fprintln(os.Stderr, "       engram projects prune [--dry-run]")
		exitFunc(1)
	}
}

func cmdProjectsList(cfg store.Config) {
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	projects, err := s.ListProjectsWithStats()
	if err != nil {
		fatal(err)
	}

	if len(projects) == 0 {
		fmt.Println("No projects found.")
		return
	}

	fmt.Printf("Projects (%d):\n", len(projects))
	for _, p := range projects {
		sessionWord := "sessions"
		if p.SessionCount == 1 {
			sessionWord = "session"
		}
		promptWord := "prompts"
		if p.PromptCount == 1 {
			promptWord = "prompt"
		}
		fmt.Printf("  %-30s %4d obs   %3d %-9s  %3d %s\n",
			p.Name,
			p.ObservationCount,
			p.SessionCount, sessionWord,
			p.PromptCount, promptWord,
		)
	}
}

// projectGroup represents a set of project names that should be merged.
type projectGroup struct {
	Names     []string
	Canonical string // suggested canonical (most observations)
}

// groupSimilarProjects groups projects by name similarity and shared directories.
// Uses a simple union-find approach.
func groupSimilarProjects(projects []store.ProjectStats) []projectGroup {
	n := len(projects)
	if n == 0 {
		return nil
	}

	// parent[i] holds the root of i's component
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}

	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(x, y int) {
		rx, ry := find(x), find(y)
		if rx != ry {
			parent[rx] = ry
		}
	}

	// Build name-only slice and index map for FindSimilar
	names := make([]string, n)
	nameToIndex := make(map[string]int, n)
	for i, p := range projects {
		names[i] = p.Name
		nameToIndex[p.Name] = i
	}

	// Group by name similarity
	for i := 0; i < n; i++ {
		similar := project.FindSimilar(projects[i].Name, names, 3)
		for _, sm := range similar {
			if j, ok := nameToIndex[sm.Name]; ok {
				union(i, j)
			}
		}
	}

	// Group by shared directory
	dirToProjects := make(map[string][]int)
	for i, p := range projects {
		for _, dir := range p.Directories {
			if dir != "" {
				dirToProjects[dir] = append(dirToProjects[dir], i)
			}
		}
	}
	for _, idxs := range dirToProjects {
		for k := 1; k < len(idxs); k++ {
			union(idxs[0], idxs[k])
		}
	}

	// Collect components
	components := make(map[int][]int)
	for i := 0; i < n; i++ {
		root := find(i)
		components[root] = append(components[root], i)
	}

	// Build groups — skip singletons (no duplicates)
	var groups []projectGroup
	for _, idxs := range components {
		if len(idxs) < 2 {
			continue
		}
		// Suggest the one with most observations as canonical
		bestIdx := idxs[0]
		for _, idx := range idxs[1:] {
			if projects[idx].ObservationCount > projects[bestIdx].ObservationCount {
				bestIdx = idx
			}
		}
		grpNames := make([]string, len(idxs))
		for k, idx := range idxs {
			grpNames[k] = projects[idx].Name
		}
		sort.Strings(grpNames)
		groups = append(groups, projectGroup{
			Names:     grpNames,
			Canonical: projects[bestIdx].Name,
		})
	}
	// Sort groups by canonical name for deterministic output
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Canonical < groups[j].Canonical
	})
	return groups
}

func cmdProjectsConsolidate(cfg store.Config) {
	doAll := false
	dryRun := false
	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--all":
			doAll = true
		case "--dry-run":
			dryRun = true
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	if !doAll {
		// Single-project mode: detect canonical project for cwd, find variants
		cwd, err := os.Getwd()
		if err != nil {
			fatal(err)
		}
		canonical := detectProject(cwd)

		allNames, err := s.ListProjectNames()
		if err != nil {
			fatal(err)
		}

		// Check if the detected canonical actually exists in the DB.
		canonicalExists := false
		for _, n := range allNames {
			if n == canonical {
				canonicalExists = true
				break
			}
		}
		if !canonicalExists {
			fmt.Printf("Note: %q has no existing memories. Merging will move memories into this new project name.\n", canonical)
		}

		// Find candidates by name similarity
		similar := project.FindSimilar(canonical, allNames, 3)

		// Also find candidates by shared directory (catches renames like sdd-agent-team → agent-teams-lite)
		allStats, _ := s.ListProjectsWithStats()
		statsMap := make(map[string]store.ProjectStats)
		var cwdDirs []string // directories for the canonical project
		for _, ps := range allStats {
			statsMap[ps.Name] = ps
			if ps.Name == canonical {
				cwdDirs = ps.Directories
			}
		}
		// If canonical has no stats yet, use cwd as its directory
		if len(cwdDirs) == 0 {
			cwdDirs = []string{cwd}
		}
		// Find projects sharing a directory with the canonical
		similarNames := make(map[string]bool)
		for _, sm := range similar {
			similarNames[sm.Name] = true
		}
		for _, ps := range allStats {
			if ps.Name == canonical || similarNames[ps.Name] {
				continue
			}
			for _, d := range ps.Directories {
				for _, cd := range cwdDirs {
					if d == cd {
						similar = append(similar, project.ProjectMatch{
							Name:      ps.Name,
							MatchType: "shared directory",
						})
						similarNames[ps.Name] = true
					}
				}
			}
		}

		if len(similar) == 0 {
			fmt.Printf("No similar project names found for %q. Nothing to consolidate.\n", canonical)
			return
		}

		fmt.Printf("Detected project: %q\n\n", canonical)
		fmt.Printf("Found similar project names:\n")
		for i, sm := range similar {
			obs := 0
			if ps, ok := statsMap[sm.Name]; ok {
				obs = ps.ObservationCount
			}
			fmt.Printf("  [%d] %-30s %3d obs  (%s)\n", i+1, sm.Name, obs, sm.MatchType)
		}

		if dryRun {
			fmt.Printf("\n[dry-run] Would merge %d project(s) into %q\n", len(similar), canonical)
			return
		}

		fmt.Printf("\nSelect which to merge into %q (comma-separated numbers, 'all', or 'none'): ", canonical)
		var answer string
		scanInputLine(&answer)
		answer = strings.TrimSpace(strings.ToLower(answer))

		if answer == "none" || answer == "n" || answer == "" {
			fmt.Println("Cancelled.")
			return
		}

		var sources []string
		if answer == "all" || answer == "a" {
			for _, sm := range similar {
				sources = append(sources, sm.Name)
			}
		} else {
			// Parse comma-separated indices
			for _, part := range strings.Split(answer, ",") {
				part = strings.TrimSpace(part)
				idx := 0
				if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 1 || idx > len(similar) {
					fmt.Fprintf(os.Stderr, "Invalid selection: %q (expected 1-%d)\n", part, len(similar))
					return
				}
				sources = append(sources, similar[idx-1].Name)
			}
		}

		if len(sources) == 0 {
			fmt.Println("Nothing selected.")
			return
		}

		fmt.Printf("\nMerging %d project(s) into %q...\n", len(sources), canonical)
		result, err := s.MergeProjects(sources, canonical)
		if err != nil {
			fatal(err)
		}

		fmt.Printf("Done! Merged into %q:\n", result.Canonical)
		fmt.Printf("  Observations: %d\n", result.ObservationsUpdated)
		fmt.Printf("  Sessions:     %d\n", result.SessionsUpdated)
		fmt.Printf("  Prompts:      %d\n", result.PromptsUpdated)
		return
	}

	// --all mode: group all projects by similarity + shared directories
	projects, err := s.ListProjectsWithStats()
	if err != nil {
		fatal(err)
	}

	groups := groupSimilarProjects(projects)

	if len(groups) == 0 {
		fmt.Println("No similar project name groups found.")
		return
	}

	fmt.Printf("Found %d group(s) of similar project names:\n\n", len(groups))

	// Build stats map for obs counts
	projectStatsMap := make(map[string]store.ProjectStats)
	for _, p := range projects {
		projectStatsMap[p.Name] = p
	}

	for i, g := range groups {
		fmt.Printf("Group %d:\n", i+1)
		for j, name := range g.Names {
			obs := 0
			if ps, ok := projectStatsMap[name]; ok {
				obs = ps.ObservationCount
			}
			marker := "  "
			if name == g.Canonical {
				marker = "→ "
			}
			fmt.Printf("  %s[%d] %-30s %3d obs\n", marker, j+1, name, obs)
		}
		fmt.Printf("  Suggested canonical: %q (→)\n", g.Canonical)

		if dryRun {
			fmt.Printf("  [dry-run] Would merge into %q\n\n", g.Canonical)
			continue
		}

		fmt.Printf("\n  Options:\n")
		fmt.Printf("    all     — merge everything into %q\n", g.Canonical)
		fmt.Printf("    1,3,... — merge only selected numbers into %q\n", g.Canonical)
		fmt.Printf("    rename  — choose a different canonical name\n")
		fmt.Printf("    skip    — don't touch this group\n")
		fmt.Printf("  Choice: ")
		var answer string
		scanInputLine(&answer)
		answer = strings.TrimSpace(strings.ToLower(answer))

		canonical := g.Canonical

		if answer == "skip" || answer == "s" || answer == "n" || answer == "" {
			fmt.Println("  Skipped.")
			fmt.Println()
			continue
		}

		if answer == "rename" || answer == "r" {
			fmt.Printf("  Enter canonical name: ")
			scanInputLine(&canonical)
			canonical = strings.TrimSpace(canonical)
			if canonical == "" {
				fmt.Println("  Empty input, skipping.")
				fmt.Println()
				continue
			}
			answer = "all" // after rename, merge everything into the new name
		}

		// Determine which sources to merge
		var sources []string
		if answer == "all" || answer == "a" || answer == "y" || answer == "yes" {
			for _, name := range g.Names {
				if name != canonical {
					sources = append(sources, name)
				}
			}
		} else {
			// Parse comma-separated indices
			for _, part := range strings.Split(answer, ",") {
				part = strings.TrimSpace(part)
				idx := 0
				if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 1 || idx > len(g.Names) {
					fmt.Fprintf(os.Stderr, "  Invalid selection: %q (expected 1-%d)\n", part, len(g.Names))
					fmt.Println()
					continue
				}
				selected := g.Names[idx-1]
				if selected != canonical {
					sources = append(sources, selected)
				}
			}
		}
		if len(sources) == 0 {
			fmt.Println("  Nothing to merge.")
			fmt.Println()
			continue
		}

		result, err := s.MergeProjects(sources, canonical)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error merging: %v\n", err)
			fmt.Println()
			continue
		}
		fmt.Printf("  Merged: %d obs, %d sessions, %d prompts\n\n",
			result.ObservationsUpdated, result.SessionsUpdated, result.PromptsUpdated)
	}
}

func cmdProjectsPrune(cfg store.Config) {
	dryRun := false
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--dry-run" {
			dryRun = true
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	allStats, err := s.ListProjectsWithStats()
	if err != nil {
		fatal(err)
	}

	// Find projects with 0 observations
	var candidates []store.ProjectStats
	for _, ps := range allStats {
		if ps.ObservationCount == 0 {
			candidates = append(candidates, ps)
		}
	}

	if len(candidates) == 0 {
		fmt.Println("No empty projects to prune.")
		return
	}

	fmt.Printf("Found %d project(s) with 0 observations:\n\n", len(candidates))
	for i, ps := range candidates {
		fmt.Printf("  [%d] %-30s %3d sessions  %3d prompts\n", i+1, ps.Name, ps.SessionCount, ps.PromptCount)
	}

	if dryRun {
		fmt.Printf("\n[dry-run] Would prune %d project(s)\n", len(candidates))
		return
	}

	fmt.Printf("\nSelect which to prune (comma-separated numbers, 'all', or 'none'): ")
	var answer string
	scanInputLine(&answer)
	answer = strings.TrimSpace(strings.ToLower(answer))

	if answer == "none" || answer == "n" || answer == "" {
		fmt.Println("Cancelled.")
		return
	}

	var selected []store.ProjectStats
	if answer == "all" || answer == "a" {
		selected = candidates
	} else {
		for _, part := range strings.Split(answer, ",") {
			part = strings.TrimSpace(part)
			idx := 0
			if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 1 || idx > len(candidates) {
				fmt.Fprintf(os.Stderr, "Invalid selection: %q (expected 1-%d)\n", part, len(candidates))
				return
			}
			selected = append(selected, candidates[idx-1])
		}
	}

	if len(selected) == 0 {
		fmt.Println("Nothing selected.")
		return
	}

	totalSessions := int64(0)
	totalPrompts := int64(0)
	for _, ps := range selected {
		result, err := s.PruneProject(ps.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error pruning %q: %v\n", ps.Name, err)
			continue
		}
		totalSessions += result.SessionsDeleted
		totalPrompts += result.PromptsDeleted
	}

	fmt.Printf("\nPruned %d project(s): %d sessions, %d prompts removed.\n", len(selected), totalSessions, totalPrompts)
}

func cmdSetup() {
	agents := setupSupportedAgents()

	// If agent name given directly: engram setup opencode
	if len(os.Args) > 2 && !strings.HasPrefix(os.Args[2], "-") {
		result, err := setupInstallAgent(os.Args[2])
		if err != nil {
			fatal(err)
		}
		fmt.Printf("✓ Installed %s plugin (%d files)\n", result.Agent, result.Files)
		fmt.Printf("  → %s\n", result.Destination)
		printPostInstall(result.Agent)
		return
	}

	// Interactive selection
	fmt.Println("engram setup — Install agent plugin")
	fmt.Println()
	fmt.Println("Which agent do you want to set up?")
	fmt.Println()

	for i, a := range agents {
		fmt.Printf("  [%d] %s\n", i+1, a.Description)
		fmt.Printf("      Install to: %s\n\n", a.InstallDir)
	}

	fmt.Print("Enter choice (1-", len(agents), "): ")
	var input string
	scanInputLine(&input)

	choice, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || choice < 1 || choice > len(agents) {
		fmt.Fprintln(os.Stderr, "Invalid choice.")
		exitFunc(1)
	}

	selected := agents[choice-1]
	fmt.Printf("\nInstalling %s plugin...\n", selected.Name)

	result, err := setupInstallAgent(selected.Name)
	if err != nil {
		fatal(err)
	}

	fmt.Printf("✓ Installed %s plugin (%d files)\n", result.Agent, result.Files)
	fmt.Printf("  → %s\n", result.Destination)
	printPostInstall(result.Agent)
}

func printPostInstall(agent string) {
	switch agent {
	case "opencode":
		fmt.Println("\nNext steps:")
		fmt.Println("  1. Restart OpenCode — plugin + MCP server are ready")
		fmt.Println("  2. Run 'engram serve &' for session tracking (HTTP API)")
	case "claude-code":
		// Offer to add engram tools to the permissions allowlist
		fmt.Print("\nAdd engram tools to ~/.claude/settings.json allowlist?\n")
		fmt.Print("This prevents Claude Code from asking permission on every tool call.\n")
		fmt.Print("Add to allowlist? (y/N): ")
		var answer string
		scanInputLine(&answer)
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer == "y" || answer == "yes" {
			if err := setupAddClaudeCodeAllowlist(); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: could not update allowlist: %v\n", err)
				fmt.Fprintln(os.Stderr, "  You can add them manually to permissions.allow in ~/.claude/settings.json")
			} else {
				fmt.Println("  ✓ Engram tools added to allowlist")
			}
		} else {
			fmt.Println("  Skipped. You can add them later to permissions.allow in ~/.claude/settings.json")
		}

		fmt.Println("\nNext steps:")
		fmt.Println("  1. Restart Claude Code — the plugin is active immediately")
		fmt.Println("  2. Verify with: claude plugin list")
		fmt.Println("  3. MCP config written to ~/.claude/mcp/engram.json using absolute binary path")
		fmt.Println("     (survives plugin auto-updates; re-run 'engram setup claude-code' if you move the binary)")
	case "gemini-cli":
		fmt.Println("\nNext steps:")
		fmt.Println("  1. Restart Gemini CLI so MCP config is reloaded")
		fmt.Println("  2. Verify ~/.gemini/settings.json includes mcpServers.engram")
		fmt.Println("  3. Verify ~/.gemini/system.md + ~/.gemini/.env exist for compaction recovery")
	case "codex":
		fmt.Println("\nNext steps:")
		fmt.Println("  1. Restart Codex so MCP config is reloaded")
		fmt.Println("  2. Verify ~/.codex/config.toml has [mcp_servers.engram]")
		fmt.Println("  3. Verify model_instructions_file + experimental_compact_prompt_file are set")
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func printUsage() {
	fmt.Printf(`engram v%s — Persistent memory for AI coding agents

Usage:
  engram <command> [arguments]

Commands:
  serve [port]       Start HTTP API server (default: 7437)
  mcp [--tools=PROFILE] [--project=NAME]
                     Start MCP server (stdio transport, for any AI agent)
                       Profiles: agent (11 tools), admin (4 tools), all (default, 15)
                       Combine: --tools=agent,admin or pick individual tools
                       --project  Override detected project name (default: git remote → cwd)
                       Example: engram mcp --tools=agent
  tui                Launch interactive terminal UI
  search <query>     Search memories [--type TYPE] [--project PROJECT] [--scope SCOPE] [--limit N]
  save <title> <msg> Save a memory  [--type TYPE] [--project PROJECT] [--scope SCOPE]
  timeline <obs_id>  Show chronological context around an observation [--before N] [--after N]
  context [project]  Show recent context from previous sessions
  stats              Show memory system statistics
  export [file]      Export all memories to JSON (default: engram-export.json)
  import <file>      Import memories from a JSON export file
  projects list      List all projects with observation, session, and prompt counts
  projects consolidate [--all] [--dry-run]
                     Merge similar project names into one canonical name
                       --all      Scan ALL projects for similar name groups
                       --dry-run  Preview what would be merged (no changes)
  cloud <command>    Cloud sync management
                       setup      Configure cloud server connection
                       sync       Push local changes and pull remote changes
                       status     Show cloud sync status
                       enroll     Enroll a project for cloud sync
                       unenroll   Remove a project from cloud sync
  setup [agent]      Install/setup agent integration (opencode, claude-code, gemini-cli, codex)
  sync               Export new memories as compressed chunk to .engram/
                       --import   Import new chunks from .engram/ into local DB
                       --status   Show sync status (local vs remote chunks)
                       --project  Filter export to a specific project
                       --all      Export ALL projects (ignore directory-based filter)
  obsidian-export    Export memories to an Obsidian-compatible markdown vault
                       --vault         Path to Obsidian vault root (required)
                       --project       Filter export to a single project (optional)
                       --limit         Cap exported observations at N (optional)
                       --since         Export only observations after this date, e.g. 2026-01-01 (optional)
                       --force         Ignore incremental state, full re-export (optional)
                       --graph-config  Graph layout mode: preserve|force|skip (default: preserve)
                       --watch         Enable auto-sync mode (runs on interval until Ctrl+C)
                       --interval      Sync interval for --watch mode (default: 10m, minimum: 1m)

  version            Print version
  help               Show this help

Environment:
  ENGRAM_DATA_DIR    Override data directory (default: ~/.engram)
  ENGRAM_PORT        Override HTTP server port (default: 7437)
  ENGRAM_PROJECT     Override auto-detected project name for MCP server

MCP Configuration (add to your agent's config):
  {
    "mcp": {
      "engram": {
        "type": "stdio",
        "command": "engram",
        "args": ["mcp", "--tools=agent"]
      }
    }
  }
`, version)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "engram: %s\n", err)
	exitFunc(1)
}

// resolveHomeFallback tries platform-specific environment variables to find
// a home directory when os.UserHomeDir() fails. This commonly happens on
// Windows when engram is launched as an MCP subprocess without full env
// propagation.
func resolveHomeFallback() string {
	// Windows: try common env vars that might be set even when
	// %USERPROFILE% is missing.
	for _, env := range []string{"USERPROFILE", "HOME", "LOCALAPPDATA"} {
		if v := os.Getenv(env); v != "" {
			if env == "LOCALAPPDATA" {
				// LOCALAPPDATA is C:\Users\<user>\AppData\Local — go up two levels.
				parent := filepath.Dir(filepath.Dir(v))
				if parent != "." && parent != v {
					return parent
				}
			}
			return v
		}
	}

	// Unix: $HOME should always work, but try passwd-style fallback.
	if v := os.Getenv("HOME"); v != "" {
		return v
	}

	return ""
}

// migrateOrphanedDB checks for engram databases that ended up in wrong
// locations (e.g. drive root on Windows when UserHomeDir failed silently)
// and moves them to the correct location if the correct location has no DB.
func migrateOrphanedDB(correctDir string) {
	correctDB := filepath.Join(correctDir, "engram.db")

	// If the correct DB already exists, nothing to migrate.
	if _, err := os.Stat(correctDB); err == nil {
		return
	}

	// Known wrong locations: relative ".engram" resolved from common roots.
	// On Windows this typically ends up at C:\.engram or D:\.engram.
	candidates := []string{
		filepath.Join(string(filepath.Separator), ".engram", "engram.db"),
	}

	// On Windows, check all drive letter roots.
	if filepath.Separator == '\\' {
		for _, drive := range "CDEFGHIJ" {
			candidates = append(candidates,
				filepath.Join(string(drive)+":\\", ".engram", "engram.db"),
			)
		}
	}

	for _, candidate := range candidates {
		if candidate == correctDB {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}

		// Found an orphaned DB — migrate it.
		log.Printf("[engram] found orphaned database at %s, migrating to %s", candidate, correctDB)

		if err := os.MkdirAll(correctDir, 0755); err != nil {
			log.Printf("[engram] migration failed (create dir): %v", err)
			return
		}

		// Move DB and WAL/SHM files if they exist.
		for _, suffix := range []string{"", "-wal", "-shm"} {
			src := candidate + suffix
			dst := correctDB + suffix
			if _, statErr := os.Stat(src); statErr != nil {
				continue
			}
			if renameErr := os.Rename(src, dst); renameErr != nil {
				log.Printf("[engram] migration failed (move %s): %v", filepath.Base(src), renameErr)
				return
			}
		}

		// Clean up empty orphaned directory.
		orphanDir := filepath.Dir(candidate)
		entries, _ := os.ReadDir(orphanDir)
		if len(entries) == 0 {
			os.Remove(orphanDir)
		}

		log.Printf("[engram] migration complete — memories recovered")
		return
	}
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
