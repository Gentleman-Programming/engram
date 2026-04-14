package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/remote"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// Injectable vars for testing.
var (
	cloudWriter io.Writer = os.Stdout
	cloudReader io.Reader = os.Stdin

	// doCloudSync performs the actual push/pull. Injectable for testing.
	doCloudSync = defaultCloudSync
)

func cmdCloud(cfg store.Config) {
	if len(os.Args) < 3 {
		printCloudUsage()
		exitFunc(1)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	switch os.Args[2] {
	case "setup":
		cmdCloudSetup(s)
	case "sync":
		cmdCloudSync(s)
	case "status":
		cmdCloudStatus(s)
	case "enroll":
		cmdCloudEnroll(s)
	case "unenroll":
		cmdCloudUnenroll(s)
	default:
		fmt.Fprintf(os.Stderr, "unknown cloud command: %s\n\n", os.Args[2])
		printCloudUsage()
		exitFunc(1)
	}
}

// ─── T22: engram cloud setup ────────────────────────────────────────────────

func cmdCloudSetup(s *store.Store) {
	scanner := bufio.NewScanner(cloudReader)

	fmt.Fprintf(cloudWriter, "Cloud server URL: ")
	scanner.Scan()
	serverURL := strings.TrimSpace(scanner.Text())

	fmt.Fprintf(cloudWriter, "API key: ")
	scanner.Scan()
	apiKey := strings.TrimSpace(scanner.Text())

	fmt.Fprintf(cloudWriter, "Mode (cloud-only / local-sync) [local-sync]: ")
	scanner.Scan()
	mode := strings.TrimSpace(scanner.Text())
	if mode == "" {
		mode = "local-sync"
	}

	// Validate connection by hitting the health endpoint.
	client, err := remote.NewClient(serverURL, apiKey, version)
	if err != nil {
		fmt.Fprintf(cloudWriter, "Connection failed: %v\n", err)
		exitFunc(1)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err = client.Get(ctx, "/api/v1/health"); err != nil {
		fmt.Fprintf(cloudWriter, "Connection failed: %v\n", err)
		exitFunc(1)
		return
	}

	cloudCfg := remote.CloudConfig{
		ServerURL:    serverURL,
		APIKey:       apiKey,
		Mode:         mode,
		PushDebounce: 10 * time.Second,
		PullInterval: 120 * time.Second,
	}

	if err := remote.SaveToStore(s, cloudCfg); err != nil {
		fmt.Fprintf(cloudWriter, "Error saving config: %v\n", err)
		exitFunc(1)
		return
	}

	fmt.Fprintf(cloudWriter, "Connected successfully to %s (mode: %s)\n", serverURL, mode)
}

// ─── T23: engram cloud sync ─────────────────────────────────────────────────

func cmdCloudSync(s *store.Store) {
	cfg, err := remote.LoadFromStore(s)
	if err != nil {
		if errors.Is(err, remote.ErrConfigNotFound) {
			fmt.Fprintf(cloudWriter, "Cloud not configured. Run: engram cloud setup\n")
			exitFunc(1)
			return
		}
		fmt.Fprintf(cloudWriter, "Error loading config: %v\n", err)
		exitFunc(1)
		return
	}

	pushed, pulled, syncErr := doCloudSync(s, cfg)
	if syncErr != nil {
		fmt.Fprintf(cloudWriter, "Sync error: %v\n", syncErr)
	}

	fmt.Fprintf(cloudWriter, "Sync complete: pushed %d, pulled %d\n", pushed, pulled)
}

func defaultCloudSync(s *store.Store, cfg remote.CloudConfig) (int, int, error) {
	client, err := remote.NewClient(cfg.ServerURL, cfg.APIKey, version)
	if err != nil {
		return 0, 0, err
	}

	sc := remote.NewSyncClient(client, s, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pushed, pushErr := sc.PushOnce(ctx)
	pulled, pullErr := sc.PullOnce(ctx)

	if pushErr != nil {
		return pushed, pulled, pushErr
	}
	return pushed, pulled, pullErr
}

// ─── T24: engram cloud status ───────────────────────────────────────────────

func cmdCloudStatus(s *store.Store) {
	cfg, err := remote.LoadFromStore(s)
	if err != nil {
		if errors.Is(err, remote.ErrConfigNotFound) {
			fmt.Fprintf(cloudWriter, "Cloud not configured. Run: engram cloud setup\n")
			exitFunc(1)
			return
		}
		fmt.Fprintf(cloudWriter, "Error loading config: %v\n", err)
		exitFunc(1)
		return
	}

	fmt.Fprintf(cloudWriter, "Mode:      %s\n", cfg.Mode)
	fmt.Fprintf(cloudWriter, "Server:    %s\n", cfg.ServerURL)

	// Health check — never fatal, just report status.
	client, err := remote.NewClient(cfg.ServerURL, cfg.APIKey, version)
	if err != nil {
		fmt.Fprintf(cloudWriter, "Health:    error (%v)\n", err)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, healthErr := client.Get(ctx, "/api/v1/health")
		cancel()
		if healthErr != nil {
			fmt.Fprintf(cloudWriter, "Health:    unreachable\n")
		} else {
			fmt.Fprintf(cloudWriter, "Health:    connected\n")
		}
	}

	// Pending mutations count.
	mutations, err := s.ListPendingSyncMutations("cloud", 10000)
	if err != nil {
		fmt.Fprintf(cloudWriter, "Pending:   error\n")
	} else {
		fmt.Fprintf(cloudWriter, "Pending:   %d mutations\n", len(mutations))
	}

	// Last sync info.
	state, err := s.GetSyncState("cloud")
	if err != nil || state == nil || state.LastPulledSeq == 0 {
		fmt.Fprintf(cloudWriter, "Last sync: never\n")
	} else {
		fmt.Fprintf(cloudWriter, "Last sync: seq %d\n", state.LastPulledSeq)
	}

	// Enrolled projects.
	projects, err := s.ListEnrolledProjects()
	if err != nil {
		fmt.Fprintf(cloudWriter, "Projects:  error\n")
	} else if len(projects) == 0 {
		fmt.Fprintf(cloudWriter, "Projects:  none\n")
	} else {
		names := make([]string, len(projects))
		for i, p := range projects {
			names[i] = p.Project
		}
		fmt.Fprintf(cloudWriter, "Projects:  %s\n", strings.Join(names, ", "))
	}
}

// ─── T25: engram cloud enroll / unenroll ────────────────────────────────────

func cmdCloudEnroll(s *store.Store) {
	if len(os.Args) < 4 {
		fmt.Fprintf(cloudWriter, "usage: engram cloud enroll <project>\n")
		exitFunc(1)
		return
	}
	project := os.Args[3]

	enrolled, err := s.IsProjectEnrolled(project)
	if err != nil {
		fmt.Fprintf(cloudWriter, "Error: %v\n", err)
		exitFunc(1)
		return
	}
	if enrolled {
		fmt.Fprintf(cloudWriter, "Already enrolled: %s\n", project)
		return
	}

	if err := s.EnrollProject(project); err != nil {
		fmt.Fprintf(cloudWriter, "Error: %v\n", err)
		exitFunc(1)
		return
	}

	fmt.Fprintf(cloudWriter, "Enrolled: %s\n", project)
}

func cmdCloudUnenroll(s *store.Store) {
	if len(os.Args) < 4 {
		fmt.Fprintf(cloudWriter, "usage: engram cloud unenroll <project>\n")
		exitFunc(1)
		return
	}
	project := os.Args[3]

	enrolled, err := s.IsProjectEnrolled(project)
	if err != nil {
		fmt.Fprintf(cloudWriter, "Error: %v\n", err)
		exitFunc(1)
		return
	}
	if !enrolled {
		fmt.Fprintf(cloudWriter, "Not enrolled: %s\n", project)
		return
	}

	if err := s.UnenrollProject(project); err != nil {
		fmt.Fprintf(cloudWriter, "Error: %v\n", err)
		exitFunc(1)
		return
	}

	fmt.Fprintf(cloudWriter, "Unenrolled: %s\n", project)
}

// ─── Cloud Usage ────────────────────────────────────────────────────────────

func printCloudUsage() {
	fmt.Fprintf(cloudWriter, `Usage: engram cloud <command>

Commands:
  setup      Configure cloud server connection
  sync       Push local changes and pull remote changes
  status     Show cloud sync status
  enroll     Enroll a project for cloud sync
  unenroll   Remove a project from cloud sync
`)
}
