# Engram — Agent Skills Index

<!-- gla:seguir-regla-trigger -->
## Universal Trigger: "seguir regla" (fuzzy, typos tolerados)

> Directiva Allan Guerrero 2026-04-09: este trigger DEBE respetarse en este agente, sin excepcion.

**Variantes reconocidas** (activan el protocolo GLA completo):
- "seguir regla", "seguir reglas", "sigue regla", "sigue la regla"
- "siguir regla" (typo), "seguire regla" (typo), "seguime la regla"
- "sigueregla", "seguirregla" (sin espacio)
- "follow rule", "apply rules", "activar regla"

**Regla de interpretacion fuzzy**: si el texto del usuario contiene `seguir/sigue/siguir/seguire` cerca de `regla/regia/rela`, activar el protocolo completo.

**Protocolo que se ejecuta** (ver `skills/seguir-regla/SKILL.md` para detalles):
1. Cargar canonical (`_ops/GLA_CANONICAL_RULES_2026-04-09.md`)
2. Leer CLAUDE.md global + hub + proyecto actual
3. Leer BATON.md + ROADMAP.md
4. Cargar Engram context + searches criticos
5. Verificar los 5+ mecanismos de enforcement
6. Lanzar 3 jueces HEALTH en paralelo
7. Consolidar veredictos
8. Reportar estado al usuario

**Skill canonical**: `C:/Users/iUser/repos/claude-workspace/skills/seguir-regla/SKILL.md`

**IMPORTANTE**: este trigger NO es opcional. Ignorarlo = violacion directa de la directiva GLA.
<!-- /gla:seguir-regla-trigger -->

When working on this project, load the relevant skill(s) BEFORE writing any code.

## How to Use

1. Check the trigger column to find skills that match your current task
2. Load the skill by reading the SKILL.md file at the listed path
3. Follow ALL patterns and rules from the loaded skill
4. Multiple skills can apply simultaneously

## Skills

| Skill | Trigger | Path |
|-------|---------|------|
| `engram-architecture-guardrails` | Any change that affects system boundaries, ownership, state flow, or cross-package responsibilities. | [`skills/architecture-guardrails/SKILL.md`](skills/architecture-guardrails/SKILL.md) |
| `engram-branch-pr` | When creating a pull request, opening a PR, or preparing changes for review. | [`skills/branch-pr/SKILL.md`](skills/branch-pr/SKILL.md) |
| `engram-business-rules` | Any change that affects sync behavior, project controls, permissions, or memory semantics. | [`skills/business-rules/SKILL.md`](skills/business-rules/SKILL.md) |
| `engram-commit-hygiene` | Any commit creation, review, or branch cleanup. | [`skills/commit-hygiene/SKILL.md`](skills/commit-hygiene/SKILL.md) |
| `engram-cultural-norms` | Starting substantial work, reviewing changes, or defining team conventions. | [`skills/cultural-norms/SKILL.md`](skills/cultural-norms/SKILL.md) |
| `engram-dashboard-htmx` | Any change to htmx attributes, partial updates, forms, or server-rendered browser UI. | [`skills/dashboard-htmx/SKILL.md`](skills/dashboard-htmx/SKILL.md) |
| `engram-docs-alignment` | Any code or workflow change that affects user or contributor behavior. | [`skills/docs-alignment/SKILL.md`](skills/docs-alignment/SKILL.md) |
| `engram-issue-creation` | When creating a GitHub issue, reporting a bug, or requesting a feature. | [`skills/issue-creation/SKILL.md`](skills/issue-creation/SKILL.md) |
| `engram-memory-protocol` | Decisions, bugfixes, discoveries, preferences, or session closure. | [`skills/memory-protocol/SKILL.md`](skills/memory-protocol/SKILL.md) |
| `engram-plugin-thin` | Changes in plugin scripts/hooks for Claude, OpenCode, Gemini, or Codex. | [`skills/plugin-thin/SKILL.md`](skills/plugin-thin/SKILL.md) |
| `engram-pr-review-deep` | Reviewing any external or internal contribution before merge. | [`skills/pr-review-deep/SKILL.md`](skills/pr-review-deep/SKILL.md) |
| `engram-project-structure` | Creating files, packages, handlers, templates, styles, or tests in this repo. | [`skills/project-structure/SKILL.md`](skills/project-structure/SKILL.md) |
| `engram-sdd-flow` | When user requests SDD or multi-phase implementation planning. | [`skills/sdd-flow/SKILL.md`](skills/sdd-flow/SKILL.md) |
| `engram-server-api` | Any route, handler, payload, or status code modification. | [`skills/server-api/SKILL.md`](skills/server-api/SKILL.md) |
| `engram-testing-coverage` | When implementing behavior changes in any package. | [`skills/testing-coverage/SKILL.md`](skills/testing-coverage/SKILL.md) |
| `engram-tui-quality` | Changes in model, update, view, navigation, or rendering. | [`skills/tui-quality/SKILL.md`](skills/tui-quality/SKILL.md) |
| `engram-ui-elements` | Adding or changing dashboard UI components or connected browsing flows. | [`skills/ui-elements/SKILL.md`](skills/ui-elements/SKILL.md) |
| `engram-visual-language` | Any dashboard styling, typography, spacing, or visual identity change. | [`skills/visual-language/SKILL.md`](skills/visual-language/SKILL.md) |
| `engram-backlog-triage` | Auditing open issues or PRs, triaging the backlog, or reviewing contributor submissions as a maintainer. | [`skills/backlog-triage/SKILL.md`](skills/backlog-triage/SKILL.md) |
| `gentleman-bubbletea` | When editing Go files in installer/internal/tui/, working on TUI screens, or adding new UI features. | [`skills/gentleman-bubbletea/SKILL.md`](skills/gentleman-bubbletea/SKILL.md) |
