# Design: memory-conflict-audit (Phase 3)

**Change**: memory-conflict-audit
**Phase**: design (HOW — architecture + contracts)
**Artifact store**: hybrid (engram + openspec)
**Reads**: `sdd/memory-conflict-audit/proposal` (#2706), `sdd/memory-conflict-audit/spec` (#2710)
**Writes**: this file + `sdd/memory-conflict-audit/design`

---

## 1. Architecture overview

### 1.1 Pattern

Three-layer additive design. From bottom up:

```
┌──────────────────────────────────────────────────────────┐
│  Surface layer (thin wrappers — flag/JSON parsing only)  │
│  ┌──────────────────────────┐ ┌────────────────────────┐ │
│  │ cmd/engram/conflicts.go  │ │ internal/server/...    │ │
│  │  cmdConflicts dispatch   │ │  /conflicts/* handlers │ │
│  └────────────┬─────────────┘ └────────────┬───────────┘ │
└───────────────┼──────────────────────────────┼───────────┘
                │ both call same store methods │
┌───────────────▼──────────────────────────────▼───────────┐
│  Store layer (rich behavior — internal/store)            │
│  ┌──────────────────────────┐ ┌────────────────────────┐ │
│  │ relations.go             │ │ store.go (existing)    │ │
│  │  ListRelations           │ │  ReplayDeferred ✔      │ │
│  │  CountRelations          │ │  CountDeferredAndDead ✔│ │
│  │  GetRelationStats        │ │                        │ │
│  │  ListDeferred            │ │                        │ │
│  │  GetDeferred             │ │                        │ │
│  │  ScanProject             │ │                        │ │
│  └──────────────────────────┘ └────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
                            │
                            ▼
┌──────────────────────────────────────────────────────────┐
│  SQLite (memory_relations, observations,                 │
│           sync_apply_deferred + new index)               │
└──────────────────────────────────────────────────────────┘
```

Behavior lives in store. CLI and HTTP only:
- parse flags / JSON,
- call store methods,
- format output.

This matches `engram stats`, `engram sync`, `/sync/status` (locked convention).

### 1.2 Boundaries (engram-architecture-guardrails)

- `cmd/engram/conflicts.go` — flag parsing, output formatting. No SQL.
- `internal/server/server.go` — JSON parsing, response shaping. No SQL.
- `internal/store/relations.go` — relation read helpers + scan loop.
- `internal/store/store.go` — deferred read helpers + new index migration.

No SQL outside `internal/store`. No HTML. No transport leaks into store.
Local SQLite remains source of truth (no cloud calls in this phase).

---

## 2. New store types

All go in `internal/store/relations.go` (or a new `internal/store/conflicts.go` if `relations.go` is becoming unwieldy — decision deferred to tasks phase). Types are **exported** because CLI and HTTP both consume them.

### 2.1 `ListRelationsOptions`

```go
type ListRelationsOptions struct {
    Project   string    // empty = no project filter (HTTP global; CLI fills via detect)
    Status    string    // empty = no status filter ("pending"|"judged"|"orphaned"|"ignored")
    SinceTime time.Time // zero = no time filter (RFC3339 in HTTP, parsed before passing in)
    Limit     int       // <=0 → defaults to 50; clamped to 500 by caller before passing
    Offset    int       // 0 by default; pagination cursor
}
```

Notes:
- `Project` filter uses JOIN to `observations` (locked from proposal).
- `Status` filter uses `idx_memrel_status_created` (new index — see §6).
- `Offset` exists to support eventual page-2 navigation; HTTP supports `?offset=`, CLI does not in Phase 3.

### 2.2 `RelationListItem`

Composes the relation row with denormalized observation context for display. The store does the JOIN once so callers don't re-query for titles.

```go
type RelationListItem struct {
    ID             int64
    SyncID         string
    Relation       string
    JudgmentStatus string
    SourceID       string  // sync_id (TEXT)
    SourceTitle    string  // from observations JOIN; empty if soft-deleted/missing
    TargetID       string  // sync_id (TEXT)
    TargetTitle    string  // from observations JOIN; empty if soft-deleted/missing
    CreatedAt      string  // SQLite TEXT timestamp, ISO8601
    UpdatedAt      string
}
```

This is intentionally **not** the full `Relation` struct — list views show one line per row; detail views (`GetRelationDetail` if needed) can return the full `Relation` plus snippets.

### 2.3 `RelationStats`

Single struct returned by `GetRelationStats`. One query (GROUP BY) populates it; no N+1.

```go
type RelationStats struct {
    Project          string         // echoed for display
    ByRelation       map[string]int // {"conflicts_with": 12, "supersedes": 3, ...}
    ByJudgmentStatus map[string]int // {"pending": 5, "judged": 7, ...}
    DeferredCount    int            // from CountDeferredAndDead()
    DeadCount        int            // from CountDeferredAndDead()
}
```

`GetRelationStats` calls `CountDeferredAndDead()` internally so callers get one struct. CLI and HTTP both render this same struct.

### 2.4 `DeferredRow`

Decoded payload (locked from proposal § Pre-resolved decisions). Decode happens in store on read so callers don't re-parse.

```go
type DeferredRow struct {
    SyncID          string
    Entity          string                 // "observation" | "relation"
    Payload         map[string]any         // json.Unmarshal result; nil if payload was malformed
    PayloadRaw      string                 // raw payload string preserved for forensics on malformed rows
    PayloadValid    bool                   // false when JSON decode failed
    ApplyStatus     string                 // "deferred" | "dead"
    RetryCount      int
    LastError       *string                // pointer because column is nullable
    LastAttemptedAt *string                // pointer; nullable
    FirstSeenAt     string                 // NOT NULL in schema
}
```

Why both `Payload` (decoded) and `PayloadRaw`: dead rows are often dead BECAUSE of malformed JSON. The admin needs the raw string to debug. Decoded form is preferred when valid.

### 2.5 `ScanResult`

Returned by `ScanProject`. CLI prints, HTTP serializes to JSON.

```go
type ScanResult struct {
    Project           string
    Inspected         int  // observations walked
    CandidatesFound   int  // total (source, target) pairs surfaced by FindCandidates
    AlreadyRelated    int  // pairs skipped because pre-check found existing row
    RelationsInserted int  // 0 in dry-run; <= MaxInsert in apply mode
    Capped            bool // true when MaxInsert was reached and we stopped early
    DryRun            bool // mirrors caller request
}
```

JSON tag mapping (used by HTTP handler):
```go
type ScanResult struct {
    Project           string `json:"project"`
    Inspected         int    `json:"inspected"`
    CandidatesFound   int    `json:"candidates_found"`
    AlreadyRelated    int    `json:"already_related"`
    RelationsInserted int    `json:"inserted"`
    Capped            bool   `json:"capped"`
    DryRun            bool   `json:"dry_run"`
}
```

### 2.6 `ScanOptions`

```go
type ScanOptions struct {
    Project   string    // required (empty → error)
    Since     time.Time // zero = no lower bound
    Apply     bool      // default false → dry-run
    MaxInsert int       // <=0 → defaults to 100
}
```

---

## 3. New store methods

All on `*Store`. All in `internal/store/relations.go` unless noted.

### 3.1 `ListRelations(opts ListRelationsOptions) ([]RelationListItem, error)`

```sql
SELECT
    r.id, r.sync_id, r.relation, r.judgment_status,
    r.source_id, src.title AS source_title,
    r.target_id, tgt.title AS target_title,
    r.created_at, r.updated_at
FROM memory_relations r
LEFT JOIN observations src ON src.sync_id = r.source_id AND src.deleted_at IS NULL
LEFT JOIN observations tgt ON tgt.sync_id = r.target_id AND tgt.deleted_at IS NULL
WHERE 1=1
  [AND r.judgment_status = ?]              -- if opts.Status != ""
  [AND r.created_at >= ?]                  -- if !opts.SinceTime.IsZero()
  [AND (src.project = ? OR tgt.project = ?)] -- if opts.Project != ""
ORDER BY r.created_at DESC
LIMIT ? OFFSET ?
```

Notes:
- `LEFT JOIN` (not INNER) so soft-deleted observations still let the relation appear (with empty title).
- Project filter is `OR` on either side because relations are bidirectional in spirit; spec says "source OR target".
- ORDER BY `created_at DESC` matches `idx_memrel_status_created`.

### 3.2 `CountRelations(opts ListRelationsOptions) (int, error)`

Same WHERE clause, `SELECT count(*)`. `Limit` and `Offset` ignored.

### 3.3 `GetRelationStats(project string) (RelationStats, error)`

Two queries (one round trip each, fine for Phase 3):

```sql
-- Query 1: counts grouped by relation + status
SELECT r.relation, r.judgment_status, count(*)
FROM memory_relations r
[LEFT JOIN observations src ON src.sync_id = r.source_id]
[LEFT JOIN observations tgt ON tgt.sync_id = r.target_id]
[WHERE src.project = ? OR tgt.project = ?]   -- only if project != ""
GROUP BY r.relation, r.judgment_status

-- Query 2: existing CountDeferredAndDead()
```

Build the maps in Go from the rowset, then call `CountDeferredAndDead`.

### 3.4 `ListDeferred(opts ListDeferredOptions) ([]DeferredRow, error)`

```go
type ListDeferredOptions struct {
    Status string // "" | "deferred" | "dead"
    Limit  int    // <=0 → 50
    Offset int
}
```

```sql
SELECT sync_id, entity, payload, apply_status, retry_count,
       last_error, last_attempted_at, first_seen_at
FROM sync_apply_deferred
[WHERE apply_status = ?]
ORDER BY first_seen_at DESC
LIMIT ? OFFSET ?
```

For each row, `json.Unmarshal(payload, &m)`. On error: `PayloadValid = false`, `Payload = nil`, `PayloadRaw = original`. Never returns an error for malformed payload (forensic visibility is the point).

### 3.5 `GetDeferred(syncID string) (DeferredRow, error)`

Single-row variant. Returns `ErrNotFound` (a new sentinel — or use existing pattern; see §11) when row doesn't exist. Same payload-decoding contract as `ListDeferred`.

### 3.6 `ScanProject(opts ScanOptions) (ScanResult, error)`

Algorithm (matches §5 in the spec):

```
1. Validate opts.Project is non-empty.
2. Apply defaults: MaxInsert=100 if <=0.
3. Query observations:
     SELECT id, sync_id, scope FROM observations
     WHERE project = ? AND deleted_at IS NULL
       [AND created_at >= ?]
     ORDER BY id
4. result.Inspected = len(rows).
5. For each obs:
     a. candidates, _ := s.FindCandidates(obs.id, CandidateOptions{
            Project: opts.Project, Scope: obs.scope, Limit: 3,
        })
        // Note: FindCandidates already inserts pending relation rows for each
        // candidate. We rely on that side effect when Apply=true. For DryRun,
        // see step 5b.
     b. If opts.DryRun (Apply=false):
          For each candidate, increment CandidatesFound.
          Run pre-check: SELECT 1 FROM memory_relations
                         WHERE source_id=? AND target_id=? LIMIT 1
          If exists → AlreadyRelated++ (do NOT insert; FindCandidates will be
          replaced by a non-mutating variant — see §3.6.1).
     c. If opts.Apply:
          For each candidate:
            - Pre-check pair existence (idx_memrel_source).
            - If exists → AlreadyRelated++; continue.
            - If RelationsInserted >= MaxInsert → set Capped=true; break the
              outer loop.
            - Insert pending row (or rely on FindCandidates' insertion — see §3.6.1).
            - RelationsInserted++.
6. Return ScanResult.
```

#### 3.6.1 Important: `FindCandidates` already inserts

Reading `FindCandidates` (`internal/store/relations.go:167`) shows it inserts a pending relation row for each candidate as a side effect (see `Candidate.JudgmentID` doc).

**Decision**: introduce a new private helper `findCandidatesNoInsert` (or pass a flag through `CandidateOptions{InsertPending: false}`) to support dry-run. Adding a flag is more conservative and matches "no behavior change to Phase 1 surfaces" (§7 of spec).

**Refined approach** (locked here):

- Add `CandidateOptions.SkipInsert bool` (default false → existing behavior preserved).
- Internally `FindCandidates` skips the INSERT loop when `SkipInsert == true`.
- `ScanProject` passes `SkipInsert: true` and does its OWN cap-aware insert loop with the pre-check.

This gives `ScanProject` precise control over `MaxInsert` semantics without forking `FindCandidates`.

### 3.7 Existing methods reused (do NOT redesign)

- `s.ReplayDeferred()` — at `store.go:5854` — Phase 2.
- `s.CountDeferredAndDead()` — at `store.go:5940` — Phase 2.
- `s.FindCandidates(...)` — extended with `SkipInsert` (§3.6.1).

---

## 4. CLI dispatch design

### 4.1 New file: `cmd/engram/conflicts.go`

Mirrors `cmd/engram/cloud.go` dispatch pattern.

```go
func cmdConflicts(cfg store.Config) {
    if len(os.Args) < 3 {
        fmt.Fprintln(os.Stderr, "usage: engram conflicts <subcommand> [options]")
        fmt.Fprintln(os.Stderr, "supported subcommands: list, show, stats, scan, deferred")
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
        fmt.Fprintf(os.Stderr, "unknown conflicts command: %s\n", os.Args[2])
        fmt.Fprintln(os.Stderr, "supported subcommands: list, show, stats, scan, deferred")
        exitFunc(1)
    }
}
```

### 4.2 Add to `cmd/engram/main.go` switch

```go
case "conflicts":
    cmdConflicts(cfg)
```

Inserted alphabetically near `case "context":` / before `case "stats":`.

### 4.3 Sub-command flag schemas

| Command | Flags |
|---------|-------|
| `list` | `--project NAME` (default: cwd-detected), `--status STATUS`, `--since RFC3339`, `--limit N` (default 50, max 500) |
| `show` | positional `<relation_id>` (int) |
| `stats` | `--project NAME` (default: cwd-detected; empty allowed via `--all`) |
| `scan` | `--project NAME` (default: cwd-detected), `--since RFC3339`, `--dry-run` (default true), `--apply` (turns off dry-run), `--max-insert N` (default 100) |
| `deferred` | `--status STATUS` (deferred\|dead\|"" all), `--limit N` (default 50), `--inspect SYNC_ID` (mutually exclusive with `--replay`), `--replay` (calls `s.ReplayDeferred()`) |

### 4.4 Project scoping helper

Reuse the existing pattern from `resolveServeSyncStatusProject` (main.go:652). Extract into a small shared helper if not already shared:

```go
func resolveConflictsProject(explicit string) string {
    if p := strings.TrimSpace(explicit); p != "" {
        return normalizeProject(p)
    }
    if cwd, err := os.Getwd(); err == nil {
        if detected := detectProject(cwd); detected != "" {
            return normalizeProject(detected)
        }
    }
    return ""
}
```

If empty after fallback AND command requires project (list/stats/scan with no `--all`), print:

```
error: no project detected. use --project NAME or run from inside a project directory.
```

Exit code 1.

### 4.5 Output formatting

Aligned columns matching `cmdStats` style. Example for `list`:

```
RELATION_ID  TYPE             STATUS    CREATED              SOURCE → TARGET
─────────────────────────────────────────────────────────────────────────────
42           conflicts_with   pending   2026-04-26 14:21:03  alpha … → beta …
43           supersedes       judged    2026-04-26 14:18:11  beta  … → gamma…
```

Truncate titles to fit (max 24 chars + `…`). No `--json` flag (locked).

`stats` follows the existing `engram stats` label-colon style:

```
project:   alpha
relations: 12 (pending=5, judged=7)
  by relation_type:
    conflicts_with:   8
    supersedes:       3
    related:          1
deferred:  4
dead:      1
```

---

## 5. HTTP route registration order (CRITICAL)

Go 1.22's `http.ServeMux` uses pattern specificity, but specific paths MUST be registered before wildcard paths to avoid ambiguity. In `internal/server/server.go::routes()`:

```go
// Conflicts (Phase 3)
//
// IMPORTANT: order matters. More-specific paths register BEFORE wildcards.
s.mux.HandleFunc("GET /conflicts",                    s.handleListConflicts)
s.mux.HandleFunc("GET /conflicts/stats",              s.handleConflictsStats)      // before {id}
s.mux.HandleFunc("GET /conflicts/deferred",           s.handleListDeferred)        // before {id}
s.mux.HandleFunc("POST /conflicts/scan",              s.handleScanConflicts)       // before {id}
s.mux.HandleFunc("POST /conflicts/deferred/replay",   s.handleReplayDeferred)
s.mux.HandleFunc("GET /conflicts/{relation_id}",      s.handleGetConflict)         // wildcard last
```

Go 1.22 mux DOES detect conflicts at register time (panics on overlap), so misordering may surface as a panic at boot. The order above is the safe registration order.

### 5.1 Handler signatures

All handlers use the existing helpers `jsonResponse` / `jsonError` (already in server.go).

```go
func (s *Server) handleListConflicts(w http.ResponseWriter, r *http.Request)
func (s *Server) handleGetConflict(w http.ResponseWriter, r *http.Request)
func (s *Server) handleConflictsStats(w http.ResponseWriter, r *http.Request)
func (s *Server) handleScanConflicts(w http.ResponseWriter, r *http.Request)
func (s *Server) handleListDeferred(w http.ResponseWriter, r *http.Request)
func (s *Server) handleReplayDeferred(w http.ResponseWriter, r *http.Request)
```

### 5.2 Response shapes (JSON)

`GET /conflicts`:
```json
{
  "total": 80,
  "limit": 50,
  "offset": 0,
  "relations": [{"id":42,"sync_id":"rel-abc","relation":"conflicts_with","judgment_status":"pending","source_id":"obs-1","source_title":"...","target_id":"obs-2","target_title":"...","created_at":"...","updated_at":"..."}]
}
```

`GET /conflicts/{relation_id}`:
```json
{
  "id": 42,
  "sync_id": "rel-abc",
  "relation": "conflicts_with",
  "judgment_status": "pending",
  "source_id": "obs-1",
  "source_snippet": "first 200 chars...",
  "target_id": "obs-2",
  "target_snippet": "first 200 chars...",
  "created_at": "...",
  "updated_at": "..."
}
```

404 on miss with `{"error":"relation 9999 not found"}`.

`GET /conflicts/stats`:
```json
{
  "project": "alpha",
  "by_relation": {"conflicts_with": 8, "supersedes": 3},
  "by_judgment_status": {"pending": 5, "judged": 7},
  "deferred": 4,
  "dead": 1
}
```

`POST /conflicts/scan` (request):
```json
{"project":"alpha","since":"2026-04-01T00:00:00Z","apply":true,"max_insert":50}
```

Response (uses `ScanResult` JSON tags from §2.5; `warning` field added when capped):
```json
{
  "project": "alpha",
  "inspected": 312,
  "candidates_found": 150,
  "already_related": 0,
  "inserted": 50,
  "capped": true,
  "dry_run": false,
  "warning": "max_insert cap reached (50); rerun with higher --max-insert to continue"
}
```

400 with JSON error when `project` is missing/empty.

`GET /conflicts/deferred`:
```json
{
  "total": 10,
  "limit": 5,
  "rows": [{"sync_id":"...","entity":"relation","apply_status":"deferred","retry_count":2,"last_error":"...","last_attempted_at":"...","first_seen_at":"...","payload":{...},"payload_valid":true}]
}
```

Note: `payload` is the decoded `map[string]any`. When `payload_valid=false`, include `payload_raw` instead.

`POST /conflicts/deferred/replay`:
```json
{"retried": 4, "succeeded": 3, "failed": 0, "dead": 1}
```

(Maps directly from `ReplayDeferredResult` — Phase 2 type.)

### 5.3 Pagination clamp logic

```go
limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
if limit <= 0 { limit = 50 }
if limit > 500 { limit = 500 }   // clamp, not error (locked from spec REQ)
```

Spec REQ says clamp (no 4xx). Pre-resolved: design-doc constraint above CLI says "out of range → 400" — that's CLI side (rejected at flag parse time). HTTP clamps silently.

---

## 6. New index migration

### 6.1 SQL

```sql
CREATE INDEX IF NOT EXISTS idx_memrel_status_created
    ON memory_relations(judgment_status, created_at DESC);
```

### 6.2 Migration placement

In `migrate()` in `internal/store/store.go`, append at the end of the existing migration sequence. Idempotent via `IF NOT EXISTS` — no `schema_version` bump needed if existing migrations are also `IF NOT EXISTS` style. (Verify with the current migration body during tasks phase.)

If the project uses a numbered migration step pattern, add as the next sequential step. Either way: **append, do not interleave**.

### 6.3 Migration test

`TestMigrate_AddsIdxMemrelStatusCreated` in `internal/store/store_test.go`:

```go
// Open an empty DB → run migrate → verify index exists.
// Approach: query sqlite_master.
SELECT name FROM sqlite_master
WHERE type='index' AND name='idx_memrel_status_created'
```

Assert exactly one row.

For "applies on legacy DB" coverage, reuse the Phase 2 pattern (legacy DDL constants + `newTestStoreWithLegacySchema`) if a snapshot is needed; otherwise the empty-DB path is sufficient because `IF NOT EXISTS` handles both cases.

---

## 7. Pre-check optimization

Both pair-existence pre-checks use `idx_memrel_source` (verified to exist from Phase 1):

```sql
SELECT 1 FROM memory_relations
WHERE source_id = ? AND target_id = ?
LIMIT 1
```

Cost: O(log N) per pre-check via index seek. For a scan of K observations, each surfacing up to 3 candidates, total pre-checks = 3K → 3K · log N ≈ negligible for Phase 3 DB sizes (< 100k observations).

Phase 4 hook: a `(source_id, target_id)` UNIQUE index would make the pre-check redundant, but that's denormalization beyond Phase 3 scope.

---

## 8. Pagination defaults

- HTTP `limit`: default 50, clamp at 500 (silent clamp — locked).
- HTTP `offset`: default 0; no max (DB performance handles it).
- CLI `list --limit`: default 50, max 500. > 500 → exit 1 with `error: --limit must be between 1 and 500` (CLI is stricter than HTTP; locked).
- CLI `list` prints first N rows; if `total > N`, print footer:
  ```
  showing 50 of 80; use --limit or query HTTP /conflicts for full pagination
  ```

---

## 9. `--dry-run` vs `--apply` contract

| Mode | Default? | DB side effects | Output |
|------|----------|-----------------|--------|
| `--dry-run` (or no flag) | yes | none | "would insert N rows" + breakdown |
| `--apply` | no | inserts up to MaxInsert | "inserted N rows" + breakdown |
| `--apply --max-insert 50` then 150 candidates exist | n/a | inserts exactly 50 | WARNING printed; `Capped=true` in result |

CLI mutual-exclusion logic:
- `--dry-run --apply` → exit 1 with `error: --dry-run and --apply are mutually exclusive`.
- Neither flag → dry-run (safe default).

HTTP equivalent: `apply: false` (or omitted) is dry-run; `apply: true` activates write. `max_insert` field defaults to 100 server-side if 0/missing.

---

## 10. Phase 2 fold-ins

### 10.1 Seq in FK miss log

`internal/store/store.go`, in `applyRelationUpsertTx` (or wherever the current log line lives — verify path during tasks phase):

```go
// Before:
log.Printf("[store] ApplyPulledMutation: relation FK miss source=%s target=%s",
    sourceID, targetID)

// After:
log.Printf("[store] ApplyPulledMutation: relation FK miss seq=%d source=%s target=%s",
    mut.Seq, sourceID, targetID)
```

The function signature already has access to the mutation (Seq comes from there). Single-line edit.

### 10.2 `TestApplyPulledRelation_MalformedPayload_StraightToDead`

Location: `internal/store/sync_apply_test.go` (locked).

Two test cases:
1. Payload string `"not valid json"` → expect `apply_status='dead'`, `retry_count=0`.
2. Payload `{"relation_type":"conflicts"}` (missing required fields) → expect `apply_status='dead'`, `retry_count=0`.

Both verify the row in `sync_apply_deferred` after a single `ApplyPulledMutation` call.

### 10.3 Multi-actor sync_id docs

`docs/PLUGINS.md` — add a section near existing relation/sync docs (TBD exact location during tasks phase). Content covers:
- Multiple agents can produce distinct relation rows for the same `(source_id, target_id)`.
- Each row has its own `sync_id` namespaced by actor.
- Annotation parsers may see duplicate `conflicts:` prefix lines for the same pair.
- Recommended: dedupe by `(source_id, target_id)` if collapsing for display.

---

## 11. Errors and sentinels

Existing patterns to reuse:
- `ErrRelationFKMissing` (Phase 2 — store.go).
- `ErrApplyDead` (Phase 2 — store.go).

New (if needed):
- `ErrDeferredNotFound = errors.New("deferred row not found")` — used by `GetDeferred`.
- Or follow existing pattern `if err == sql.ErrNoRows { return DeferredRow{}, fmt.Errorf("GetDeferred: sync_id %q not found", id) }` — preferred since it matches `FindCandidates` style. Decision: **wrap `sql.ErrNoRows` with formatted message** (no new sentinel; matches existing convention).

CLI error messages: human-readable, prefixed `error: `, lowercased. HTTP errors: `{"error": "..."}` JSON body, appropriate status code.

---

## 12. Test strategy

Per layer (matches Phase 1 + Phase 2 conventions):

| Layer | Test file | Pattern |
|-------|-----------|---------|
| Store | `internal/store/relations_test.go` (extend) | Real SQLite + seed data |
| Store (deferred) | `internal/store/store_test.go` (extend) | Real SQLite + `sync_apply_deferred` seed |
| Store (migration) | `internal/store/store_test.go` (extend) | `TestMigrate_AddsIdxMemrelStatusCreated` |
| Store (sync apply) | `internal/store/sync_apply_test.go` (extend) | `TestApplyPulledRelation_MalformedPayload_StraightToDead` |
| CLI | `cmd/engram/conflicts_test.go` (new) | `captureOutput(t, fn)` + `withArgs` (Phase 1+2 helpers) |
| HTTP | `internal/server/server_test.go` (extend) | `httptest.NewRecorder()` + JSON decode |
| Integration | `internal/store/relations_test.go` or new `_integration_test.go` | Full `ScanProject` end-to-end with seeded multi-observation DB |

Strict TDD: RED → GREEN → REFACTOR for every new method, handler, and CLI sub-command. Migration test is mandatory.

Additional coverage required by spec REQs:
- `GET /conflicts?limit=1000` → expect 200 with at most 500 rows (clamp test).
- `POST /conflicts/scan` body without `project` → 400.
- `engram conflicts deferred --inspect <sync_id>` decoded payload print.
- `engram conflicts scan --apply --max-insert 50` with 150 candidates → exactly 50 inserted, warning printed, exit 0.

---

## 13. Affected files (final list)

| File | Action | Purpose |
|------|--------|---------|
| `cmd/engram/main.go` | Modify | Add `case "conflicts":` |
| `cmd/engram/conflicts.go` | New | Dispatch + 5 sub-command functions |
| `cmd/engram/conflicts_test.go` | New | CLI sub-command tests |
| `internal/server/server.go` | Modify | 6 routes + 6 handlers |
| `internal/server/server_test.go` | Modify | HTTP tests for 6 endpoints |
| `internal/store/relations.go` | Modify | `ListRelations`, `CountRelations`, `GetRelationStats`, `ScanProject`, types, `SkipInsert` flag on `CandidateOptions` |
| `internal/store/relations_test.go` | Modify | Tests for new methods |
| `internal/store/store.go` | Modify | `ListDeferred`, `GetDeferred`, new index migration, Seq in FK-miss log |
| `internal/store/store_test.go` | Modify | Migration test, deferred listing tests |
| `internal/store/sync_apply_test.go` | Modify | `TestApplyPulledRelation_MalformedPayload_StraightToDead` |
| `docs/PLUGINS.md` | Modify | Multi-actor sync_id section |

---

## 14. ADR-style decisions

### ADR-1: Project filter via JOIN, not denormalized column

**Decision**: `ListRelations` joins to `observations` for project filtering. No `project` column added to `memory_relations`.

**Rationale**:
- Phase 3 scope is observability + audit — schema migrations are friction.
- JOIN cost is acceptable at Phase 3 scale (< 100k relations).
- Denormalization can be added later without breaking the API (HTTP/CLI surface unchanged).

**Rejected alternative**: Add `project TEXT` column to `memory_relations` populated on insert. Rejected because: (a) requires backfill migration, (b) requires consistency rule (what if source.project != target.project?), (c) Phase 4 hook explicitly mentions this — defer.

### ADR-2: Synchronous `POST /conflicts/scan`

**Decision**: Scan runs synchronously in the HTTP handler. No background job, no queue.

**Rationale**:
- Phase 3 DB sizes don't justify async infrastructure.
- CLI is the recommended path for large scans (`engram conflicts scan` runs on the operator's terminal — no HTTP timeout).
- Documented timeout guidance is enough mitigation for Phase 3.

**Rejected alternative**: 202 Accepted + job_id polling. Rejected because: introduces job table, polling endpoint, status state — disproportionate complexity for the current need.

### ADR-3: Single-threaded scan loop

**Decision**: `ScanProject` walks observations sequentially, calling `FindCandidates` one at a time.

**Rationale**:
- Goroutine pool is a Phase 4 hook explicitly listed.
- FTS5 reads are SQLite-bound; parallelism gain is questionable without measurement.
- Sequential is easier to reason about, easier to test, easier to add `--max-insert` cap to.

**Rejected alternative**: Worker pool with 4 goroutines. Rejected: premature optimization for Phase 3.

### ADR-4: `DeferredRow.Payload` is decoded `map[string]any`, not raw string

**Decision**: Store decodes payload JSON in `ListDeferred` / `GetDeferred`. Raw preserved in `PayloadRaw`.

**Rationale**:
- Admin UX: `engram conflicts deferred --inspect <sync_id>` should print human-readable structure, not a JSON blob to be re-parsed manually.
- Forensic safety: `PayloadValid=false` + `PayloadRaw` keeps malformed payloads visible (those are the most interesting ones).

**Rejected alternative**: Raw string only. Rejected because admins would need to pipe to `jq` for every inspect call — friction.

### ADR-5: Pair-existence pre-check uses `idx_memrel_source`

**Decision**: Skip pairs where any relation row already exists (any judgment_status). Use 2-column lookup against `idx_memrel_source`.

**Rationale**:
- Prevents duplicate pending rows (the dominant Phase 3 failure mode).
- Existing index makes it cheap (no new index needed for this guard).
- "Skip if any status" is simpler than "skip if pending only" — operators don't care about re-judging.

**Rejected alternative**: Allow re-insert if status is `judged`/`rejected`. Rejected because: (a) creates two pending rows, (b) confusing for operators, (c) explicit `engram conflicts re-pend <id>` is a future Phase 4 capability.

### ADR-6: New `idx_memrel_status_created` index

**Decision**: Add composite index `(judgment_status, created_at DESC)` to `memory_relations`.

**Rationale**:
- `ListRelations` filters by status and orders by created_at. Without this index, "list pending relations" full-scans the table.
- Read-only index, additive, idempotent (`IF NOT EXISTS`). Zero data risk.
- Doesn't block Phase 4's `project` denormalization.

**Rejected alternative**: Defer to Phase 4 with denormalized `project` column. Rejected because: (a) the index is independent of any future column, (b) operators need fast list NOW, (c) trivial to add and remove.

---

## 15. Risks (architectural)

| Risk | Scope | Mitigation |
|------|-------|------------|
| `FindCandidates` insertion side-effect coupled to dry-run | Store | New `SkipInsert` flag on `CandidateOptions` (additive) |
| HTTP route order panic at boot | Server | Documented order in §5; pre-commit smoke test that registers routes |
| Project filter via JOIN slow at >100k relations | Store | Documented Phase 4 hook; not a Phase 3 blocker |
| `ReplayDeferred` semantic drift between CLI/HTTP and autosync | Store | Same path used everywhere (locked); contract tests pin behavior |
| `MaxInsert` cap silently truncating without WARNING | CLI/HTTP | `Capped` field always returned; CLI prints WARNING; HTTP includes `warning` field |
| Pair-existence pre-check race (concurrent scan + agent insert) | Store | Acceptable: scan is serialized in one process; if a race happens, UNIQUE constraint on `sync_id` prevents corruption (FindCandidates generates unique sync_ids) |

---

## 16. Out of scope (Phase 4 hooks confirmed)

- `project` column on `memory_relations` (denormalize)
- Goroutine pool for scan parallelism
- Resumable scan with checkpoint table
- `--json` flag for CLI output
- Cloud admin dashboard widget consuming `/conflicts/*`
- Embedding-based candidate detection (replace BM25 source)
- `(source_id, target_id)` UNIQUE constraint on `memory_relations`

---

## 17. Validation checklist (before tasks phase)

- [x] All 16 spec REQs map to a section in this design.
- [x] All locked decisions from proposal `Pre-resolved decisions` are honored.
- [x] No new external dependencies.
- [x] Local SQLite remains source of truth.
- [x] All changes additive — Phase 1 + Phase 2 surfaces unchanged.
- [x] HTTP route order is documented and safe.
- [x] Strict TDD path is clear (test files identified per layer).
- [x] Migration test is required and specified.
- [x] Phase 4 hooks are explicit and don't block this design.
