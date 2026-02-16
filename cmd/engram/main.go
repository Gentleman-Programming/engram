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
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/alanbuscaglia/engram/internal/mcp"
	"github.com/alanbuscaglia/engram/internal/server"
	"github.com/alanbuscaglia/engram/internal/store"
	engramsync "github.com/alanbuscaglia/engram/internal/sync"
	"github.com/alanbuscaglia/engram/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// version is set via ldflags at build time by goreleaser.
// Falls back to "dev" for local builds.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cfg := store.DefaultConfig()

	// Allow overriding data dir via env
	if dir := os.Getenv("ENGRAM_DATA_DIR"); dir != "" {
		cfg.DataDir = dir
	}

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
	case "version", "--version", "-v":
		fmt.Printf("engram %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
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

	s, err := store.New(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	srv := server.New(s, port)
	if err := srv.Start(); err != nil {
		fatal(err)
	}
}

func cmdMCP(cfg store.Config) {
	s, err := store.New(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	mcpSrv := mcp.NewServer(s)
	if err := mcpserver.ServeStdio(mcpSrv); err != nil {
		fatal(err)
	}
}

func cmdTUI(cfg store.Config) {
	s, err := store.New(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	model := tui.New(s)
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		fatal(err)
	}
}

func cmdSearch(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: engram search <query> [--type TYPE] [--project PROJECT] [--limit N]")
		os.Exit(1)
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
		default:
			queryParts = append(queryParts, os.Args[i])
		}
	}

	query := strings.Join(queryParts, " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "error: search query is required")
		os.Exit(1)
	}

	s, err := store.New(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	results, err := s.Search(query, opts)
	if err != nil {
		fatal(err)
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
		fmt.Printf("[%d] #%d (%s) — %s\n    %s\n    %s%s\n\n",
			i+1, r.ID, r.Type, r.Title,
			truncate(r.Content, 300),
			r.CreatedAt, project)
	}
}

func cmdSave(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: engram save <title> <content> [--type TYPE] [--project PROJECT]")
		os.Exit(1)
	}

	title := os.Args[2]
	content := os.Args[3]
	typ := "manual"
	project := ""

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
		}
	}

	s, err := store.New(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	s.CreateSession("manual-save", project, "")
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "manual-save",
		Type:      typ,
		Title:     title,
		Content:   content,
		Project:   project,
	})
	if err != nil {
		fatal(err)
	}

	fmt.Printf("Memory saved: #%d %q (%s)\n", id, title, typ)
}

func cmdTimeline(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: engram timeline <observation_id> [--before N] [--after N]")
		os.Exit(1)
	}

	obsID, err := strconv.ParseInt(os.Args[2], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid observation id %q\n", os.Args[2])
		os.Exit(1)
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

	s, err := store.New(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	result, err := s.Timeline(obsID, before, after)
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
	if len(os.Args) > 2 {
		project = os.Args[2]
	}

	s, err := store.New(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	ctx, err := s.FormatContext(project)
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
	s, err := store.New(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	stats, err := s.Stats()
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

	s, err := store.New(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	data, err := s.Export()
	if err != nil {
		fatal(err)
	}

	out, err := json.MarshalIndent(data, "", "  ")
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
		os.Exit(1)
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

	s, err := store.New(cfg)
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
	project := ""
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--import":
			doImport = true
		case "--status":
			doStatus = true
		case "--project":
			if i+1 < len(os.Args) {
				project = os.Args[i+1]
				i++
			}
		}
	}

	syncDir := ".engram"

	s, err := store.New(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	sy := engramsync.New(s, syncDir)

	if doStatus {
		local, remote, pending, err := sy.Status()
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
		result, err := sy.Import()
		if err != nil {
			fatal(err)
		}

		if result.ChunksImported == 0 {
			fmt.Println("Already up to date — no new chunks to import.")
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
	result, err := sy.Export(username, project)
	if err != nil {
		fatal(err)
	}

	if result.IsEmpty {
		fmt.Println("Nothing new to sync — all memories already exported.")
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

// ─── Helpers ─────────────────────────────────────────────────────────────────

func printUsage() {
	fmt.Printf(`engram v%s — Persistent memory for AI coding agents

Usage:
  engram <command> [arguments]

Commands:
  serve [port]       Start HTTP API server (default: 7437)
  mcp                Start MCP server (stdio transport, for any AI agent)
  tui                Launch interactive terminal UI
  search <query>     Search memories [--type TYPE] [--project PROJECT] [--limit N]
  save <title> <msg> Save a memory  [--type TYPE] [--project PROJECT]
  timeline <obs_id>  Show chronological context around an observation [--before N] [--after N]
  context [project]  Show recent context from previous sessions
  stats              Show memory system statistics
  export [file]      Export all memories to JSON (default: engram-export.json)
  import <file>      Import memories from a JSON export file
  sync               Export new memories as compressed chunk to .engram/
                       --import   Import new chunks from .engram/ into local DB
                       --status   Show sync status (local vs remote chunks)
                       --project  Filter export to a specific project
  version            Print version
  help               Show this help

Environment:
  ENGRAM_DATA_DIR    Override data directory (default: ~/.engram)
  ENGRAM_PORT        Override HTTP server port (default: 7437)

MCP Configuration (add to your agent's config):
  {
    "mcp": {
      "engram": {
        "type": "stdio",
        "command": "engram",
        "args": ["mcp"]
      }
    }
  }
`, version)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "engram: %s\n", err)
	os.Exit(1)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
