# Engram + Claude Code: Semantic Memory with Vector Search

## Overview

Claude Code ships with a built-in memory system: flat Markdown files in an
`autoMemoryDirectory` that get injected into every conversation as context.
This works well for structured knowledge that fits in a few files, but it has
a hard limitation -- search is keyword-only and linear. As memory grows, you
either stuff everything into the context window or lose it.

Engram adds a second memory path: semantic search via embedding vectors.
Instead of relying on exact keyword matches, we can ask "what do we know about
replication topology?" and get back observations that mention failover,
replicas, and primary promotion -- even if none of them contain the literal
phrase "replication topology."

The two systems work together:

- **Native auto-memory** (flat files) -- auto-loaded into every session as
  `system-reminder` context. Good for identity, standing rules, active state.
- **Engram** (vector search via MCP) -- searched on demand. Good for deep
  knowledge, historical decisions, procedures, anything too large to always
  inject.

A PostToolUse hook bridges the two: every time Claude Code writes a memory
file, the hook pushes it into Engram for semantic indexing. One-way sync,
flat files are the source of truth.

## Architecture

```
                         Claude Code Session
                               |
               +---------------+---------------+
               |                               |
        Native Memory                    Engram MCP Tools
        (auto-loaded)                   (searched on demand)
               |                               |
   ~/.claude/unified-memory/*.md        mem_search, mem_save,
               |                        mem_context, mem_get_observation
               |                               |
        PostToolUse Hook               Engram MCP Server (stdio)
        (on Write tool)                        |
               |                        SQLite + FTS5
               v                        + embedding vectors
        Engram HTTP API                        |
        localhost:7437                   Ollama / OpenAI
               |                        (nomic-embed-text)
               v
        SQLite + embeddings
        (same database)
```

There are two distinct Engram processes in this setup:

1. **MCP server** (stdio) -- launched by Claude Code as a child process. This
   is how Claude Code calls `mem_search`, `mem_save`, etc. Runs only during a
   session.

2. **HTTP server** (`engram serve`) -- a long-running process that the
   PostToolUse hook talks to via `curl`. This must be running independently
   for the hook to work.

Both processes share the same SQLite database (`~/.engram/engram.db` by
default), so observations saved via either path are visible to both.

## Prerequisites

- **Go 1.21+** -- to build Engram from source
- **Ollama** with `nomic-embed-text` pulled -- local embedding provider
- **Claude Code** -- with hooks and MCP support

Pull the embedding model if you haven't:

```bash
ollama pull nomic-embed-text
```

Verify Ollama is running:

```bash
curl -s http://localhost:11434/api/tags | python3 -m json.tool
```

## Installation

We use the ScaleDB fork which includes vector search support (upstream PR:
[Gentleman-Programming/engram#139](https://github.com/Gentleman-Programming/engram/pull/139)).

```bash
go install github.com/scaledb-io/engram/cmd/engram@latest
```

Verify the binary:

```bash
engram version
```

The binary lands in `~/go/bin/engram` by default. Make sure `~/go/bin` is in
your `PATH`, or use the full path in configuration below.

## Configuration

Three files need to be edited. All paths below assume macOS with a home
directory of `~`.

### 1. MCP server -- `~/.claude.json`

Add the `engram` entry under `mcpServers`:

```json
{
  "mcpServers": {
    "engram": {
      "command": "/Users/YOUR_USER/go/bin/engram",
      "args": [
        "mcp",
        "--tools=agent",
        "--embedding-provider=ollama",
        "--embedding-model=nomic-embed-text"
      ]
    }
  }
}
```

The `--tools=agent` flag exposes the 11 agent-facing tools (search, save,
context, etc.) and hides the 4 admin tools. Use `--tools=all` if you want
everything.

### 2. Permissions, hook, and auto-memory -- `~/.claude/settings.json`

Add three things to your settings:

**Permissions** -- allow all Engram MCP tools without per-tool prompts:

```json
{
  "permissions": {
    "allow": [
      "mcp__engram"
    ]
  }
}
```

**Auto-memory directory** -- tells Claude Code where to store flat-file
memories:

```json
{
  "autoMemoryDirectory": "~/.claude/unified-memory"
}
```

**PostToolUse hook** -- fires after every `Write` tool call, syncing memory
files to Engram:

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Write",
        "hooks": [
          {
            "type": "command",
            "command": "~/.claude/hooks/sync-memory-file-to-engram.sh",
            "async": true
          }
        ]
      }
    ]
  }
}
```

The `"async": true` is important -- it lets the hook run in the background
without blocking Claude Code's response.

### 3. The PostToolUse hook script -- `~/.claude/hooks/sync-memory-file-to-engram.sh`

Create the hook script:

```bash
#!/bin/bash
# PostToolUse hook for Write tool — syncs memory files to Engram on save.
# Reads hook input JSON from stdin, checks if the written file is in unified-memory/,
# and if so, upserts it into Engram via the MCP server's HTTP API.

