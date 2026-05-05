[← Codebase Guide](../CODEBASE-GUIDE.md) | [← Previous: Integrations](integrations.md) | [Next: Reference Map →](reference-map.md)

# Maintainer Playbook

**Use this page when reviewing changes, planning large features, or checking whether product behavior, package ownership, tests, and docs still line up.**

## Main product capabilities

| Capability | What it does | Where it lives |
|---|---|---|
| Proactive save | Agent-written structured memories. | `internal/mcp`, `internal/store` |
| FTS5 search | Retrieves memories by text, type, project, scope. | `internal/store`, `internal/server`, `internal/mcp` |
| Recent context | Session summary and context for a new session. | `internal/store`, `internal/mcp` |
| Prompt capture | Saves user prompts as retrievable context. | `internal/store`, `internal/mcp`, plugins |
| Topic upserts | Updates evolving decisions with `topic_key`. | `internal/store`, `internal/mcp` |
| Conflict surfacing | Detects/judges relationships between memories. | `internal/store/relations.go`, `internal/mcp`, `cmd/engram/conflicts.go` |
| Doctor/repair | Operational diagnostics and guided repair. | `internal/diagnostic`, `cmd/engram/doctor.go`, `docs/DOCTOR.md` |
| Git sync | Chunk export/import in `.engram/`. | `internal/sync` |
| Cloud sync | Per-project replication against cloud. | `internal/cloud/*`, `internal/sync` |
| Autosync | Background push/pull with degraded status. | `internal/cloud/autosync` |
| Dashboard | Web visibility for projects, activity, admin, audit. | `internal/cloud/dashboard`, `internal/cloud/cloudstore` |
| Agent setup | Automated MCP/plugin installation and manual MCP configuration by IDE. | `internal/setup`, `plugin/`, `docs/AGENT-SETUP.md` |
| TUI | Terminal memory navigation. | `internal/tui` |
| Obsidian beta | Experimental export/plugin to Obsidian. | `internal/obsidian`, `plugin/obsidian`, `docs/beta/obsidian-brain.md` |

## How to navigate the repo

### Recommended first pass

1. **Product and commands**: `README.md`.
2. **Full technical reference**: `DOCS.md`.
3. **Existing architecture**: `docs/ARCHITECTURE.md`.
4. **Binary entry point**: `cmd/engram/main.go` and `cmd/engram/cloud.go`.
5. **Data core**: `internal/store/store.go`.
6. **Access surfaces**: `internal/mcp/mcp.go`, `internal/server/server.go`, `internal/tui/`.
7. **Sync and cloud**: `internal/sync/sync.go`, `internal/cloud/*`.
8. **Integrations**: `internal/setup/setup.go`, `plugin/`.
9. **Historical/active specs**: `openspec/changes/` and `openspec/specs/`.

### PR review path

```text
1. What behavior does it claim to change?
2. Which package should own it?
3. Does the change touch the correct source of truth?
4. Is there a test at the affected boundary?
5. Did public docs stay aligned?
6. Does the UI, if any, represent real behavior?
```

## Guardrails that must not break

### Local-first

- Local SQLite is the user's source of truth.
- Cloud is opt-in and project-scoped.
- `engram sync --cloud --project <name>` is explicit; avoid implicit “sync everything”.
- A local client must be able to keep working without cloud.

### Technical boundaries

- Store does not render HTML.
- Dashboard does not write SQL directly when it belongs in cloudstore.
- HTTP handlers do not concentrate SQL + HTML + transport.
- Plugins do not contain core rules.
- Adapters are thin; reusable behavior lives in Go.

### Sync and policies

- Enrollment controls what can sync.
- Cloud pause controls what the organization allows now.
- Blocked sync fails with a visible error and reason code; there is no silent drop.
- If a rule is org-level, it must be enforced server-side.

### Docs

- Docs describe current behavior, not intent.
- Do not duplicate the complete API outside `DOCS.md`.
- If you change an endpoint, command, or setup, update docs in the same change.
- Validate command names, routes, and files before publishing.

## Checklists by change type

### Local store change

- [ ] The rule really belongs in `internal/store`.
- [ ] Migration/schema is covered by existing or new tests.
- [ ] FTS/dedupe/topic/scope/soft delete remain coherent.
- [ ] If it touches sync, mutations are queued or applied correctly.
- [ ] `internal/store/*_test.go` covers the expected flow and edge cases.
- [ ] `DOCS.md#database-schema` is updated if schema or public semantics change.

