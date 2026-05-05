package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/llm"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// cmdConflicts is the top-level dispatcher for `engram conflicts <subcommand>`.
// Mirrors the cloud.go pattern: switch on os.Args[2] → delegate to sub-command function.
func cmdConflicts(cfg store.Config) {
	if len(os.Args) < 3 {
		printConflictsUsage()
		exitFunc(1)
		return
	}
	switch os.Args[2] {
	case "list":
		cmdConflictsList(cfg)
	case "show":
		cmdConflictsShow(cfg)
	case "stats":
		cmdConflictsStats(cfg)
	case "scan":
		cmdConflictsScan(cfg)
	case "deferred":
		cmdConflictsDeferred(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown conflicts subcommand: %s\n", os.Args[2])
		printConflictsUsage()
		exitFunc(1)
	}
}

func printConflictsUsage() {
	fmt.Fprintln(os.Stderr, "usage: engram conflicts <subcommand> [options]")
	fmt.Fprintln(os.Stderr, "subcommands: list, show, stats, scan, deferred")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  list       [--project P]  [--status S]  [--since RFC3339]  [--limit N]")
	fmt.Fprintln(os.Stderr, "  show       <relation_id>")
	fmt.Fprintln(os.Stderr, "  stats      [--project P]")
	fmt.Fprintln(os.Stderr, "  scan       [--project P]  [--since RFC3339]  [--dry-run]  [--apply]  [--max-insert N]")
	fmt.Fprintln(os.Stderr, "             [--semantic]  [--concurrency N]  [--timeout-per-call SECONDS]")
	fmt.Fprintln(os.Stderr, "             [--max-semantic N]  [--yes]")
	fmt.Fprintln(os.Stderr, "  deferred   [--status S]  [--limit N]  [--inspect SYNC_ID]  [--replay]")
}

// resolveConflictsProject returns the explicit project if non-empty, otherwise falls
// back to detecting the project from the current working directory.
// On detection failure, writes an error to stderr and calls exitFunc(1).
func resolveConflictsProject(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot resolve cwd: %v\n", err)
		fmt.Fprintln(os.Stderr, "hint: use --project to specify the project explicitly")
		exitFunc(1)
	}
	detected := detectProject(cwd)
	if detected == "" {
		fmt.Fprintln(os.Stderr, "error: could not detect project from cwd")
		fmt.Fprintln(os.Stderr, "hint: use --project to specify the project explicitly")
		exitFunc(1)
	}
	return detected
}

// ─── list ─────────────────────────────────────────────────────────────────────

func cmdConflictsList(cfg store.Config) {
	args := os.Args[3:]

	var projectFlag, statusFlag, sinceFlag string
	limit := 50

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		case "--status":
			if i+1 < len(args) {
				statusFlag = args[i+1]
				i++
			}
		case "--since":
			if i+1 < len(args) {
				sinceFlag = args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					limit = n
				}
				i++
			}
		}
	}

	proj := resolveConflictsProject(projectFlag)

	var sinceTime time.Time
	if sinceFlag != "" {
		t, err := time.Parse(time.RFC3339, sinceFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: --since must be RFC3339 format: %v\n", err)
			exitFunc(1)
			return
		}
		sinceTime = t
	}

	if limit > 500 {
		fmt.Fprintln(os.Stderr, "error: --limit cannot exceed 500")
		exitFunc(1)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	opts := store.ListRelationsOptions{
		Project:   proj,
		Status:    statusFlag,
		SinceTime: sinceTime,
		Limit:     limit,
	}

	items, err := s.ListRelations(opts)
	if err != nil {
		fatal(err)
		return
	}

	total, err := s.CountRelations(opts)
	if err != nil {
		fatal(err)
		return
	}

	fmt.Printf("Conflicts List (project: %s)\n", proj)
	fmt.Printf("  Total:  %d\n", total)
	fmt.Printf("  Showing: %d\n", len(items))
	if len(items) == 0 {
		fmt.Println("  No relations found.")
		return
	}
	fmt.Println()
	for _, item := range items {
		fmt.Printf("  id:             %d\n", item.ID)
		fmt.Printf("  sync_id:        %s\n", item.SyncID)
		fmt.Printf("  relation:       %s\n", item.Relation)
		fmt.Printf("  judgment_status: %s\n", item.JudgmentStatus)
		fmt.Printf("  source:         %s — %s\n", item.SourceID, truncate(item.SourceTitle, 60))
		fmt.Printf("  target:         %s — %s\n", item.TargetID, truncate(item.TargetTitle, 60))
		fmt.Printf("  created_at:     %s\n", item.CreatedAt)
		fmt.Println()
	}
}

