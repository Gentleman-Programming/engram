package main

// Engram <-> Pi memory bridge.
//
//   engram pi import <file.json>
//        [--dry-run] [--source NAME] [--no-dedup]
//        [--redact-secrets] [--redact-keys] [--redact-logs]
//   engram pi export [--format json] [--project PROJECT]
//   engram pi search <query> [--type TYPE] [--project PROJECT] [--scope SCOPE] [--limit N]
//
// The bridge lets Pi (and other agent harnesses) push their captured observations
// into Engram and pull them back as Pi-compatible JSON, using the same Store layer
// the rest of the CLI uses.

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// piMemoryRecord is the Pi-compatible shape for a single imported memory.
// It is intentionally tolerant: most fields are optional so Pi agents can
// export a minimal {title, content} payload or a fully-specified observation.
type piMemoryRecord struct {
	Title    string `json:"title"`
	Content  string `json:"content"`
	Type     string `json:"type,omitempty"`
	Project  string `json:"project,omitempty"`
	Scope    string `json:"scope,omitempty"`
	TopicKey string `json:"topic_key,omitempty"`
	Source   string `json:"source,omitempty"`
}

// piImportEnvelope accepts either a Pi-style {"memories": [...]} object or a
// bare JSON array of records. This makes the import path resilient to slight
// differences in how Pi (or a sub-agent) serializes its captured memories.
type piImportEnvelope struct {
	Memories []piMemoryRecord `json:"memories"`
}

// piExportEnvelope is the Pi-compatible container returned by `engram pi export`.
type piExportEnvelope struct {
	Source      string             `json:"source"`
	ExportedAt  string             `json:"exported_at"`
	Project     string             `json:"project,omitempty"`
	Observations []store.Observation `json:"observations"`
	Count       int                `json:"count"`
}

// cmdPi is the entry point for the `engram pi` subcommand tree.
func cmdPi(cfg store.Config) {
	if len(os.Args) < 3 {
		printPiUsage()
		exitFunc(1)
		return
	}
	switch os.Args[2] {
	case "import":
		cmdPiImport(cfg)
	case "export":
		cmdPiExport(cfg)
	case "search":
		cmdPiSearch(cfg)
	case "help", "--help", "-h":
		printPiUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown pi subcommand: %s\n", os.Args[2])
		printPiUsage()
		exitFunc(1)
	}
}

func printPiUsage() {
	fmt.Fprintln(os.Stderr, "usage: engram pi <subcommand> [options]")
	fmt.Fprintln(os.Stderr, "pi bridge: import, export, and search memories in Pi-compatible JSON")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  import <file.json>   Import external memories from a JSON file")
	fmt.Fprintln(os.Stderr, "                       [--dry-run] [--source NAME] [--no-dedup]")
	fmt.Fprintln(os.Stderr, "                       [--redact-secrets] [--redact-keys] [--redact-logs]")
	fmt.Fprintln(os.Stderr, "  export               Export Pi-compatible JSON to stdout")
	fmt.Fprintln(os.Stderr, "                       [--format json] [--project PROJECT]")
	fmt.Fprintln(os.Stderr, "  search <query>       Search memories, output JSON with metadata")
	fmt.Fprintln(os.Stderr, "                       [--type TYPE] [--project PROJECT] [--scope SCOPE] [--limit N]")
}

// ─── import ──────────────────────────────────────────────────────────────────