### MCP tools change

- [ ] The tool uses store as the source of truth.
- [ ] Project resolution respects `.engram/config.json`, cwd, and the `ambiguous_project` flow.
- [ ] The `agent`/`admin` profile remains intentional.
- [ ] Errors return useful envelopes for agents.
- [ ] Tests in `internal/mcp/*_test.go` cover the contract.
- [ ] `docs/AGENT-SETUP.md`, `docs/ARCHITECTURE.md`, or `DOCS.md` are updated if visible behavior changes.

### Local API change

- [ ] The route belongs to `engram serve`, not cloud.
- [ ] `internal/server/server.go` only orchestrates request/response and calls store/services.
- [ ] Status codes and JSON errors are deterministic.
- [ ] Tests in `internal/server/*_test.go` cover errors and success.
- [ ] `DOCS.md#http-api-endpoints` is updated if there is a new/modified public endpoint.

### Sync/cloud change

- [ ] Local SQLite remains the source of truth.
- [ ] Cloud sync is project-scoped.
- [ ] Push and pull are covered if the sync contract changes.
- [ ] Blocks/policies fail loudly with reason code.
- [ ] `internal/cloud/autosync/*_test.go`, `internal/cloud/remote/*_test.go`, `internal/cloud/cloudserver/*_test.go`, or `internal/cloud/cloudstore/*_test.go` cover the affected boundary.
- [ ] Cloud docs (`docs/engram-cloud/*`, `DOCS.md#cloud-cli-opt-in`, `DOCS.md#cloud-autosync`) stay aligned.

### Dashboard change

- [ ] The UI represents real state, not a fake control.
- [ ] Admin policy is enforced in `cloudserver`/`cloudstore`, not only in templ/HTMX.
- [ ] Handlers stay in `internal/cloud/dashboard`.
- [ ] Queries/read model stay in `internal/cloud/cloudstore`.
- [ ] Generated templ components are kept with the change when applicable.
- [ ] Tests cover routes, authentication/session/administration, HTMX partials, and edge cases.

### Plugins/setup change

- [ ] The plugin remains a thin adapter.
- [ ] Core behavior lives in Go, not duplicated shell/TypeScript.
- [ ] Setup is idempotent.
- [ ] Windows/macOS/Linux or documented paths remain correct.
- [ ] `docs/AGENT-SETUP.md` and `docs/PLUGINS.md` reflect the exact current flow.
- [ ] Do not promise automatic cloud bootstrap if it is still CLI-first.

### Documentation change

- [ ] The payoff appears at the start.
- [ ] Current behavior is documented, not roadmap.
- [ ] Paths exist.
- [ ] Commands and routes are current.
- [ ] The complete API reference is not duplicated; link to `DOCS.md`.
- [ ] There is a table/checklist when the reader must decide or act.

## Maintainer hard questions

Before approving architecture, ask:

1. **Who is the source of truth?** If the answer is not local SQLite for local memory, review the design.
2. **Where is the rule enforced?** If it is org-level policy and only exists in UI, it is wrong.
3. **Which boundary changes?** Store/server/cloudstore/cloudserver/dashboard/autosync/plugin.
4. **Is there a boundary test?** Expensive bugs appear between packages, not in isolated helpers.
5. **Did docs stay synchronized?** If the user can see or run it, it must be documented.

## Warning signs

| Smell | Probable problem | Expected correction |
|---|---|---|
| SQL inside dashboard handler | Mixed concerns | Move query to `cloudstore`. |
| Admin toggle that only changes HTML | Fake control | Persist state and enforce in server. |
| Plugin implements dedupe/sync policy | Adapter is too thick | Move to Go. |
| Cloud required for local feature | Breaks local-first | Design local first, cloud after. |
| New endpoint without docs/tests | Invisible contract | Add tests and update `DOCS.md`. |
| Generic helper with hidden coupling | Local cleverness | Place behavior in explicit owner. |

## Quick ownership decision

```text
Does the change affect stored data?       store/cloudstore
Does it affect HTTP input/output?         server/cloudserver
Does it affect browser experience?        dashboard
Does it affect background replication?    autosync
Does it affect remote transport?          remote/cloudserver
Does it affect a specific agent or host?  plugin/setup
Does it affect human commands?            cmd/engram + docs
```

---

[← Previous: Integrations](integrations.md) | [Next: Reference Map →](reference-map.md)