// ─── show ─────────────────────────────────────────────────────────────────────

func cmdConflictsShow(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: engram conflicts show <relation_id>")
		exitFunc(1)
		return
	}

	idStr := strings.TrimSpace(os.Args[3])
	relID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: relation_id must be an integer: %v\n", err)
		exitFunc(1)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	// Scan via ListRelations (no filter, no limit) and find by integer ID.
	// Phase 4 hook: add GetRelationByID to store for O(log N) lookup.
	items, err := s.ListRelations(store.ListRelationsOptions{Limit: 0})
	if err != nil {
		fatal(err)
		return
	}

	var found *store.RelationListItem
	for i := range items {
		if items[i].ID == relID {
			found = &items[i]
			break
		}
	}

	if found == nil {
		fmt.Fprintf(os.Stderr, "error: relation %d not found\n", relID)
		exitFunc(1)
		return
	}

	fmt.Printf("Conflict Detail\n")
	fmt.Printf("  relation_id:     %d\n", found.ID)
	fmt.Printf("  sync_id:         %s\n", found.SyncID)
	fmt.Printf("  relation:        %s\n", found.Relation)
	fmt.Printf("  judgment_status: %s\n", found.JudgmentStatus)
	fmt.Printf("  created_at:      %s\n", found.CreatedAt)
	fmt.Printf("  updated_at:      %s\n", found.UpdatedAt)
	fmt.Println()
	fmt.Printf("  source_id:       %s\n", found.SourceID)
	fmt.Printf("  source_title:    %s\n", found.SourceTitle)
	fmt.Println()
	fmt.Printf("  target_id:       %s\n", found.TargetID)
	fmt.Printf("  target_title:    %s\n", found.TargetTitle)
}

// ─── stats ────────────────────────────────────────────────────────────────────

func cmdConflictsStats(cfg store.Config) {
	args := os.Args[3:]

	var projectFlag string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		}
	}

	proj := resolveConflictsProject(projectFlag)

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	stats, err := s.GetRelationStats(proj)
	if err != nil {
		fatal(err)
		return
	}

	fmt.Printf("Conflicts Stats (project: %s)\n", proj)
	fmt.Println()

	if len(stats.ByJudgmentStatus) == 0 {
		fmt.Println("  No relations found.")
	} else {
		fmt.Println("  By judgment_status:")
		// Print in a stable order: pending, accepted, rejected, then others.
		for _, status := range []string{"pending", "accepted", "rejected"} {
			if n, ok := stats.ByJudgmentStatus[status]; ok {
				fmt.Printf("    %-12s %d\n", status+":", n)
			}
		}
		for status, n := range stats.ByJudgmentStatus {
			if status != "pending" && status != "accepted" && status != "rejected" {
				fmt.Printf("    %-12s %d\n", status+":", n)
			}
		}
	}

	if len(stats.ByRelation) > 0 {
		fmt.Println()
		fmt.Println("  By relation type:")
		for rel, n := range stats.ByRelation {
			fmt.Printf("    %-20s %d\n", rel+":", n)
		}
	}

	fmt.Println()
	fmt.Printf("  Deferred:    %d\n", stats.DeferredCount)
	fmt.Printf("  Dead:        %d\n", stats.DeadCount)
}