func cmdPiImport(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: engram pi import <file.json> [--dry-run] [--source NAME] [--no-dedup] [--redact-secrets] [--redact-keys] [--redact-logs]")
		exitFunc(1)
		return
	}

	inFile := os.Args[3]
	var (
		dryRun          bool
		source          string
		noDedup         bool
		redactSecrets   bool
		redactKeys      bool
		redactLogs      bool
	)
	for i := 4; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--dry-run":
			dryRun = true
		case "--source":
			if i+1 < len(os.Args) {
				source = os.Args[i+1]
				i++
			}
		case "--no-dedup":
			noDedup = true
		case "--redact-secrets":
			redactSecrets = true
		case "--redact-keys":
			redactKeys = true
		case "--redact-logs":
			redactLogs = true
		default:
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n", os.Args[i])
			exitFunc(1)
			return
		}
	}

	raw, err := os.ReadFile(inFile)
	if err != nil {
		fatal(fmt.Errorf("read %s: %w", inFile, err))
		return
	}

	records, err := parsePiImport(raw)
	if err != nil {
		fatal(fmt.Errorf("parse %s: %w", inFile, err))
		return
	}

	redactor := newPiRedactor(redactSecrets, redactKeys, redactLogs)
	for i := range records {
		records[i] = redactor.apply(records[i])
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	sessionID := "pi-import"
	if source != "" {
		sessionID = "pi-import-" + sanitizeSessionSuffix(source)
	}
	// Ensure a session row exists for the imported observations. Use a stable
	// per-source directory marker so repeated imports land in the same session.
	dirMarker := "/engram-pi-bridge"
	if err := s.CreateSession(sessionID, "", dirMarker); err != nil {
		fatal(err)
		return
	}

	var (
		imported int
		skipped  int
		failed   int
	)
	for _, r := range records {
		typ := strings.TrimSpace(r.Type)
		if typ == "" {
			typ = "discovery"
		}
		project := strings.TrimSpace(r.Project)
		scope := strings.TrimSpace(r.Scope)
		if scope == "" {
			scope = "project"
		}

		if !noDedup {
			dup, derr := piIsDuplicate(s, r, typ, project, scope)
			if derr != nil {
				fmt.Fprintf(os.Stderr, "warn: dedup check failed for %q: %v\n", r.Title, derr)
			} else if dup {
				skipped++
				continue
			}
		}

		if dryRun {
			imported++
			continue
		}

		toolName := "" // tool_name left unset; source is recorded via topic scope, not a column.
		id, err := storeAddObservation(s, store.AddObservationParams{
			SessionID: sessionID,
			Type:      typ,
			Title:     r.Title,
			Content:   r.Content,
			ToolName:  toolName,
			Project:   project,
			Scope:     scope,
			TopicKey:  r.TopicKey,
		})
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "error: failed to import %q: %v\n", r.Title, err)
			continue
		}
		imported++
		_ = id // id used only in non-dry-run verbose output below
		fmt.Printf("imported #%d %q (%s)\n", id, r.Title, typ)
	}

	mode := "imported"
	if dryRun {
		mode = "would import"
	}
	fmt.Printf("pi import: %s %d, skipped(dedup) %d, failed %d (source=%q, file=%s)\n",
		mode, imported, skipped, failed, source, inFile)
}

