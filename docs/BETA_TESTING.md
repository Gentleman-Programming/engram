[← Back to README](../README.md)

# Engram Beta — Phases 2 + 3 + 4

Community-testing guide for the new memory-conflict-surfacing features.
Runs in **complete isolation** from your existing engram setup.

---

## What's new in this beta

| Phase | What it adds |
|---|---|
| **2 — Cloud sync of relations** | `memory_relations` (conflict verdicts) now sync across machines. Multi-actor scenarios persist correctly. Server validates relation payloads. |
| **3 — Admin observability** | New `engram conflicts list/show/stats/scan/deferred` CLI + 6 HTTP endpoints under `/conflicts/*` for retroactive audit + scan over existing memories using FTS5. |
| **4 — Semantic LLM-judge** | New `--semantic` scan flag uses your **existing** Claude Code or OpenCode CLI to catch vocabulary-different conflicts (e.g. "Hexagonal" ↔ "Ports and Adapters") that FTS5 alone misses. **$0 if you're on a subscription** (Pro/Max/Plus). |

---

## Why this guide is "isolated"

Everything below uses **non-default ports**, a **separate data dir**, a **separate token**, and the binary is built locally as `./engram-beta`. Your production engram cloud, your `~/.engram/engram.db`, and your installed `engram` binary are **NOT touched**.

Reset anytime with the cleanup section at the end.

---

## Prerequisites

- Docker + Docker Compose
- Go 1.25+ (to build the beta binary — see `go.mod`)
- For Phase 4 testing: `claude` or `opencode` CLI installed and authenticated (whichever you already use)

---

## 1. Clone the branch

```bash
git clone https://github.com/Gentleman-Programming/engram.git engram-beta-repo
cd engram-beta-repo
git checkout feat/memory-conflict-surfacing-cloud-sync
```

---

## 2. Start the isolated cloud

```bash
docker compose -f docker-compose.beta.yml up -d
```