// ─── scan ─────────────────────────────────────────────────────────────────────

func cmdConflictsScan(cfg store.Config) {
	args := os.Args[3:]

	var projectFlag, sinceFlag string
	dryRun := true // default
	apply := false
	maxInsert := 100

	// Phase 4 semantic flags (parsed here; wired into ScanOptions below).
	semantic := false
	concurrency := 5
	timeoutPerCall := 60
	yesFlag := false
	maxSemantic := 100

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		case "--since":
			if i+1 < len(args) {
				sinceFlag = args[i+1]
				i++
			}
		case "--dry-run":
			dryRun = true
			apply = false
		case "--apply":
			apply = true
			dryRun = false
		case "--max-insert":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					maxInsert = n
				}
				i++
			}
		case "--semantic":
			semantic = true
		case "--concurrency":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					concurrency = n
				}
				i++
			}
		case "--timeout-per-call":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					timeoutPerCall = n
				}
				i++
			}
		case "--yes":
			yesFlag = true
		case "--max-semantic":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					maxSemantic = n
				}
				i++
			}
		}
	}

	// Explicit mutex enforcement
	if dryRun && apply {
		fmt.Fprintln(os.Stderr, "error: --dry-run and --apply are mutually exclusive")
		exitFunc(1)
		return
	}

	// Concurrency range validation (1–20).
	if concurrency < 1 || concurrency > 20 {
		fmt.Fprintf(os.Stderr, "error: --concurrency must be between 1 and 20; got %d\n", concurrency)
		exitFunc(1)
		return
	}

	proj := resolveConflictsProject(projectFlag)

	var sinceTime time.Time
	if sinceFlag != "" {
		t, err := time.Parse(time.RFC3339, sinceFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: --since must be RFC3339 format: %v\n", err)
			exitFunc(1)
			return
		}
		sinceTime = t
	}

	opts := store.ScanOptions{
		Project:   proj,
		Since:     sinceTime,
		Apply:     apply,
		MaxInsert: maxInsert,
	}

	// Wire semantic runner when --semantic is set.
	if semantic {
		runner, err := resolveAgentRunner()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			exitFunc(1)
			return
		}

		// Cost estimate and confirmation prompt (skipped on --yes).
		if !yesFlag {
			inToks, outToks := llm.EstimateScanCost(maxSemantic)
			fmt.Printf("Semantic scan will make up to %d LLM calls (~%d input tokens, ~%d output tokens).\n",
				maxSemantic, inToks, outToks)
			fmt.Println("Subscription users: counts against your quota.")
			fmt.Print("Continue? [y/N] ")
			var answer string
			fmt.Scanln(&answer) //nolint:errcheck
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				fmt.Println("Aborted.")
				return
			}
		}

		buildPrompt := func(a, b store.ObservationSnippet) string {
			return llm.BuildPrompt(
				llm.ObservationSnippet{ID: a.SyncID, Title: a.Title, Type: a.Type, Content: a.Content},
				llm.ObservationSnippet{ID: b.SyncID, Title: b.Title, Type: b.Type, Content: b.Content},
			)
		}

		opts.Semantic = true
		opts.Concurrency = concurrency
		opts.TimeoutPerCall = time.Duration(timeoutPerCall) * time.Second
		opts.MaxSemantic = maxSemantic
		opts.Runner = runner
		opts.BuildPrompt = buildPrompt
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	result, err := s.ScanProject(opts)
	if err != nil {
		fatal(err)
		return
	}

	fmt.Printf("Conflicts Scan (project: %s)\n", proj)
	fmt.Printf("  inspected:        %d\n", result.Inspected)
	fmt.Printf("  candidates_found: %d\n", result.CandidatesFound)
	fmt.Printf("  already_related:  %d\n", result.AlreadyRelated)
	fmt.Printf("  inserted:         %d\n", result.RelationsInserted)
	fmt.Printf("  dry_run:          %v\n", result.DryRun)

	if semantic {
		fmt.Printf("  semantic_judged:  %d\n", result.SemanticJudged)
		fmt.Printf("  semantic_skipped: %d\n", result.SemanticSkipped)
		fmt.Printf("  semantic_errors:  %d\n", result.SemanticErrors)
	}

	if result.Capped {
		if semantic {
			fmt.Printf("WARNING: max-semantic cap of %d reached — stopped early. Re-run to continue.\n", maxSemantic)
		} else {
			fmt.Printf("WARNING: max-insert cap of %d reached — stopped early. Re-run to continue.\n", maxInsert)
		}
	}
}

