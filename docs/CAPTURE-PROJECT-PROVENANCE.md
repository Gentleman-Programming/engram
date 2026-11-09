# Local capture project provenance

New local captures can carry optional `project_provenance` evidence:

```json
{
  "source": "git_root",
  "directory": "/work/original-directory",
  "raw_project": "Original Project"
}
```

`source` accepts the existing Go project-detection vocabulary: `git_remote`,
`git_root`, `git_child`, `dir_basename`, `ambiguous`, `explicit_override`,
`session`, `user_selected_after_ambiguous_project`, `request_body`, `config`,
`all_projects`, `process_override`, `stored_project`, `unbound_git`, plus
`unknown`. Missing or empty source means unknown, never explicit designation.
`directory` and `raw_project` are optional strings. Unsupported sources and
non-string known fields are rejected; unknown extra properties are ignored.

## Local HTTP and Store boundaries

The object is additive input to `POST /sessions`, `POST /observations`,
`POST /observations/passive`, and `POST /prompts`. Validation occurs before
capture writes. New session registration/resume and its evidence commit in the
same transaction. Existing ownership, terminal-session, isolation and enrollment
checks still apply. Ending a session does not capture or rewrite this evidence;
extra metadata on end requests is safely ignored.

`GET /sessions/{id}` includes `project_provenance` only when local evidence
exists. Go callers can use `Store.CaptureProjectProvenance(kind, id)` for
`session`, `observation`, or `prompt`; observation/prompt IDs are decimal strings.
A missing result means unknown evidence, not explicit designation.

Evidence lives in the additive local `capture_project_provenance` table, not in
canonical exported/imported rows or Cloud mutation payloads. Existing records
are not backfilled. Existing sessions and idempotent captures do not get
relabelled; omission does not erase known evidence. Observation topic updates
and deduplication preserve the original row's evidence rather than rewriting its
capture origin. This is not a complete event-history ledger.

## CLI and MCP

CLI saves preserve their resolved detection source. Explicit command-line and
process overrides retain their available pre-normalization value. Detectors
that already normalized a name do not invent a raw original.

MCP saves, prompt saves, session summaries, session starts and passive captures
carry the source they resolve. Session-derived writes inherit known local
session source/raw evidence; an old session without evidence remains unknown.
A session association, request-body project, or ownership mode is not proof of
explicit designation. Current capture directory is retained separately from the
canonical project attribution. No new CLI options or MCP tools are introduced.

## Limits

This metadata is caller-supplied local evidence, not authorization or imported
ownership proof. It neither grants Cloud access nor bypasses enrollment, and
changes no project attribution, content or historical ownership. Routing,
aliases, inbox commands, legacy migration and delivery behavior are unchanged.
In particular, this provenance slice does **not** implement inbox routing.
Future routing must interpret uncertain sources conservatively and keep existing
enrolled behavior compatible.
