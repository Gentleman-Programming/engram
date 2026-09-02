# MCP Profile Instructions Specification

## Purpose

Define the specification for dynamic MCP server instructions generation, profile synchronization, plugin hook parity, and documentation alignment across tool allowlists.

## Requirements

### Requirement: REQ-MPI-001 Dynamic Instruction Generation & Length Bounds

The system MUST dynamically generate MCP server instructions via `buildServerInstructions(allowlist map[string]bool) string` that list only registered tools.
The system MUST filter CORE tools (`mem_save`, `mem_search`, `mem_context`, `mem_session_summary`, `mem_get_observation`, `mem_save_prompt`, `mem_current_project`, `mem_judge`, `mem_compare`) and DEFERRED tools based on the active allowlist.
The system MUST include the `## CONFLICT SURFACING` section IF AND ONLY IF both `mem_save` and `mem_judge` are registered in the allowlist.
The system MUST ensure that generated instructions across all profile configurations (`nil`/all, `agent`, `admin`, empty, or custom sets) strictly contain fewer than 2048 UTF-8 runes (`utf8.RuneCountInString(instructions) < 2048`).

#### Scenario: Handshake character bounds validation
- GIVEN any valid tool allowlist or profile (`nil`, `ProfileAgent`, `ProfileAdmin`, or custom map)
- WHEN `buildServerInstructions` compiles the instruction text
- THEN the result length in runes MUST be strictly less than 2048.

#### Scenario: Conditional conflict surfacing inclusion
- GIVEN an allowlist containing both `mem_save` and `mem_judge`
- WHEN server instructions are generated
- THEN the output MUST include the `## CONFLICT SURFACING` header and judgment instructions.

#### Scenario: Conditional conflict surfacing exclusion

- GIVEN an allowlist missing either `mem_save` or `mem_judge` (such as `ProfileAdmin` or a custom `mem_judge`-only subset)
- WHEN server instructions are generated
- THEN the output MUST NOT contain `## CONFLICT SURFACING` or `mem_save`-dependent judgment instructions.

### Requirement: REQ-MPI-002 Tool Profile Instruction Correctness

The instructions MUST accurately partition and advertise tools based on the configured profile:
1. When profile is `ProfileAgent` (`--tools=agent`), instructions MUST advertise only the 18 agent tools and MUST NOT advertise any of the 4 admin tools (`mem_stats`, `mem_delete`, `mem_timeline`, `mem_merge_projects`).
2. When profile is `nil` or `--tools=all`, instructions MUST advertise all 22 registered tools.
3. When profile is `ProfileAdmin` (`--tools=admin`), instructions MUST advertise only the 4 admin tools and omit core/conflict agent blocks.
4. When a custom allowlist is provided, instructions MUST advertise only the exact intersection of registered tools.

#### Scenario: Agent profile omits admin tools
- GIVEN an MCP server initialized with `ProfileAgent`
- WHEN `buildServerInstructions` generates the server prompt
- THEN CORE tools MUST contain all registered core tools
- AND DEFERRED tools MUST NOT list `mem_stats`, `mem_delete`, `mem_timeline`, or `mem_merge_projects`.

#### Scenario: Admin profile instructions
- GIVEN an MCP server initialized with `ProfileAdmin`
- WHEN instructions are generated
- THEN DEFERRED tools MUST list `mem_stats`, `mem_delete`, `mem_timeline`, and `mem_merge_projects`
- AND CORE tools and CONFLICT SURFACING MUST be omitted if their tools are absent.

#### Scenario: All/nil profile instructions
- GIVEN an MCP server initialized with a `nil` allowlist (all tools)
- WHEN instructions are generated
- THEN all 22 tools MUST be advertised across CORE and DEFERRED sections.

### Requirement: REQ-MPI-003 Plugin Hook and Skill Parity

Plugin hook scripts (`session-start.sh`, `post-compaction.sh`) and memory skills (`SKILL.md`) for `claude-code` and `codex` MUST reflect the actual agent profile toolset (18 tools). They MUST NOT list admin tools (`mem_stats`, `mem_delete`, `mem_timeline`, `mem_merge_projects`) in deferred tool guidance.

#### Scenario: Plugin hook startup prompt
- GIVEN `plugin/claude-code/scripts/session-start.sh` or `plugin/codex/scripts/session-start.sh`
- WHEN executed or inspected
- THEN DEFERRED tools list MUST NOT contain `mem_stats`, `mem_delete`, `mem_timeline`, or `mem_merge_projects`
- AND CORE tools list MUST include `mem_compare`.
- AND DEFERRED tools list MUST include `mem_doctor` and `mem_capture_passive`.

#### Scenario: Memory skill consistency
- GIVEN `plugin/claude-code/skills/memory/SKILL.md` or `plugin/codex/skills/memory/SKILL.md`
- WHEN loaded by an agent
- THEN tool lists MUST match the 18 agent tools available in `ProfileAgent`.

### Requirement: REQ-MPI-004 Documentation & Comment Alignment

All MCP package comments, setup guides, and plugin documentation MUST consistently state that Engram exposes 18 agent tools and 22 total tools across profiles.

#### Scenario: Documentation verification
- GIVEN project documentation (`docs/AGENT-SETUP.md`, `docs/PLUGINS.md`, `DOCS.md`, and `internal/mcp/mcp.go`)
- WHEN referencing MCP tool numbers
- THEN the documentation MUST state 18 agent tools and 22 total tools.