// ─── deferred ─────────────────────────────────────────────────────────────────

func cmdConflictsDeferred(cfg store.Config) {
	args := os.Args[3:]

	var statusFlag, inspectFlag string
	replay := false
	limit := 50

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--status":
			if i+1 < len(args) {
				statusFlag = args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					limit = n
				}
				i++
			}
		case "--inspect":
			if i+1 < len(args) {
				inspectFlag = args[i+1]
				i++
			}
		case "--replay":
			replay = true
		}
	}

	// Mutex: --inspect and --replay cannot be combined.
	if inspectFlag != "" && replay {
		fmt.Fprintln(os.Stderr, "error: --inspect and --replay are mutually exclusive")
		exitFunc(1)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	if replay {
		result, err := s.ReplayDeferred()
		if err != nil {
			fatal(err)
			return
		}
		fmt.Printf("Deferred Replay\n")
		fmt.Printf("  retried:   %d\n", result.Retried)
		fmt.Printf("  succeeded: %d\n", result.Succeeded)
		fmt.Printf("  failed:    %d\n", result.Failed)
		fmt.Printf("  dead:      %d\n", result.Dead)
		return
	}

	if inspectFlag != "" {
		row, err := s.GetDeferred(inspectFlag)
		if err != nil {
			// GetDeferred wraps sql.ErrNoRows with a "not found" message.
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			exitFunc(1)
			return
		}
		fmt.Printf("Deferred Row\n")
		fmt.Printf("  sync_id:          %s\n", row.SyncID)
		fmt.Printf("  entity:           %s\n", row.Entity)
		fmt.Printf("  apply_status:     %s\n", row.ApplyStatus)
		fmt.Printf("  retry_count:      %d\n", row.RetryCount)
		fmt.Printf("  payload_valid:    %v\n", row.PayloadValid)
		if row.PayloadValid {
			fmt.Printf("  payload:          %v\n", row.Payload)
		} else {
			fmt.Printf("  payload_raw:      %s\n", row.PayloadRaw)
		}
		if row.LastError != nil {
			fmt.Printf("  last_error:       %s\n", *row.LastError)
		}
		fmt.Printf("  first_seen_at:    %s\n", row.FirstSeenAt)
		return
	}

	// Default: list deferred rows.
	opts := store.ListDeferredOptions{
		Status: statusFlag,
		Limit:  limit,
	}
	rows, err := s.ListDeferred(opts)
	if err != nil {
		fatal(err)
		return
	}

	fmt.Printf("Deferred Queue\n")
	fmt.Printf("  Showing: %d\n", len(rows))
	if len(rows) == 0 {
		fmt.Println("  Queue is empty.")
		return
	}
	fmt.Println()
	for _, row := range rows {
		fmt.Printf("  sync_id:      %s\n", row.SyncID)
		fmt.Printf("  apply_status: %s\n", row.ApplyStatus)
		fmt.Printf("  retry_count:  %d\n", row.RetryCount)
		fmt.Printf("  first_seen_at: %s\n", row.FirstSeenAt)
		fmt.Println()
	}
}
