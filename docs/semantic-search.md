# Explicit local semantic search

Lexical FTS5 search remains the default, including `--match all|any` and
`match_mode: "all"|"any"`. Semantic search is an explicit, local-only alternative.
It returns **potentially related memories**, not proof that a memory answers a
question. Similarity is cosine relatedness, not probability, truth or confidence;
zero and negative scores are valid. There is no universal score threshold.

## Configure the process

Run an already-installed local Ollama daemon with an already-installed embedding
model. Engram never installs Ollama, pulls models, or downloads weights. Set these
variables in the environment of the CLI, HTTP server, or MCP process:

```sh
export ENGRAM_EMBEDDING_MODEL='your-installed-model'
# Optional; this is the default:
export ENGRAM_EMBEDDING_URL='http://127.0.0.1:11434'
```

There is no default model or fixed vector dimension. The URL must be a root HTTP
loopback URL. Remote hosts, proxies, redirects, credentials and cloud-backed
model metadata are rejected. Configuration is machine/process-local, captured at
startup, not project or sync configuration; restart long-running processes after
changing it. Invalid optional configuration does not prevent lexical operation.
No provider requests occur until explicit indexing or semantic querying.

## Build manually, then query explicitly

```sh
engram embed --project my-project --scope project --type decision
# Success is JSON, for example: {"indexed":3}
engram search 'why did we separate storage?' --project my-project \
  --scope project --type decision --search-mode semantic
```

`engram embed [--project PROJECT|--all] [--scope SCOPE] [--type TYPE]` rebuilds
all visible matching observations, not just a limited search page. Omit project
selectors to use the existing current-project resolver; use `--all` explicitly
for all projects. There is no force flag, model pull, background indexing or
indexing side effect of save/search. Index only data you intend to send to your
local daemon. Rebuild with the same visibility filters used by the query.

HTTP, with the same environment on `engram serve`:

```sh
curl 'http://127.0.0.1:7437/search?q=storage+boundaries&project=my-project&scope=project&type=decision&search_mode=semantic'
curl 'http://127.0.0.1:7437/search?q=storage+boundaries&project=my-project&search_mode=semantic&response_format=compact'
```

MCP `mem_search` arguments, with the same environment on `engram mcp`:

```json
{"query":"storage boundaries","project":"my-project","search_mode":"semantic","response_format":"compact"}
```

`search_mode` accepts `lexical` or `semantic`; omission keeps lexical output and
behavior. Do not combine semantic mode with any nonempty `match_mode` or explicit
CLI `--match` (even `all`). Existing project/all-projects, scope, type, limit,
lifecycle, and relation behavior still applies. Full HTTP results remain an
array; compact HTTP and MCP keep their existing envelopes. Only semantic hits
include `similarity` and `retrieval_mode: "semantic"`. No HTTP indexing endpoint
or MCP indexing tool is provided: run `engram embed` manually.

## Failures and recovery

Semantic failures are errors, never lexical fallback or successful empty results:

| Condition | HTTP status | Action |
| --- | --- | --- |
| Invalid mode or semantic/match conflict; invalid input | 400 | Fix arguments; omit match selection for semantic mode |
| Missing/stale index, source/cache drift, changed model identity | 409 | Run `engram embed` with the same filters; retry explicitly |
| Model not configured/installed, invalid URL, cloud-backed model, provider offline | 503 | Configure the variables above; start the local daemon and select an installed local model |
| Invalid provider response or vectors | 502 | Fix the local provider/model, then rebuild manually |
| Deadline exceeded / caller cancellation | 504 / 408 | Retry explicitly only when intended |

CLI failures use stderr and a nonzero exit. MCP failures return tool errors with
configuration/rebuild guidance. Provider bodies and input texts are not copied
into provider diagnostics. Model names and full content digests must agree; a
model replacement or any source change invalidates relevant coverage. Queries
embed only the query, never missing documents. Index data is derived local state,
not cloud/sync data. No hybrid ranking, rescue, ANN, TUI changes or automatic
rebuild is implied.

`GET /health` advertises `capabilities.semantic_search: true` as literal protocol
support, **not provider or index readiness**. An unconfigured server advertises it
too; only an explicit operation determines readiness for its selected corpus.
