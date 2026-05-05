[← Back to README](../README.md)

# Team Usage

Engram works for both solo workflows and shared team memory. The important thing to understand is that **scope defines who a memory is for**, and sync determines **where that memory travels**.

---

## The Mental Model

- `scope: project` = shared working memory for the project
- `scope: personal` = your private workspace inside Engram

Ask one question before saving:

> **Should a teammate's agent be able to find this later?**

- If **yes** → save it as `project`
- If **no** → save it as `personal`

That is the real boundary. Do not think in terms of “important vs unimportant.” Think in terms of **shared vs private usefulness**.

---

## What `scope: project` Means

Use `project` for information that should help anyone working on the same repository or project context.

Typical examples:

- architecture decisions
- bug root causes
- migration notes
- naming conventions
- deployment gotchas
- team policies
- “why we chose X instead of Y”

If your teammate opens the same project tomorrow, their agent should be able to search and find these memories.

### Rule of thumb

If the memory would be valuable in a PR review, incident response, onboarding session, or future debugging pass, it probably belongs in `project` scope.

---

## What `scope: personal` Means

Use `personal` for information that is useful to **you**, but should not become part of the team's shared memory.

Typical examples:

- your learning notes
- personal reminders
- private drafting or scratch thinking
- your preferred explanation style
- experimental prompts or workflows
- notes that are only relevant on your own machine or setup

This gives you a personal workspace without polluting the project's shared memory.

---

## Scope and Sync

Scope and sync are related, but they are NOT the same concept.

### `project` scope

- Intended for team-visible project memory
- Safe default for shared technical knowledge
- Can be replicated to a shared project sync target when your team syncs Engram data

### `personal` scope

- Intended for your own private memory
- Can still sync across **your own devices** if you use a personal sync workflow
- Should not be mixed into a team-shared project memory repository

In other words:

- **team sync target** → use for shared `project` memories
- **personal sync target** → use for your own `personal` memories across machines

Do not treat `personal` as “throwaway.” It is still durable memory. It is just **not team memory**.

---

## Language Convention for Shared Memory

If your team shares project memory, you need a language convention.

### Recommended convention

- `scope: project` → use the team's **lingua franca**
- `scope: personal` → use any language you prefer

For most globally distributed teams, that means:

- `project` in **English**
- `personal` in **your native language**, if you want

### Why this matters

Engram search is powered by FTS5. It is fast and language-agnostic, but it is **not magically multilingual**.

If one developer saves a shared memory in Spanish and another searches in English, the search may not match the relevant terms. The result is fragmented team memory: the information exists, but teammates cannot reliably find it.

That is why shared project memories should use a common language.

---

## Examples

### Save as `project`

- “Payments webhook retries must stay idempotent because Stripe can replay events.”
- “We keep auth middleware in `internal/http/auth.go`; do not duplicate token parsing in handlers.”
- “Production outage was caused by stale cached feature flags after deploy; flush cache during rollout.”

### Save as `personal`

- “Remember: I understand this package better when I read store code before handlers.”
- “Use Spanish when I draft explanations for myself.”
- “My local Docker setup needs extra cleanup after branch switches.”

If a memory is primarily about **how the project works**, lean toward `project`.
If it is primarily about **how you work**, lean toward `personal`.

---

## Practical Team Policy

If you are adopting Engram in a team, start with this simple policy:

1. Save shared technical knowledge as `scope: project`
2. Save private notes and personal workflows as `scope: personal`
3. Use one shared language for `project` memories
4. Keep team sync and personal sync separated
5. When in doubt, ask: **should another teammate's agent retrieve this?**

That policy is enough for most teams.

---

## Recommended Workflow

### For individuals with multiple devices

- Use `personal` for your private notes
- Sync those notes only through your own personal sync setup
- Use `project` only for knowledge you want available to collaborators

### For teams

- Decide the shared language first
- Treat `project` as the team's memory layer
- Encourage agents and humans to save decisions, bug fixes, and non-obvious discoveries there
- Keep `personal` out of team-shared memory flows

This prevents the two most common failures:

1. **Everything saved as `project`** → shared memory becomes noisy and hard to trust
2. **Everything saved as `personal`** → the team loses the compounding value of shared knowledge

---

## Final Rule

`project` is for **collective memory**.

`personal` is for **individual memory**.

If you keep that distinction clean, Engram scales from one developer on two laptops to a distributed team sharing durable technical context.