INPUT=$(cat)
FILE_PATH=$(echo "$INPUT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('tool_input',{}).get('file_path',''))" 2>/dev/null)

# Only sync files in unified-memory/
case "$FILE_PATH" in
  */.claude/unified-memory/*.md) ;;
  *) exit 0 ;;
esac

BASENAME=$(basename "$FILE_PATH" .md)
[ "$BASENAME" = "MEMORY" ] && exit 0

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
API="http://localhost:$ENGRAM_PORT"

# Check if Engram HTTP server is running — graceful degradation
curl -s "$API/health" > /dev/null 2>&1 || exit 0

TITLE=$(echo "$BASENAME" | sed 's/_/ /g; s/-/ /g')
TOPIC_KEY="memory/$BASENAME"
CONTENT=$(python3 -c "import sys,json; print(json.dumps(sys.stdin.read()))" < "$FILE_PATH")

# Ensure session exists
curl -s -X POST "$API/sessions" \
  -H 'Content-Type: application/json' \
  -d '{"id":"memory-sync","project":"dbre","directory":"'"$HOME"'"}' > /dev/null 2>&1

# Upsert via topic_key — overwrites previous version, no duplicates
curl -s -X POST "$API/observations" \
  -H 'Content-Type: application/json' \
  -d "{
    \"session_id\": \"memory-sync\",
    \"type\": \"reference\",
    \"title\": \"$TITLE\",
    \"content\": $CONTENT,
    \"project\": \"dbre\",
    \"topic_key\": \"$TOPIC_KEY\"
  }" > /dev/null
```

Make it executable:

```bash
chmod +x ~/.claude/hooks/sync-memory-file-to-engram.sh
```

## Seeding Existing Memories

If you already have memory files in `~/.claude/unified-memory/`, use the bulk
sync script to push them all into Engram at once.

Create `~/.claude/sync-memory-to-engram.sh`:

```bash
#!/bin/bash
# Sync unified memory files into Engram as observations.
# Runs via cron or manually. Idempotent — Engram's topic_key upsert
# ensures updates overwrite previous versions, no duplicates.

MEMORY_DIR="$HOME/.claude/unified-memory"
ENGRAM="$HOME/go/bin/engram"
ENGRAM_PORT="${ENGRAM_PORT:-7437}"
API="http://localhost:$ENGRAM_PORT"

# Ensure engram server is running
if ! curl -s "$API/health" > /dev/null 2>&1; then
  echo "[sync] Engram not running, starting..."
  ENGRAM_EMBEDDING_PROVIDER=ollama ENGRAM_EMBEDDING_MODEL=nomic-embed-text \
    "$ENGRAM" serve &
  sleep 2
fi

# Ensure session exists
curl -s -X POST "$API/sessions" \
  -H 'Content-Type: application/json' \
  -d '{"id":"memory-sync","project":"dbre","directory":"'"$HOME"'"}' > /dev/null 2>&1

COUNT=0
for f in "$MEMORY_DIR"/*.md; do
  [ "$(basename "$f")" = "MEMORY.md" ] && continue
  [ ! -f "$f" ] && continue

  BASENAME=$(basename "$f" .md)
  TITLE=$(echo "$BASENAME" | sed 's/_/ /g; s/-/ /g')
  TOPIC_KEY="memory/$BASENAME"
  CONTENT=$(python3 -c "import sys,json; print(json.dumps(sys.stdin.read()))" < "$f")

  curl -s -X POST "$API/observations" \
    -H 'Content-Type: application/json' \
    -d "{
      \"session_id\": \"memory-sync\",
      \"type\": \"reference\",
      \"title\": \"$TITLE\",
      \"content\": $CONTENT,
      \"project\": \"dbre\",
      \"topic_key\": \"$TOPIC_KEY\"
    }" > /dev/null

  COUNT=$((COUNT + 1))
done

echo "[sync] $COUNT memory files synced to Engram"
```

Run it:

```bash
chmod +x ~/.claude/sync-memory-to-engram.sh
~/.claude/sync-memory-to-engram.sh
```

The script is idempotent. It will start Engram's HTTP server if needed, and
the `topic_key` upsert ensures re-runs overwrite rather than duplicate.

The `MEMORY.md` file is skipped intentionally -- it is the auto-memory index
file that Claude Code manages, and its content is a pointer file rather than
substantive knowledge.

## How It Works

The reactive flow when Claude Code saves a memory file:

1. Claude Code calls the **Write** tool to save
   `~/.claude/unified-memory/some-topic.md`
2. The **PostToolUse hook** fires (async, non-blocking)
3. The hook reads the JSON input from stdin to extract the `file_path`
4. It checks: is this path inside `unified-memory/`? If not, exit silently
5. It checks: is the Engram HTTP server running? If not, exit silently
   (graceful degradation -- no errors, no noise)
6. It derives a `topic_key` from the filename: `memory/some-topic`
7. It POSTs to `POST /observations` with the file content and `topic_key`
8. Engram upserts the observation (topic_key dedup), generates an embedding
   vector asynchronously, and stores both in SQLite

Key design decisions:

- **topic_key dedup** -- The `topic_key` field acts as a unique key. If an
  observation with `topic_key=memory/some-topic` already exists, the POST
  replaces it rather than creating a duplicate. This means we can re-sync
  freely without cleanup.

- **Async embedding** -- The embedding vector is generated after the
  observation is saved. If Ollama is slow or temporarily down, the observation
  is still searchable via FTS5 (keyword search). The embedding backfill
  command can fill in gaps later.

- **Graceful degradation** -- If the Engram HTTP server is not running, the
  hook exits silently with code 0. Claude Code never sees an error. Memory
  files still work as native flat-file context. The Engram sync just doesn't
  happen until the server is started.

## Using Engram in Claude Code

Once configured, Claude Code has access to these MCP tools (with the `agent`
profile):

| Tool | Purpose |
|------|---------|
| `mem_search` | Semantic + keyword search across all observations |
| `mem_save` | Save a new observation |
| `mem_context` | Get recent context for the current session |
| `mem_get_observation` | Retrieve a specific observation by ID |
| `mem_capture_passive` | Save a passive observation (lower priority) |
| `mem_save_prompt` | Save a reusable prompt template |
| `mem_session_start` | Start a named session |
| `mem_session_end` | End the current session |
| `mem_session_summary` | Get a summary of the current session |
| `mem_suggest_topic_key` | Suggest a topic_key for dedup |
| `mem_update` | Update an existing observation |

Example queries Claude Code might use:

```
# Search for anything related to MySQL failover
mem_search("mysql failover procedure")

# Find past decisions about Kafka partition counts
mem_search("kafka partition count decision")

# Get context about a specific project
mem_search("opensearch outbound connection", project="dbre")
```

The search is hybrid: if embeddings are available, Engram combines vector
similarity with FTS5 keyword scoring. If no embeddings exist for an
observation (e.g., it was saved before embeddings were configured), it falls
back to FTS5 only.

## Running the Engram HTTP Server

The PostToolUse hook requires the Engram HTTP server to be running. The MCP
server (started by Claude Code) is separate -- it uses stdio transport and
does not expose an HTTP endpoint.

### Option 1: Manual

```bash
ENGRAM_EMBEDDING_PROVIDER=ollama ENGRAM_EMBEDDING_MODEL=nomic-embed-text \
  engram serve
```

Default port is 7437. Override with `ENGRAM_PORT` or pass as an argument:

```bash
engram serve 8080
```

### Option 2: launchd (macOS, persistent)

Create `~/Library/LaunchAgents/io.scaledb.engram.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>io.scaledb.engram</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/YOUR_USER/go/bin/engram</string>
    <string>serve</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>ENGRAM_EMBEDDING_PROVIDER</key>
    <string>ollama</string>
    <key>ENGRAM_EMBEDDING_MODEL</key>
    <string>nomic-embed-text</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/tmp/engram.log</string>
  <key>StandardErrorPath</key>
  <string>/tmp/engram.err</string>
</dict>
</plist>
```

Load it:

```bash
launchctl load ~/Library/LaunchAgents/io.scaledb.engram.plist
```

### Option 3: Background process in shell profile

Add to `~/.zshrc`:

```bash
# Start Engram HTTP server if not already running
if ! curl -s http://localhost:7437/health > /dev/null 2>&1; then
  ENGRAM_EMBEDDING_PROVIDER=ollama ENGRAM_EMBEDDING_MODEL=nomic-embed-text \
    nohup engram serve > /tmp/engram.log 2>&1 &
fi
```

## Embedding Providers

Engram supports two embedding providers. Configuration is via CLI flags or
environment variables (flags take precedence).

### Ollama (local, free)

| Setting | CLI Flag | Env Var | Default |
|---------|----------|---------|---------|
| Provider | `--embedding-provider=ollama` | `ENGRAM_EMBEDDING_PROVIDER` | -- |
| Model | `--embedding-model=nomic-embed-text` | `ENGRAM_EMBEDDING_MODEL` | `nomic-embed-text` |
| URL | `--embedding-url=http://localhost:11434` | `ENGRAM_EMBEDDING_URL` | `http://localhost:11434` |

`nomic-embed-text` produces 768-dimensional vectors. It runs entirely on your
machine with no API calls. Model size is ~275MB.

### OpenAI (cloud)

| Setting | CLI Flag | Env Var | Default |
|---------|----------|---------|---------|
| Provider | `--embedding-provider=openai` | `ENGRAM_EMBEDDING_PROVIDER` | -- |
| Model | `--embedding-model=text-embedding-3-small` | `ENGRAM_EMBEDDING_MODEL` | `text-embedding-3-small` |
| API Key | -- | `ENGRAM_EMBEDDING_API_KEY` | -- (required) |

`text-embedding-3-small` produces 1536-dimensional vectors. Requires an
OpenAI API key.

### MaxChars truncation

Each provider reports a `MaxChars()` limit. Text exceeding this limit is
truncated before embedding. For `nomic-embed-text`, this corresponds to
roughly 8,192 tokens (~30K characters). Long documents will be truncated
silently -- the full text is still stored and searchable via FTS5, but only
the first portion is embedded for vector search.

## Backfilling Embeddings

If you have existing observations in Engram that were saved before embeddings
were configured (or if Ollama was down when they were saved), use the backfill
command:

```bash
engram backfill-embeddings \
  --embedding-provider=ollama \
  --embedding-model=nomic-embed-text
```

Options:

| Flag | Default | Purpose |
|------|---------|---------|
| `--embedding-provider` | (from env) | Which provider to use |
| `--embedding-model` | (from env) | Which model to use |
| `--embedding-url` | (from env) | Provider URL |
| `--batch-size=N` | 50 | Observations per batch |

The command processes only observations that lack embeddings. It is safe to
run multiple times -- already-embedded observations are skipped.

## Limitations

1. **Two processes required** -- The MCP server (stdio, launched by Claude
   Code) handles in-session search and save. The HTTP server (`engram serve`)
   handles the PostToolUse hook sync. Both must be running for the full
   experience. If only the MCP server is running, Claude Code can still
   search and save directly -- the hook sync just won't work.

2. **One-way sync** -- Changes flow from flat files to Engram, not the other
   way. If Claude Code saves something via `mem_save` directly (bypassing
   flat files), it lives only in Engram. The flat files are the source of
   truth for auto-loaded context.

3. **nomic-embed-text context limit** -- The model has an 8K token context
   window (~30K characters). Longer documents are truncated before embedding.
   The full text is still stored and keyword-searchable, but vector search
   only covers the first portion.

4. **Ollama must be running** -- If Ollama is down when an observation is
   saved, the text is stored but no embedding is generated. Use
   `engram backfill-embeddings` to fill gaps after Ollama is back.

5. **Shared SQLite database** -- Both the MCP server and HTTP server access
   the same `~/.engram/engram.db`. SQLite handles concurrent readers well,
   but write contention is possible under heavy load. In practice, this is
   not an issue for memory workloads.

6. **Hook only fires on Write** -- The PostToolUse hook is bound to the
   `Write` tool matcher. If a memory file is edited outside Claude Code
   (e.g., manually in an editor), it won't be synced until the next bulk
   sync or until Claude Code writes to it.

---

Upstream PR: [Gentleman-Programming/engram#139](https://github.com/Gentleman-Programming/engram/pull/139) --
adds the embedding provider interface, Ollama + OpenAI implementations,
hybrid vector + FTS5 search, and the `backfill-embeddings` command.
