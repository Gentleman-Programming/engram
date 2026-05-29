# Team Usage Guide

This guide is for teams adopting Engram collaboratively. It explains what `scope: project` and `scope: personal` mean in practice, what language to use for shared memory, and how scope interacts with sync today.

If you are an individual developer using Engram, you can stick to defaults and skip this page.

---

## Mental model: project vs personal scope

Every observation Engram saves has a scope. The two values are:

- **`scope: project`** (default) — memory about the project itself: decisions, bugfixes, conventions, gotchas. Intended to be useful to other teammates and to their AI agents.
- **`scope: personal`** — memory that only matters to you: reading notes, personal shortcuts, style preferences, learning links.

The quick rule of thumb:

> If a teammate's AI agent should find this memory, save it as `project`. If it's only useful to you, save it as `personal`.

### What scope actually does today

Scope is a **search and filter signal**, not a privacy boundary. Concretely:

- `mem_search`, `mem_context`, `GET /search`, and `GET /context` accept a `scope` parameter and filter results accordingly.
- The MCP `mem_save` and the REST `POST /observations` endpoints persist the scope value alongside the observation.
- Agents reading the memory base can choose to focus on shared knowledge (`scope: project`) or your personal workspace (`scope: personal`).

### What scope does NOT do today

Scope **does not filter sync**. Sync operates by **project/session association**, not by scope: when a project is enrolled for cloud sync (or when you run `engram sync` locally), both `project` and `personal` observations of that project are exported and shared with whoever has access to the sync target.

If you need true isolation for personal notes, two workarounds work today:

1. **Use a separate project name for personal notes** (e.g. `myname-notes`) and do not enroll that project for cloud sync.
2. **Do not enroll the shared project** if you save personal observations there.

A future change may add scope-aware sync filtering. Until then, treat scope as a semantic and search convention, not a transport-level guarantee.

---

## What to save in each scope

| Save as `scope: project` | Save as `scope: personal` |
|--------------------------|---------------------------|
| Architectural decisions and tradeoffs | Personal reading notes and learnings |
| Bugfixes that affect other contributors | Editor shortcuts and dotfile tweaks |
| Conventions, naming rules, repo gotchas | Style or workflow preferences |
| API contracts, deployment quirks | Links to articles you want to revisit |
| Onboarding context for new contributors | Personal todos and reminders |

### Examples

**Project scope** (shared with the team):

```
title: "JWT refresh token rotation"
type: decision
scope: project
content: **What**: Rotate refresh tokens on every use, invalidate the
old one immediately. **Why**: prevents replay attacks if a token leaks
in transit. **Where**: src/auth/refresh.ts.
```

**Personal scope** (your own workspace):

```
title: "Postgres EXPLAIN cheatsheet"
type: learning
scope: personal
content: **What**: Quick reference for reading EXPLAIN output.
**Learned**: Sequential scans on a 10k row table are usually fine;
worry when you see them on tables with >100k rows.
```

---

## Language strategy for shared memory

FTS5, the SQLite full-text search engine Engram uses, is language-agnostic but not multilingual. A search in English will not match an observation saved in Spanish, and vice versa. For a globally distributed team, this matters: if every developer saves project-scope memories in their native language, the shared knowledge base fragments and search stops working as a team tool.

### Convention

- **`scope: project`** → save in the team's lingua franca. For most international teams this is English, matching code, commits, and PRs.
- **`scope: personal`** → save in any language you prefer. Nobody else searches your personal scope, so language drift has no cost.

### Why this matters

Picture a project with English, Spanish, Russian, and Japanese speakers. Without a language convention, a developer searches `auth middleware decision` and only finds the entries an English-speaking teammate saved. The Russian developer's equally valuable note titled `решение по auth middleware` stays invisible. The shared knowledge base fragments along language lines.

Picking one language for shared memory keeps the search index coherent. Personal scope stays free for whatever language helps you think.

---

## Adopting these conventions on a team

The mechanics are already in place; what teams need is a written agreement. Three concrete steps:

1. **Document the conventions in your project README.** A short section that says "we save shared memory in English under `scope: project`; personal notes go under `scope: personal`" is enough to align everyone.
2. **Mention scope in code review.** When a memory-related change lands, check whether the saved observations chose the right scope. A misplaced `scope: personal` for a decision that should be team-visible erodes the shared knowledge base over time.
3. **Audit regularly with `mem_context`.** Run `mem_context` with `scope: personal` once in a while to make sure nothing team-relevant slipped into your personal workspace, and vice versa.

---

## Sync behavior today (read this if you collaborate)

Two flows move observations off your machine:

- **Engram Cloud autosync.** When a project is enrolled and autosync is enabled, mutations push to the cloud server. See [`docs/AGENT-SETUP.md`](AGENT-SETUP.md#cloud-autosync-toggle) for the toggle.
- **Local `engram sync`.** Exports project-scoped chunks to `.engram/` for sharing via git or any file-based transport.

Both flows export observations of the target project regardless of their scope. `personal` does not stay local automatically. The recommended pattern today:

- **Shared work** → use a project name everyone on the team uses; enroll it for cloud sync. Save shared knowledge as `scope: project` in the team lingua franca.
- **Truly personal notes** → either (a) save them under a separate project name that you do not enroll, or (b) keep them off Engram entirely.

If your team needs scope-aware sync (personal observations stay on your machine, project observations sync to the team), open an issue describing the policy you want and the data flow you expect. The current contract is "enrolled project means full project export"; changing that needs a deliberate design decision.

---

## Related docs

- [`docs/AGENT-SETUP.md`](AGENT-SETUP.md) — per-agent setup, Memory Protocol, autosync toggle.
- [`docs/ARCHITECTURE.md`](ARCHITECTURE.md) — session lifecycle, topic keys, memory hygiene.
- [`docs/engram-cloud/README.md`](engram-cloud/README.md) — Engram Cloud overview and enrollment.
- [`DOCS.md`](../DOCS.md) — full reference for tools, endpoints, and storage.
