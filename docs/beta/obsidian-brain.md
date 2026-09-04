# 🧠 Obsidian Brain — Beta

> **Status**: Beta — feedback welcome on the [GitHub issues](https://github.com/Gentleman-Programming/engram/issues) tagged `beta:obsidian`.
> **Available since**: `v1.12.0-beta.1`
> **Stability**: Behavior is locked but flag names may evolve before stable release.
> **Side-by-side**: This beta installs as `engram-beta` so it doesn't replace your stable `engram`. Both binaries share the same `~/.engram/engram.db`, so memories captured by your stable agent show up in the beta exports automatically.

Visualize your AI agent's memory as an interactive knowledge graph in [Obsidian](https://obsidian.md/). Every observation becomes a Markdown note. Sessions, projects, and topic clusters become connected hubs. Open Obsidian's Graph View and **see how your agent's brain actually thinks**.

![Brain Graph Preview](https://raw.githubusercontent.com/Gentleman-Programming/engram/main/assets/obsidian-brain-graph.png)

---

## Why This Exists

Engram already stores every decision, bugfix, and architectural insight your agent makes. But that data lives in a SQLite file. You can search it via the CLI, query it via MCP, browse it in the TUI — but you can't **see the connections**.

Obsidian's graph view is the perfect canvas:
- Each memory = a node
- Each session = a hub connecting its observations
- Each `topic_key` cluster = a thematic hub connecting related work across time
- Project boundaries become visual neighborhoods

The result: your agent's knowledge becomes a **navigable cognitive map** instead of a flat database.

---

## Install Side-by-Side (`engram-beta`)

The beta ships as a separate binary named `engram-beta` so it never touches your stable `engram` install. Both binaries read and write the same `~/.engram/engram.db`, so anything captured by your stable agent is immediately visible to the beta.

Pick the right archive for your platform from the [release page](https://github.com/Gentleman-Programming/engram/releases/tag/v1.12.0-beta.1), then extract and rename:

### macOS (Apple Silicon)

```bash
curl -L https://github.com/Gentleman-Programming/engram/releases/download/v1.12.0-beta.1/engram_1.12.0-beta.1_darwin_arm64.tar.gz -o /tmp/engram-beta.tar.gz
tar -xzf /tmp/engram-beta.tar.gz -C /tmp
sudo mv /tmp/engram /usr/local/bin/engram-beta
sudo chmod +x /usr/local/bin/engram-beta
engram-beta version
```

### macOS (Intel)

```bash
curl -L https://github.com/Gentleman-Programming/engram/releases/download/v1.12.0-beta.1/engram_1.12.0-beta.1_darwin_amd64.tar.gz -o /tmp/engram-beta.tar.gz
tar -xzf /tmp/engram-beta.tar.gz -C /tmp
sudo mv /tmp/engram /usr/local/bin/engram-beta
sudo chmod +x /usr/local/bin/engram-beta
engram-beta version
```

### Linux

```bash
curl -L https://github.com/Gentleman-Programming/engram/releases/download/v1.12.0-beta.1/engram_1.12.0-beta.1_linux_amd64.tar.gz -o /tmp/engram-beta.tar.gz
tar -xzf /tmp/engram-beta.tar.gz -C /tmp
sudo mv /tmp/engram /usr/local/bin/engram-beta
sudo chmod +x /usr/local/bin/engram-beta
engram-beta version
```

### From source (`go install`)

```bash
go install github.com/Gentleman-Programming/engram/cmd/engram@v1.12.0-beta.1
mv ~/go/bin/engram ~/go/bin/engram-beta
engram-beta version
```

### Verify both binaries coexist

```bash
which engram        # /usr/local/bin/engram (stable, untouched)
which engram-beta   # /usr/local/bin/engram-beta (the beta)
engram version      # your stable version
engram-beta version # v1.12.0-beta.1
```

### Uninstall

```bash
sudo rm /usr/local/bin/engram-beta
```

The stable `engram`, your `~/.engram/engram.db`, and your Obsidian vault are untouched.

---

## Quick Start (60 seconds)

```bash
# 1. Create a vault folder anywhere
mkdir -p ~/Obsidian/engram

# 2. Export your full memory + bootstrap the engram-brain graph layout
engram-beta obsidian-export --vault ~/Obsidian/engram --graph-config force

# 3. Open Obsidian, "Open folder as vault" → ~/Obsidian/engram
# 4. Cmd+G (Mac) or Ctrl+G → Graph View
```

You should now see hundreds (or thousands) of connected nodes, color-coded by type, clustered by session and topic.

---

## How It Works

```
SQLite (~/.engram/engram.db)
        │
        ▼
 obsidian-export reads via store.Export()
        │
        ▼
 Transform → Markdown files + YAML frontmatter + wikilinks
        │
        ▼
 {vault}/engram/{project}/{type}/{slug}-{id}.md
        │
        ▼
 Obsidian renders the graph from wikilinks
```

Key principle: **the exporter is one-way**. Engram writes to the vault, never reads from it. Your manual edits in Obsidian don't propagate back. If you re-run the export, your edited notes are overwritten with the latest from Engram. This is by design — the vault is a **live mirror**, not a fork.

The exporter only writes inside `{vault}/engram/`. It will never touch other folders in your vault, so you can safely point it at an existing vault that has your own notes.

---

## Vault Structure

```
~/Obsidian/engram/
├── .obsidian/
│   └── graph.json                      ← engram-brain visual config
├── engram/                              ← namespace (everything lives here)
│   ├── .engram-sync-state.json          ← incremental sync state
│   ├── _sessions/                       ← session hub notes
│   │   ├── session-abc123.md
│   │   └── session-def456.md
│   ├── _topics/                         ← topic cluster hubs
│   │   ├── architecture--obsidian-plugin.md
│   │   └── architecture--auth-model.md
│   ├── engram/                          ← project folder
│   │   ├── architecture/
│   │   │   └── added-topic-based-memory-upsert-82.md
│   │   ├── bugfix/
│   │   │   └── fixed-fts5-syntax-error-149.md
│   │   ├── decision/
│   │   ├── discovery/
│   │   ├── pattern/
│   │   └── preference/
│   ├── gentle-ai/
│   │   └── ...
│   └── (one folder per project)
```

### File Format

Each observation becomes a Markdown file with YAML frontmatter:

```markdown
---
id: 1719
type: architecture
project: engram
scope: project
topic_key: architecture/obsidian-plugin/explore
session_id: session-abc123
created_at: "2026-04-06T14:30:00Z"
updated_at: "2026-04-06T14:35:00Z"
revision_count: 1
tags:
  - engram
  - architecture
  - obsidian
aliases:
  - "Exploration: Obsidian plugin for Engram brain visualization"
---

# Exploration: Obsidian plugin for Engram brain visualization

**What**: Complete exploration of Obsidian plugin architecture...
**Why**: User wants to visualize agent memory as connected graph...
**Where**: internal/server/, internal/store/...
**Learned**: Obsidian graph only shows wikilinks between real .md files...

---
*Session*: [[session-session-abc123]]
*Topic*: [[topic-architecture--obsidian-plugin]]
```

The wikilinks at the bottom are what give the graph view its connections. Obsidian indexes them automatically.

### Hub Notes

**Session hubs** (in `_sessions/`) — one per session, contains backlinks to every observation captured in that session. Open one to see the full chronological story of a work session.

**Topic hubs** (in `_topics/`) — one per `topic_key` prefix that has ≥2 observations. Groups related work across time. For example, all `architecture/obsidian-plugin/*` observations cluster under `_topics/architecture--obsidian-plugin.md`. Singletons don't get hubs (prevents orphan clutter).

Slashes in topic keys become `--` in filenames for filesystem safety.

---

## CLI Reference

```bash
engram-beta obsidian-export --vault <path> [flags]
```

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--vault <path>` | ✅ | — | Path to your Obsidian vault root. The exporter writes inside `{vault}/engram/` only. |
| `--project <name>` | No | current project | Export only observations from this known project. |
| `--all` | No | false | Explicitly export observations from every project. |
| `--limit <n>` | No | 0 (no limit) | Maximum observations per source query. |
| `--since <date>` | No | (state file) | Export only observations updated after this date. Format: RFC3339 (`2026-04-06T14:30:00Z`) or `YYYY-MM-DD`. |
| `--force` | No | false | Re-export everything, ignoring the incremental state file. |
| `--graph-config <mode>` | No | `preserve` | Bootstrap the Obsidian graph view config. See [Graph Config](#graph-config) below. |
| `--watch` | No | false | Run as a daemon — exports on a fixed interval until interrupted. |
| `--interval <duration>` | No | `10m` | Sync interval for `--watch` mode. Go duration format (`30s`, `5m`, `1h`). Minimum: `1m`. |

### Examples

```bash
# Single export of the current project
engram-beta obsidian-export --vault ~/Obsidian/engram

# Explicitly export every project
engram-beta obsidian-export --vault ~/Obsidian/engram --all

# Export only the engram project
engram-beta obsidian-export --vault ~/Obsidian/engram --project engram

# Force a complete re-export (useful after pruning observations)
engram-beta obsidian-export --vault ~/Obsidian/engram --force

# Auto-sync every 10 minutes (default), runs forever
engram-beta obsidian-export --vault ~/Obsidian/engram --watch

# Auto-sync every 5 minutes, only the engram project
engram-beta obsidian-export --vault ~/Obsidian/engram --watch --interval 5m --project engram

# Reset the graph layout to the engram-brain default
engram-beta obsidian-export --vault ~/Obsidian/engram --graph-config force

# Never touch the graph config (you have your own custom layout)
engram-beta obsidian-export --vault ~/Obsidian/engram --graph-config skip
```

---

## Auto-Sync Mode

The `--watch` flag turns the export into a long-running daemon. Use cases:
- Keep your vault current while you work
- Run on a server alongside `engram serve` for team-shared brains
- Feed a live dashboard

### Behavior

1. **First cycle runs immediately** — no waiting for the first interval. You see results right away.
2. **Subsequent cycles run on the interval** — every `--interval` duration after the previous cycle completes.
3. **Errors don't kill the daemon** — transient failures are logged and the loop continues.
4. **Graceful shutdown** — `Ctrl+C` (SIGINT) or `SIGTERM` finishes the current cycle and exits cleanly with code 0.
5. **Graph config applies only on first cycle** — subsequent cycles automatically skip graph config writes, so your manual customizations stay safe.

### Cycle Log Format

```
2026/04/06 23:29:29 [2026-04-06T21:29:29Z] sync: created=5 updated=2 deleted=0 skipped=1731 hubs=279
```

Counts: how many notes were created, updated, deleted (soft-deleted obs), skipped (unchanged), and total hub notes regenerated.

### Running Persistent

To keep the daemon running across reboots, use `nohup`, `launchd` (macOS), `systemd` (Linux), or Task Scheduler (Windows).

**macOS quick start with `nohup`**:
```bash
nohup ~/go/bin/engram-beta obsidian-export \
  --vault ~/Obsidian/engram \
  --watch --interval 10m \
  > ~/.engram/obsidian-sync.log 2>&1 &
```

**macOS launchd plist** (`~/Library/LaunchAgents/com.engram-beta.obsidian-sync.plist`):
```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.engram-beta.obsidian-sync</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/engram-beta</string>
        <string>obsidian-export</string>
        <string>--vault</string>
        <string>/Users/YOU/Obsidian/engram</string>
        <string>--watch</string>
        <string>--interval</string>
        <string>10m</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/YOU/.engram/obsidian-sync.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/YOU/.engram/obsidian-sync.log</string>
</dict>
</plist>
```

Then load it:
```bash
launchctl load ~/Library/LaunchAgents/com.engram-beta.obsidian-sync.plist
```

---

## Graph Config

Obsidian's graph view config lives in `{vault}/.obsidian/graph.json`. Engram ships an opinionated default tuned for the engram-brain visual style:

- **6 color groups**: `_sessions`, `_topics`, `#architecture`, `#bugfix`, `#decision`, `#pattern`
- **Forces tuned for cohesion**: `centerStrength: 0.515`, `repelStrength: 12.71`, `linkStrength: 0.729`, `linkDistance: 207`
- **Clean look**: no arrows, no text labels (visible on zoom in)
- **Default node and link sizes**

### Modes

| Mode | Behavior |
|------|----------|
| `preserve` (default) | Writes the default config **only if `graph.json` doesn't exist**. Respects any custom layout you've created. |
| `force` | **Always overwrites** `graph.json` with the engram-brain default. Use this to reset after experimenting. |
| `skip` | **Never touches** `graph.json`. Use this if you have a carefully curated layout you don't want any tooling to manage. |

In `--watch` mode, the graph config is applied **only on the first cycle**. Subsequent cycles always skip it, so daemon mode never clobbers your manual tweaks.

### Color Group Reference

| Query | Color | Hex |
|-------|-------|-----|
| `path:engram/_sessions` | Soft pink | `#E0CBD2` |
| `path:engram/_topics` | Light cyan | `#D3FFFF` |
| `tag:#architecture` | Electric blue | `#001EFF` |
| `tag:#bugfix` | Pure red | `#FF0000` |
| `tag:#decision` | Bright green | `#00FF2A` |
| `tag:#pattern` | Orange | `#FF6800` |

### Customizing the Graph

You can absolutely tweak the graph view in Obsidian's UI — Engram's `preserve` default mode protects your changes. If you want to start fresh later, run `--graph-config force` once and the engram defaults come back.

---

## TypeScript Plugin (Optional)

For an in-Obsidian experience with a ribbon button, settings tab, and status bar indicator, there's a TypeScript community plugin in [`plugin/obsidian/`](https://github.com/Gentleman-Programming/engram/tree/main/plugin/obsidian).

It uses Engram's HTTP API (`engram serve`) instead of reading SQLite directly:

```bash
cd plugin/obsidian
npm install
npm run build

mkdir -p ~/Obsidian/engram/.obsidian/plugins/engram-brain
cp manifest.json main.js ~/Obsidian/engram/.obsidian/plugins/engram-brain/
```

Then in Obsidian: Settings → Community plugins → enable "Engram Brain". Configure the URL to your `engram serve` endpoint (default: `http://127.0.0.1:7437`).

This is **lower priority than the CLI** — the CLI doesn't require `engram serve` to be running, works fully offline, and is the recommended path for daily use.

---

## Tips for the Graph View

### Reading the Graph

- **Bright/large nodes in clusters** = session hubs and topic hubs. They have many backlinks.
- **Dense petals around hubs** = observations belonging to that session/topic.
- **Thin bridges between clusters** = sessions that touched multiple topics, or topics that span multiple projects. **These bridges are where cross-project insight lives.**
- **Orphan nodes at the edges** = memories without a `topic_key` or with very short sessions.

### Useful Filter Queries

In the Filter panel of Graph View:

| Query | Shows |
|-------|-------|
| `path:engram/engram` | Only your engram project's brain |
| `tag:#bugfix` | Constellation of every bug you've ever fixed |
| `tag:#architecture path:engram/engram` | Architectural decisions in this project only |
| `path:_topics` | Just the topic hubs — your "table of contents" |
| `path:_sessions architecture` | Sessions that involved architecture work |
| `tag:#decision tag:#architecture` | Big architectural decisions |

### Navigating

- **Click a node** → opens the note
- **Cmd+click another node** → see the path between two memories
- **Right sidebar → Backlinks** → live view of everything connecting to the current note
- **Cmd+G** → toggle the graph view on/off

---

## Troubleshooting

### "I ran the export but Obsidian shows nothing"
- Check that you opened the **vault root** (`~/Obsidian/engram`), not the inner `engram/engram/` folder
- Refresh: `Cmd+P` → "Reload app without saving"

### "Auto-sync says `--interval requires --watch`"
- The `--interval` flag only works with `--watch`. Either add `--watch` or remove `--interval`.

### "Auto-sync says `--interval must be at least 1m`"
- Minimum interval is 1 minute. This is to prevent hammering your filesystem and SQLite. If you need faster updates, consider running the export in a tight loop with your own scheduler.

### "I customized the graph and now it looks weird"
- Run `engram-beta obsidian-export --vault ~/Obsidian/engram --graph-config force` to reset to the engram-brain default.

### "I want to keep my custom graph layout AND auto-sync"
- This is the default! `--watch` only applies graph config on the first cycle, and the default mode is `preserve` which only writes when the file is missing.

### "The export is slow on huge memory databases"
- The first run is full. Subsequent runs are incremental and fast (only writes changed observations).
- Consider `--project <name>` to scope to one project at a time.

### "Where is the sync state stored?"
- `{vault}/engram/.engram-sync-state.json` — JSON file tracking last export timestamp and per-observation file paths. Safe to delete to force a full re-export, or use `--force`.

---

## Feedback & Bugs

This is a beta feature. **We want to hear how it works for you, especially**:

- Does the graph layout feel right at your scale (100 / 1k / 10k+ observations)?
- Are the color groups useful, or do you wish they were configurable?
- Would embeddings for semantic search and richer graph edges be valuable? (Currently out of scope; topic_key hierarchy is the only relationship signal.)
- Did the daemon mode crash, leak memory, or behave unexpectedly?
- Did the graph config bootstrap clobber something you cared about?

File issues at [github.com/Gentleman-Programming/engram/issues](https://github.com/Gentleman-Programming/engram/issues) with the `beta:obsidian` label.

---

## Roadmap (post-beta)

Once there's feedback signal:

- **Move into stable** — promote docs into main README, remove beta marker
- **Embeddings + semantic edges** — optional `--with-embeddings` flag using local Ollama
- **Bidirectional sync** — let Obsidian edits flow back to Engram (with conflict resolution)
- **Custom hub templates** — user-defined templates for session/topic hub notes
- **Graph layout presets** — multiple curated layouts beyond the engram-brain default
- **`engram serve` integration** — auto-export when the HTTP server is running, no separate daemon needed

If any of those resonate strongly with you, open an issue and it'll get prioritized.