// parsePiImport accepts three shapes and normalizes them to a record slice:
//  1. {"memories": [ {title,content,...}, ... ]}      — Pi export envelope
//  2. [ {title,content,...}, ... ]                     — bare array of records
//  3. {title,content,...}                              — single record object
//
// It also gracefully accepts an Engram ExportData payload by lifting its
// observations into records, so `engram export` output can be re-imported
// through the Pi bridge.
func parsePiImport(raw []byte) ([]piMemoryRecord, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("empty file")
	}

	// Shape 2: bare array.
	if trimmed[0] == '[' {
		var recs []piMemoryRecord
		if err := json.Unmarshal(raw, &recs); err != nil {
			return nil, fmt.Errorf("decode memory array: %w", err)
		}
		return recs, nil
	}

	// Try the Pi envelope {"memories": [...]} first.
	var env piImportEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && env.Memories != nil {
		return env.Memories, nil
	}

	// Try a single record object.
	var single piMemoryRecord
	if err := json.Unmarshal(raw, &single); err == nil && single.Title != "" {
		return []piMemoryRecord{single}, nil
	}

	// Fall back: Engram ExportData.
	var data store.ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("unrecognized JSON shape (expected {memories:[...]}, [ {...} ], or a single record)")
	}
	if len(data.Observations) == 0 && len(data.Sessions) == 0 && len(data.Prompts) == 0 {
		return nil, fmt.Errorf("no memories found in payload (expected {memories:[...]}, [ {...} ], a single record, or an Engram export)")
	}
	recs := make([]piMemoryRecord, 0, len(data.Observations))
	for _, o := range data.Observations {
		rec := piMemoryRecord{
			Title:    o.Title,
			Content:  o.Content,
			Type:     o.Type,
			Scope:    o.Scope,
			TopicKey: derefString(o.TopicKey),
		}
		if o.Project != nil {
			rec.Project = *o.Project
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

// piIsDuplicate implements the bridge's "dedup by title+type+project" rule.
// It searches existing memories scoped by type+project and treats the incoming
// record as a duplicate when an exact (case-insensitive) title match exists.
// Content is not compared because Pi captures often re-issue the same memory
// with refreshed detail — the title is the stable identity we trust.
func piIsDuplicate(s *store.Store, r piMemoryRecord, typ, project, scope string) (bool, error) {
	query := strings.TrimSpace(r.Title)
	if query == "" {
		return false, nil
	}
	results, err := storeSearch(s, query, store.SearchOptions{
		Type:    typ,
		Project: project,
		Scope:   scope,
		Limit:   20,
	})
	if err != nil {
		return false, err
	}
	want := strings.ToLower(strings.TrimSpace(r.Title))
	for _, res := range results {
		if strings.ToLower(strings.TrimSpace(res.Title)) == want {
			return true, nil
		}
	}
	return false, nil
}

// ─── export ──────────────────────────────────────────────────────────────────

func cmdPiExport(cfg store.Config) {
	format := "json"
	project := ""
	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--format":
			if i+1 < len(os.Args) {
				format = os.Args[i+1]
				i++
			}
		case "--project":
			if i+1 < len(os.Args) {
				project = os.Args[i+1]
				i++
			}
		default:
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n", os.Args[i])
			exitFunc(1)
			return
		}
	}

	if format != "json" {
		fatal(fmt.Errorf("unsupported --format %q (only json is supported)", format))
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	var data *store.ExportData
	if strings.TrimSpace(project) != "" {
		data, err = s.ExportProject(project)
	} else {
		data, err = storeExport(s)
	}
	if err != nil {
		fatal(err)
		return
	}

	envelope := piExportEnvelope{
		Source:       "engram",
		ExportedAt:   data.ExportedAt,
		Project:      project,
		Observations: data.Observations,
		Count:        len(data.Observations),
	}

	out, err := jsonMarshalIndent(envelope, "", "  ")
	if err != nil {
		fatal(err)
		return
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fatal(err)
		return
	}
	fmt.Println()
}

// ─── search ──────────────────────────────────────────────────────────────────

func cmdPiSearch(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: engram pi search <query> [--type TYPE] [--project PROJECT] [--scope SCOPE] [--limit N]")
		exitFunc(1)
		return
	}

	var queryParts []string
	opts := store.SearchOptions{Limit: 10}
	for i := 3; i < len(os.Args); i++ {
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
		case "--scope":
			if i+1 < len(os.Args) {
				opts.Scope = os.Args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(os.Args) {
				if n, err := atoi(os.Args[i+1]); err == nil {
					opts.Limit = n
				}
				i++
			}
		default:
			queryParts = append(queryParts, os.Args[i])
		}
	}

	query := strings.Join(queryParts, " ")
	if strings.TrimSpace(query) == "" {
		fmt.Fprintln(os.Stderr, "error: search query is required")
		exitFunc(1)
		return
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

	// Pi expects a machine-readable envelope with stable metadata, not the
	// human-readable numbered list the base `engram search` prints.
	type piSearchHit struct {
		ID        int64   `json:"id"`
		Title     string  `json:"title"`
		Content   string  `json:"content"`
		Type      string  `json:"type"`
		Scope     string  `json:"scope"`
		Project   string  `json:"project,omitempty"`
		TopicKey  string  `json:"topic_key,omitempty"`
		Rank      float64 `json:"rank"`
		CreatedAt string  `json:"created_at"`
		UpdatedAt string  `json:"updated_at"`
	}
	type piSearchEnvelope struct {
		Query   string         `json:"query"`
		Count   int           `json:"count"`
		Results []piSearchHit `json:"results"`
	}

	hits := make([]piSearchHit, 0, len(results))
	for _, r := range results {
		hit := piSearchHit{
			ID:        r.ID,
			Title:     r.Title,
			Content:   r.Content,
			Type:      r.Type,
			Scope:     r.Scope,
			Rank:      r.Rank,
			CreatedAt: r.CreatedAt,
			UpdatedAt: r.UpdatedAt,
		}
		if r.Project != nil {
			hit.Project = *r.Project
		}
		if r.TopicKey != nil {
			hit.TopicKey = *r.TopicKey
		}
		hits = append(hits, hit)
	}

	out, err := jsonMarshalIndent(piSearchEnvelope{
		Query:   query,
		Count:   len(hits),
		Results: hits,
	}, "", "  ")
	if err != nil {
		fatal(err)
		return
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fatal(err)
		return
	}
	fmt.Println()
}

// ─── redaction ───────────────────────────────────────────────────────────────

// piRedactor applies the requested redaction passes to imported records before
// they touch the database. Each pass is opt-in so callers can trade safety for
// fidelity depending on the source's trust level.
type piRedactor struct {
	secrets bool
	keys    bool
	logs    bool
}

func newPiRedactor(secrets, keys, logs bool) *piRedactor {
	return &piRedactor{secrets: secrets, keys: keys, logs: logs}
}

func (rd *piRedactor) apply(r piMemoryRecord) piMemoryRecord {
	if rd == nil || (!rd.secrets && !rd.keys && !rd.logs) {
		return r
	}
	r.Title = rd.redactString(r.Title)
	r.Content = rd.redactString(r.Content)
	return r
}

func (rd *piRedactor) redactString(s string) string {
	if rd == nil {
		return s
	}
	out := s
	if rd.secrets {
		out = redactSecretPatterns(out)
	}
	if rd.keys {
		out = redactKeyPatterns(out)
	}
	if rd.logs {
		out = redactLogLines(out)
	}
	return out
}

// redactSecretPatterns masks high-entropy credential-looking tokens.
var (
	// API keys: OpenAI-style (sk-...), Anthropic-style (sk-ant-...), Stripe (sk_live_/sk_test_),
	// JWTs (eyJ... three base64 segments), GitHub PATs (ghp_/gho_/ghs_/ghr_...), Slack (xox[baprs]-...).
	reAPIKey = regexp.MustCompile(
		`(?i)(sk-(?:ant-)?[A-Za-z0-9_\-]{8,}|sk_(?:live|test)_[A-Za-z0-9]{8,}|eyJ[A-Za-z0-9_\-\.]{8,}\.[A-Za-z0-9_\-\.]{8,}\.[A-Za-z0-9_\-\.]{8,}|gh[posr]_[A-Za-z0-9]{16,}|xox[baprs]-[A-Za-z0-9\-]{10,})`,
	)
	// URLs carrying a token/query-credential: ?token=, ?access_token=, ?key=, ?api_key=,
	// ?password=, ?secret=, #access_token=, and userinfo https://user:pass@host.
	reURLWithToken = regexp.MustCompile(
		`(?i)([?&#](?:access_token|token|api_key|apikey|key|password|passwd|secret|refresh_token)=)[^&\s#]+`,
	)
	reURLUserinfo = regexp.MustCompile(`(https?://)[^/\s:@]+:[^/\s@]+@`)
)

func redactSecretPatterns(s string) string {
	s = reAPIKey.ReplaceAllString(s, "[REDACTED:secret]")
	s = reURLWithToken.ReplaceAllString(s, "${1}[REDACTED]")
	s = reURLUserinfo.ReplaceAllString(s, "${1}[REDACTED]@[REDACTED]")
	return s
}

// redactKeyPatterns masks the *value* of well-known credential assignments
// while preserving the surrounding label and punctuation so the import stays
// readable. Three shapes are matched:
//   1. key: value / key=value (optionally quoted) — anywhere on a line, not
//      just at the start, because Pi imports often inline credentials.
//   2. Bearer <token> and Authorization: Bearer <token> (space-delimited).
//   3. Authorization: <scheme> <token>.
// Group layout (used by the replacement template):
//   ${1}  label + separator (e.g. "api_key: " / "token=" / "Bearer ")
//   ${2}  optional opening quote
//   ${3}  the secret value (replaced)
//   ${4}  optional closing quote
var reKeyValueSecret = regexp.MustCompile(
	`(?im)(` +
		`(?:api[_-]?key|apikey|secret|secret[_-]?key|access[_-]?token|auth[_-]?token|refresh[_-]?token|bearer|password|passwd|client[_-]?secret|private[_-]?key|aws[_-]?secret[_-]?access[_-]?key|token)\s*[:=]\s*` +
		`|(?:authorization\s*[:=]\s*)?Bearer\s+` +
		`)(["']?)([A-Za-z0-9_\-\.=]+)(["']?)`,
)

func redactKeyPatterns(s string) string {
	// Preserve ${1} (label/separator) and surrounding quotes; replace only the value.
	return reKeyValueSecret.ReplaceAllString(s, `${1}${2}[REDACTED]${4}`)
}

// reLogLine matches common log prefixes (RFC3339 timestamp + LEVEL, or bracketed
// [LEVEL] / [component]) so --redact-logs can drop entire log lines from imports.
var reLogLine = regexp.MustCompile(
	`(?m)^\s*(?:\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+\-]\d{2}:?\d{2})?\s+(?:DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL|TRACE)\b|\[(?:DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL|TRACE)\]|\[\w+\]\s+(?:DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL))`,
)

func redactLogLines(s string) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		if reLogLine.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// sanitizeSessionSuffix collapses a user-supplied --source into a safe,
// filesystem-and-SQL-friendly session suffix (lowercase alnum + dashes).
func sanitizeSessionSuffix(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if out == "" {
		return "anon"
	}
	return out
}

// atoi is a thin wrapper kept local to avoid pulling strconv into callers that
// only need a best-effort integer parse with a Go-standard error.
func atoi(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}
