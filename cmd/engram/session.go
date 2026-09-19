package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// Injectable store entry points for the session end command, following the
// package-var pattern used across main.go so tests can stub every store touch.
var (
	storeEndSessionStrict = func(s *store.Store, id string, summary *string) (store.SessionEndResult, error) {
		return s.EndSessionStrict(id, summary)
	}
	storeStaleOpenSessions = func(s *store.Store, now time.Time, olderThan time.Duration, project string) ([]store.StaleOpenSession, error) {
		return s.StaleOpenSessions(now, olderThan, project)
	}
	storeEndSessionsBulk = func(s *store.Store, now time.Time, olderThan time.Duration, project string) ([]string, error) {
		return s.EndSessionsBulk(now, olderThan, project)
	}
)

// sessionEndArgs carries the parsed form of one "engram session end"
// invocation. An empty sessionID selects bulk mode; any nonempty sessionID
// selects single mode.
type sessionEndArgs struct {
	sessionID  string
	summary    string
	hasSummary bool
	olderThan  time.Duration
	hasByAge   bool
	project    string
	apply      bool
	jsonOut    bool
}

// bulk reports whether the invocation targets the bulk (filter-driven) mode.
func (a sessionEndArgs) bulk() bool { return a.sessionID == "" }

// compactAgePattern matches the compact day/week duration forms ("30d", "2w")
// accepted alongside Go duration syntax.
var compactAgePattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)([dw])$`)

// parseSessionEndAge accepts Go duration syntax ("72h", "45m") plus the
// compact day/week forms ("30d", "2w") the CLI documents for staleness
// windows. d=24h, w=7d. Zero and negative windows are rejected: they select
// nothing sensible and almost always signal a typo.
func parseSessionEndAge(value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		matches := compactAgePattern.FindStringSubmatch(value)
		if matches == nil {
			return 0, fmt.Errorf("invalid duration %q (use Go syntax like 72h or compact 30d/2w)", value)
		}
		n, parseErr := strconv.ParseFloat(matches[1], 64)
		if parseErr != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", value, parseErr)
		}
		unit := 24 * time.Hour
		if matches[2] == "w" {
			unit = 7 * 24 * time.Hour
		}
		d = time.Duration(n * float64(unit))
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid duration %q: must be positive", value)
	}
	return d, nil
}

// isMissingFlagValue reports whether the token after a value-taking flag fails
// to name a usable value: it is blank or is itself a long flag. Accepting such
// a token as the value (e.g. summary="--apply") would consume the next option
// as data and run the end operation under an argument the operator never
// typed, so it is rejected with the same error as an absent value.
func isMissingFlagValue(value string) bool {
	return strings.TrimSpace(value) == "" || strings.HasPrefix(value, "--")
}

// parseSessionEndArgs validates the tokens that follow "engram session end"
// and rejects anything undocumented BEFORE the store is opened (#1084
// guarantee): silently ignoring an unsupported option such as --apply would
// let an end operation run under assumptions the operator never made. Flag
// values are held to the same bar, so a value that looks like the next flag
// (--summary --apply) errors instead of being swallowed. Bulk mode (no session
// ID) requires an explicit --by-age window (#1253): without one the staleness
// cutoff degenerates to "now" and an --apply run would end every live open
// session, while --project alone only narrows the match within a window.
func parseSessionEndArgs(args []string) (sessionEndArgs, error) {
	var parsed sessionEndArgs
	missingValue := func(flag string) error {
		return fmt.Errorf("%s requires a value", flag)
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--summary":
			if i+1 >= len(args) || isMissingFlagValue(args[i+1]) {
				return sessionEndArgs{}, missingValue("--summary")
			}
			parsed.summary = args[i+1]
			parsed.hasSummary = true
			i++
		case "--by-age":
			if i+1 >= len(args) {
				return sessionEndArgs{}, missingValue("--by-age")
			}
			d, err := parseSessionEndAge(args[i+1])
			if err != nil {
				return sessionEndArgs{}, fmt.Errorf("invalid --by-age value: %w", err)
			}
			parsed.olderThan = d
			parsed.hasByAge = true
			i++
		case "--project":
			if i+1 >= len(args) || isMissingFlagValue(args[i+1]) {
				return sessionEndArgs{}, missingValue("--project")
			}
			parsed.project = args[i+1]
			i++
		case "--apply":
			parsed.apply = true
		case "--json":
			parsed.jsonOut = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return sessionEndArgs{}, fmt.Errorf("unknown flag %q", args[i])
			}
			if parsed.sessionID != "" {
				return sessionEndArgs{}, fmt.Errorf("unexpected extra argument %q", args[i])
			}
			parsed.sessionID = args[i]
		}
	}

	if parsed.sessionID != "" {
		if parsed.hasByAge || parsed.project != "" {
			return sessionEndArgs{}, errors.New("a session ID and the bulk filters (--by-age/--project) are mutually exclusive")
		}
		if parsed.apply {
			return sessionEndArgs{}, errors.New("--apply is only valid with bulk filters")
		}
		return parsed, nil
	}
	if parsed.hasSummary {
		return sessionEndArgs{}, errors.New("--summary is only valid when ending a single session by ID")
	}
	// A staleness window is the bulk-mode seatbelt: the store query treats a
	// zero window as "everything open up to now", so ending by project name
	// alone would sweep live sessions into the batch.
	if !parsed.hasByAge {
		if parsed.project != "" {
			return sessionEndArgs{}, errors.New("bulk mode requires --by-age DURATION (--project only narrows within that window)")
		}
		return sessionEndArgs{}, errors.New("specify a session ID, or an explicit staleness window (--by-age) for bulk mode")
	}
	return parsed, nil
}

func printSessionEndUsage() {
	fmt.Fprintln(os.Stderr, "usage: engram session end <id> [--summary TEXT] [--json]")
	fmt.Fprintln(os.Stderr, "       engram session end --by-age DURATION [--project NAME] [--apply] [--json]")
	fmt.Fprintln(os.Stderr, "  End one session by ID (immediate, idempotent: an already-ended session is a no-op notice),")
	fmt.Fprintln(os.Stderr, "  or bulk-end stale open sessions older than the --by-age window (required in bulk mode).")
	fmt.Fprintln(os.Stderr, "  --project only narrows the bulk match within that window.")
	fmt.Fprintln(os.Stderr, "  Bulk runs a DRY-RUN preview by default; add --apply to end the matched sessions.")
	fmt.Fprintln(os.Stderr, "  DURATION accepts Go syntax (72h) or compact forms (30d, 2w).")
}

func cmdSession(cfg store.Config) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: engram session end <id> [--summary TEXT] [--json]")
		fmt.Fprintln(os.Stderr, "       engram session end --by-age DURATION [--project NAME] [--apply] [--json]")
		exitFunc(1)
		return
	}

	switch os.Args[2] {
	case "end":
		cmdSessionEnd(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown session command: %s\n\n", os.Args[2])
		printSessionEndUsage()
		exitFunc(1)
	}
}

func cmdSessionEnd(cfg store.Config) {
	parsed, err := parseSessionEndArgs(os.Args[3:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		printSessionEndUsage()
		exitFunc(1)
		return
	}
	if parsed.bulk() {
		cmdSessionEndBulk(cfg, parsed)
		return
	}
	cmdSessionEndSingle(cfg, parsed)
}

// cmdSessionEndSingle ends exactly one session by ID. An already-ended session
// is a normal no-op notice with exit code 0; an unknown ID is a hard error —
// the CLI path must not inherit the silent success of store.EndSession. With
// --json it emits {"id", "status", "ended_at"} built straight from the end
// result's authoritative ended_at (omitted when the result carries none), so a
// committed end always produces its JSON even if a separate post-commit read
// would fail; the not-found fatal path never emits JSON.
func cmdSessionEndSingle(cfg store.Config, parsed sessionEndArgs) {
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer func() { _ = s.Close() }()

	var summary *string
	if parsed.hasSummary {
		summary = &parsed.summary
	}
	result, err := storeEndSessionStrict(s, parsed.sessionID, summary)
	if err != nil {
		fatal(err)
		return
	}
	if result.Status == store.SessionEndStatusNotFound {
		fmt.Fprintf(os.Stderr, "error: session %q not found\n", parsed.sessionID)
		exitFunc(1)
		return
	}
	if parsed.jsonOut {
		payload := map[string]any{"id": parsed.sessionID, "status": result.Status}
		if result.EndedAt != nil {
			payload["ended_at"] = *result.EndedAt
		}
		writeSessionEndJSON(payload)
		return
	}
	switch result.Status {
	case store.SessionEndStatusEnded:
		fmt.Printf("Session %q ended\n", parsed.sessionID)
	case store.SessionEndStatusAlreadyEnded:
		fmt.Printf("Session %q already ended\n", parsed.sessionID)
	}
}

// cmdSessionEndBulk previews (dry-run, the default) or ends every open session
// matching the staleness filters. The dry-run lists what would end and mutates
// nothing; only --apply persists the ends.
func cmdSessionEndBulk(cfg store.Config, parsed sessionEndArgs) {
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer func() { _ = s.Close() }()

	now := time.Now()
	if !parsed.apply {
		stale, err := storeStaleOpenSessions(s, now, parsed.olderThan, parsed.project)
		if err != nil {
			fatal(err)
			return
		}
		if parsed.jsonOut {
			ids := make([]string, 0, len(stale))
			for _, sess := range stale {
				ids = append(ids, sess.ID)
			}
			writeSessionEndJSON(map[string]any{"dry_run": true, "would_end": ids, "count": len(ids)})
			return
		}
		fmt.Printf("DRY RUN — %d session(s) would be ended:\n", len(stale))
		for _, sess := range stale {
			fmt.Printf("  %s  project=%s  directory=%s  started=%s  last activity=%s\n", sess.ID, sess.Project, sess.Directory, sess.StartedAt, sess.LastActivity)
		}
		fmt.Println("re-run with --apply to end them")
		return
	}

	ids, err := storeEndSessionsBulk(s, now, parsed.olderThan, parsed.project)
	if err != nil {
		fatal(err)
		return
	}
	if parsed.jsonOut {
		writeSessionEndJSON(map[string]any{"ended": ids, "count": len(ids)})
		return
	}
	fmt.Printf("Ended %d session(s):\n", len(ids))
	for _, id := range ids {
		fmt.Printf("  %s\n", id)
	}
}

func writeSessionEndJSON(value any) {
	out, err := jsonMarshalIndent(value, "", "  ")
	if err != nil {
		fatal(err)
		return
	}
	fmt.Println(string(out))
}