What this starts (all bound to `127.0.0.1` only):
- `engram-beta-postgres` on port **25432** (your prod's 5433 untouched)
- `engram-beta-cloud` on port **28080** (your prod's 18080 untouched)
- Volume `engram-beta-pg` (separate from prod data)

Verify it's up:

```bash
curl -s http://127.0.0.1:28080/health
# Expected: {"status":"ok",...}
```

---

## 3. Build the beta binary

```bash
go build -o ./engram-beta ./cmd/engram
```

A standalone `./engram-beta` in your repo dir. **Does NOT replace your installed `engram`**.

---

## 4. Configure isolated environment

Run these in the same shell where you'll execute the beta commands:

```bash
# Separate data dir — leaves ~/.engram untouched
export ENGRAM_DATA_DIR=/tmp/engram-beta-data
mkdir -p "$ENGRAM_DATA_DIR"

# Point at the beta cloud
export ENGRAM_CLOUD_SERVER=http://127.0.0.1:28080
export ENGRAM_CLOUD_TOKEN=beta-token-CHANGE-ME-please-32chars

# Verify
./engram-beta version
./engram-beta cloud status
```

Expected: cloud status shows `configured=true`, server matches the beta URL.

---

## 5. Test scenarios

### 5.1 Phase 1 baseline — conflict surfacing on save

The CLI `save` syntax is positional: `engram save <title> <content> [flags]`.

```bash
# First memory
./engram-beta save \
  "Use Clean Architecture" \
  "Layers: entities, use cases, adapters." \
  --type architecture --project beta-test

# Conflicting memory
./engram-beta save \
  "Use Hexagonal Architecture" \
  "Ports and adapters separate domain from infra." \
  --type architecture --project beta-test
```

The second `save` should return `candidates[]` with the first memory's id and a `judgment_id`. This is **Phase 1** behavior — base feature already shipped, included here as sanity check.

---

### 5.2 Phase 2 — cloud sync of relations

```bash
# Enroll the project for cloud sync
./engram-beta cloud enroll beta-test

# Sync to beta cloud
./engram-beta sync --cloud --project beta-test

# Check status
./engram-beta cloud status
```

Expected: `phase: healthy`, sync mutations pushed to the beta cloud.

To verify cross-machine: create a 2nd data dir and pull:

```bash
# Simulate a "second machine"
export ENGRAM_DATA_DIR_2=/tmp/engram-beta-data-2
mkdir -p "$ENGRAM_DATA_DIR_2"

ENGRAM_DATA_DIR="$ENGRAM_DATA_DIR_2" ./engram-beta cloud enroll beta-test
ENGRAM_DATA_DIR="$ENGRAM_DATA_DIR_2" ./engram-beta sync --cloud --project beta-test
ENGRAM_DATA_DIR="$ENGRAM_DATA_DIR_2" ./engram-beta search "Architecture" --project beta-test
```

The 2nd "machine" should see the memories synced from the 1st.

---

### 5.3 Phase 3 — admin observability CLI

```bash
# List existing conflicts
./engram-beta conflicts list --project beta-test

# Show stats
./engram-beta conflicts stats --project beta-test

# Retroactive scan (FTS5-based, no LLM yet)
./engram-beta conflicts scan --project beta-test --dry-run
./engram-beta conflicts scan --project beta-test --apply --max-insert 10

# After scan, inspect what got created
./engram-beta conflicts list --project beta-test --status pending

# Drill into a specific relation
./engram-beta conflicts show <relation_id>
```

The HTTP API works too. The `/conflicts/*` routes live on the **local engram serve** (port 7437) — no auth required, localhost-only. Start it in a separate terminal:

```bash
# Terminal 2 (keep the same exported env vars from step 4):
./engram-beta serve
```

Then query from anywhere:

```bash
curl -s "http://127.0.0.1:7437/conflicts?project=beta-test" | jq
curl -s "http://127.0.0.1:7437/conflicts/stats?project=beta-test" | jq
```

Note: this is the **client-side serve** (your local API), not the beta cloud. Cloud sync of relations happens in the background per Phase 2.

---

### 5.4 Phase 4 — semantic LLM-judge (the killer feature)

This is where your **existing** agent CLI does the work. **Your subscription pays $0 extra** — only quota is consumed.

```bash
# Tell engram which agent CLI to use
export ENGRAM_AGENT_CLI=claude   # or opencode

# Run scan with semantic detection
./engram-beta conflicts scan --project beta-test --semantic --apply \
  --max-semantic 5 --concurrency 3 --yes
```

What happens:
1. FTS5 finds candidate pairs (lexical overlap)
2. For each pair, engram shells out to your CLI: `claude -p "Compare these..."` or `opencode run ...`
3. Your agent's LLM returns a verdict (relation + confidence + reasoning)
4. Engram persists positive verdicts as `memory_relations` with `marked_by_kind=system`

After scan:

```bash
./engram-beta conflicts stats --project beta-test
# Should show semantic counters > 0 if any candidates surfaced

./engram-beta conflicts list --project beta-test --status judged
# See the verdicts with reasoning
```

**Test the killer case** — vocabulary-different conflict:

```bash
./engram-beta save \
  "Use Postgres for the user database" \
  "Postgres 15 is our SQL store for users and sessions." \
  --type architecture --project beta-test

./engram-beta save \
  "We migrated to MongoDB last quarter" \
  "Document store now backs the user collection. SQL is gone." \
  --type decision --project beta-test

./engram-beta conflicts scan --project beta-test --semantic --apply \
  --max-semantic 5 --yes
```

Expect `supersedes` or `conflicts_with` verdict because the LLM understands "Postgres → MongoDB" is a real disagreement, even if FTS5 alone wouldn't have flagged them.

---

### 5.5 New MCP tool — `mem_compare` (Phase 4b)

If you have an agent connected via MCP to this beta engram:

```bash
# Point agent at beta engram MCP (in your agent config):
#   command: /path/to/engram-beta-repo/engram-beta
#   args: ["mcp"]
#   env: { ENGRAM_DATA_DIR: /tmp/engram-beta-data }
```

The agent will see `mem_compare(memory_id_a, memory_id_b, relation, confidence, reasoning, [model])` as a new tool.

---

## 6. What to look for / report

- **Phase 2**: did relations sync across data dirs? Any FK errors? Check `/sync/status`'s `deferred_count` and `dead_count`.
- **Phase 3**: does `conflicts list/scan` produce sensible output? Pagination working? Stats accurate?
- **Phase 4**: did your CLI invocation work? Did you get $0 cost (sub) or unexpected charges (API)? Did the LLM verdict make sense for the pair?
- **General**: any latency, hangs, weird logs, missing features.

Report at: https://github.com/Gentleman-Programming/engram/issues (tag `beta-phase-2-3-4`).

---

## 7. Cleanup

```bash
# Stop and DESTROY beta cloud + postgres data
docker compose -f docker-compose.beta.yml down -v

# Remove beta data dirs
rm -rf /tmp/engram-beta-data /tmp/engram-beta-data-2

# Remove the beta binary
rm -f ./engram-beta

# (Optional) Remove the cloned repo
cd .. && rm -rf engram-beta-repo
```

Your production `engram` setup is **fully untouched**. Your prod cloud, your `~/.engram/engram.db`, your `engram` binary — none of those were modified during this test.

---

## Troubleshooting

**`engram-beta cloud status` shows wrong server**
Check `echo $ENGRAM_CLOUD_SERVER` — must point at `http://127.0.0.1:28080`.

**`cloud-beta` container exits with auth error**
The token in `docker-compose.beta.yml` must match `ENGRAM_CLOUD_TOKEN` exported in your shell. Default for both is `beta-token-CHANGE-ME-please-32chars`.

**Phase 4 `--semantic` says "ENGRAM_AGENT_CLI is not set"**
Export it: `export ENGRAM_AGENT_CLI=claude` (or `opencode`). The CLI must be on your `PATH` and authenticated.

**Phase 4 prompt times out**
Default timeout is 60s/call. Increase: `--timeout-per-call 120`. Or reduce concurrency: `--concurrency 2`.

**Port 28080 already in use**
Edit `docker-compose.beta.yml` and change `28080:28080` to a free port (and update `ENGRAM_CLOUD_SERVER` accordingly).
