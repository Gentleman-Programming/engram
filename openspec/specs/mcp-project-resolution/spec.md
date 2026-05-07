# MCP Project Resolution Specification

## Purpose

Authoritative project resolution for MCP tool calls. `mem_save` accepts a validated explicit project selection, while other write tools keep their documented auto-detection behavior. Read tools that expose `project` validate optional overrides. The system exposes detection as a first-class discovery tool and covers cwd, directory, config, git, ambiguous, and basename cases with structured results and error envelopes.

---

## Requirements

### Requirement: REQ-300 DetectProjectFull Contract

The system MUST expose `DetectProjectFull(dir string) DetectionResult` where `DetectionResult` carries `Project string`, `Source string`, `Path string`, `Warning string` (optional), `Error error` (optional), and `AvailableProjects []string` (populated only when `Error` is `ErrAmbiguousProject`).

#### Scenario: Happy path — successful detection

- GIVEN a directory resolvable to a git repo
- WHEN `DetectProjectFull` is called with that directory
- THEN the result has a non-empty `Project`, a non-empty `Source`, a non-empty `Path`, and `Error` is nil

#### Scenario: Ambiguous directory

- GIVEN a directory that is a parent of multiple git repos
- WHEN `DetectProjectFull` is called
- THEN `Error` equals `ErrAmbiguousProject` AND `AvailableProjects` contains one entry per detected child repo AND `Project` is empty

#### Scenario: Zero-repo fallback

- GIVEN a directory with no git repo at or near it
- WHEN `DetectProjectFull` is called
- THEN `Project` equals the basename of the directory AND `Source` equals `"dir_basename"` AND `Error` is nil

---

### Requirement: REQ-301 Case 1 — cwd Is Repo Root (git_remote)

When the working directory is the root of a git repository, the system MUST set `Source` to `"git_remote"` and derive `Project` from the remote URL basename (without `.git` suffix).

#### Scenario: Directory is a git repo root with a remote

- GIVEN a directory that is a git root with a configured remote origin URL
- WHEN `DetectProjectFull` is called
- THEN `Source` is `"git_remote"` AND `Project` matches the repository name from the remote URL AND `Path` equals the directory

#### Scenario: Repo root with no remote configured

- GIVEN a directory that is a git root with no remote
- WHEN `DetectProjectFull` is called
- THEN the system falls through to Case 2 (`git_root` source using directory basename) without error

---

### Requirement: REQ-302 Case 2 — cwd Is Subdirectory of a Repo (git_root)

When the working directory is inside a git repository (but not its root), the system MUST walk up to the repo root, set `Source` to `"git_root"`, and set `Path` to the repo root directory.

#### Scenario: Working directory is a subdirectory

- GIVEN a directory that is two levels deep inside a git repository
- WHEN `DetectProjectFull` is called
- THEN `Source` is `"git_root"` AND `Path` equals the ancestor git root directory AND `Project` is the repo root's basename

#### Scenario: Boundary — git root itself routes to Case 1 first

- GIVEN a directory that is both a git root and has a remote configured
- WHEN `DetectProjectFull` is called
- THEN `Source` is `"git_remote"`, not `"git_root"` (Case 1 takes priority)

---

### Requirement: REQ-303 Case 3 — cwd Is Parent of Exactly One Repo Child (git_child)

When the working directory contains exactly one child directory that is a git repository, the system MUST auto-promote that child, set `Source` to `"git_child"`, populate `Warning` describing the promotion, and set `Project` and `Path` to the child repo's values.

#### Scenario: Single git child found

- GIVEN a directory containing exactly one subdirectory that is a git repository
- WHEN `DetectProjectFull` is called
- THEN `Source` is `"git_child"` AND `Project` equals the child repo's name AND `Warning` is non-empty describing the auto-promotion AND `Error` is nil

#### Scenario: Warning is surfaced in tool response

- GIVEN a cwd that triggers Case 3
- WHEN any MCP write tool is called from that directory
- THEN the tool response includes the `Warning` field with the promotion message

---

### Requirement: REQ-304 Case 4 — cwd Is Parent of Multiple Repo Children (ambiguous_project)

When the working directory contains more than one child directory that is a git repository, the system MUST return `ErrAmbiguousProject` with `AvailableProjects` listing all found child repo names and set `Project` to empty.

#### Scenario: Multiple git children found

- GIVEN a directory containing two or more subdirectory git repositories
- WHEN `DetectProjectFull` is called
- THEN `Error` equals `ErrAmbiguousProject` AND `AvailableProjects` lists every detected child repo AND `Project` is empty string

#### Scenario: Write tool fails fast on ambiguous cwd

- GIVEN a cwd that is a parent of multiple git repos
- WHEN a write tool (e.g., `mem_save`) is called
- THEN the tool returns a structured error with `error_code: "ambiguous_project"` AND `available_projects` populated AND no memory is written

---

### Requirement: REQ-305 Case 5 — cwd Without Any Repos Nearby (dir_basename)

When the working directory is not in or adjacent to any git repository, the system MUST set `Source` to `"dir_basename"` and set `Project` to the basename of the working directory.

#### Scenario: No repos anywhere nearby

- GIVEN a directory with no `.git` folder at any ancestor and no git-repo children
- WHEN `DetectProjectFull` is called
- THEN `Source` is `"dir_basename"` AND `Project` equals `filepath.Base(dir)` AND `Error` is nil AND `Warning` is empty

#### Scenario: Basename used even for unusual directory names

- GIVEN a directory named `my-project-2026` with no git context
- WHEN `DetectProjectFull` is called
- THEN `Project` is `"my-project-2026"`

---

### Requirement: REQ-306 Child Scan Constraints

The child-directory scan MUST operate at depth=1 only, inspect at most 20 directory entries, skip hidden directories and the noise set (`node_modules`, `vendor`, `.venv`, `__pycache__`, `target`, `dist`, `build`), enforce a 200ms wall-clock timeout, and short-circuit as soon as more than 1 git repository is found.

#### Scenario: Scan stops after finding two repos

- GIVEN a directory containing 10 child directories, the first two being git repos
- WHEN `DetectProjectFull` runs the child scan
- THEN the scan stops after confirming 2 repos and returns `ErrAmbiguousProject` without reading the remaining 8 entries

#### Scenario: Noise directories are skipped

- GIVEN a directory containing `node_modules/` (with a `.git` inside) and one legitimate repo
- WHEN `DetectProjectFull` runs the child scan
- THEN `node_modules` is not counted AND the single legitimate repo is auto-promoted (Case 3)

#### Scenario: Hidden directories are skipped

- GIVEN a directory containing `.hidden-repo/` with `.git` and one other repo
- WHEN `DetectProjectFull` runs the child scan
- THEN `.hidden-repo` is not counted AND the visible repo is auto-promoted

#### Scenario: Timeout enforced

- GIVEN a directory scan that would exceed 200ms (simulated via test seam)
- WHEN `DetectProjectFull` runs the child scan
- THEN the function returns before or at the timeout AND falls through to Case 5 (dir_basename)

---

### Requirement: REQ-307 DetectProject Backward-Compatibility Wrapper

`DetectProject(dir string) string` MUST delegate entirely to `DetectProjectFull(dir)` and return `.Project`, preserving the existing CLI contract without duplicating logic.

#### Scenario: Same result as DetectProjectFull.Project

- GIVEN any directory
- WHEN both `DetectProject` and `DetectProjectFull` are called with it
- THEN `DetectProject` returns the same value as `DetectProjectFull(dir).Project`

#### Scenario: Ambiguous cwd returns basename fallback for wrapper compatibility

- GIVEN a directory that triggers `ErrAmbiguousProject`
- WHEN `DetectProject` is called
- THEN it returns the directory basename fallback (the wrapper surfaces no error to the CLI caller)

---

### Requirement: REQ-308 mem_save Validates Explicit Project Selection

`mem_save` MAY expose and accept a `project` field as an explicit project selection, but it MUST validate that selection against known project context before writing. The explicit project MUST NOT create arbitrary buckets.

Validation and precedence rules for `mem_save` are:

- when `session_id` is present and `project` is omitted, use the existing session project.
- when `project` is present, accept it only if it is backed by known store context OR matches the existing session project OR exactly matches the config-resolved cwd project.
- when `project` is missing, unbacked, mismatched with the session project, collides after normalization, or depends on ambiguous cwd resolution without exact recovery context, fail loudly and do not write.
- `mem_save_prompt` keeps its older cwd/default behavior plus ambiguous-project recovery support when applicable.
- other write tools keep their documented auto-detect-only behavior.

#### Scenario: Backed explicit project overrides cwd detection

- GIVEN a call to `mem_save` with an explicit `project` argument that already exists in store context
- WHEN the handler processes the request
- THEN the observation is stored under that validated explicit project AND the response reports `project_source = "explicit_override"`

#### Scenario: session_id without project uses the existing session project

- GIVEN a call to `mem_save` with `session_id` and no `project`
- WHEN the referenced session already has a project association
- THEN the observation is stored under the session project, not the server cwd project

#### Scenario: Unbacked or mismatched explicit project fails loudly

- GIVEN a call to `mem_save` with an explicit `project` that is unknown, mismatched with the session project, or ambiguous after normalization
- WHEN the handler processes the request
- THEN the handler returns a structured error envelope AND no write occurs

#### Scenario: mem_save schema exposes project for validated selection

- GIVEN the MCP tool-list response
- WHEN a client inspects the input schema for `mem_save`
- THEN the schema includes `project` (and ambiguous-recovery metadata) for validated explicit selection

---

### Requirement: REQ-309 Write Tools Fail Fast on ErrAmbiguousProject

When `resolveWriteProject` returns `ErrAmbiguousProject`, write tool handlers MUST return a structured error envelope without writing any data.

#### Scenario: Structured error envelope returned

- GIVEN a cwd that is a parent of multiple git repos
- WHEN `mem_save` is called
- THEN the handler returns `{error: "ambiguous_project", message: "...", available_projects: [...]}` AND the store write is not executed

#### Scenario: Non-ambiguous write succeeds

- GIVEN a cwd that resolves to a single project (any source)
- WHEN `mem_save` is called
- THEN the handler writes the observation AND returns `{..., project, project_source, project_path}` with no error field

---

### Requirement: REQ-310 Read Tools Accept Supported Optional project Field

The MCP read tools that expose project-scoped list/search behavior (`mem_search`, `mem_context`, `mem_timeline`, `mem_stats`, `mem_doctor`) MUST include `project` as an OPTIONAL field in their JSON schema. When `project` is omitted or empty, the handler MUST fall back to auto-detection via `DetectProjectFull`. `mem_get_observation` is ID-based and does not accept a project override; it resolves project only for response metadata and may use a plain-text degraded response when cwd is ambiguous.

#### Scenario: Omitted project falls back to auto-detect

- GIVEN a call to `mem_search` with no `project` argument
- WHEN the handler processes the request
- THEN results are scoped to the auto-detected project

#### Scenario: Explicit project is forwarded for validation by supported read tools

- GIVEN a call to a supported read tool such as `mem_search` with `project: "known-project"`
- WHEN the handler processes the request
- THEN `store.ProjectExists("known-project")` is called before executing the query

---

### Requirement: REQ-311 Read Tools Validate Explicit Project Override

When a read tool receives a non-empty `project`, the handler MUST call `store.ProjectExists`. If the project is unknown, the handler MUST return a structured error with `available_projects`. If known, proceed normally.

#### Scenario: Known project proceeds normally

- GIVEN `project: "engram"` is supplied AND `ProjectExists("engram")` returns true
- WHEN a read tool is called
- THEN the tool executes and returns results scoped to `"engram"`

#### Scenario: Unknown project returns structured error

- GIVEN `project: "does-not-exist"` is supplied AND `ProjectExists` returns false
- WHEN a read tool is called
- THEN the handler returns `{error: "unknown_project", available_projects: [...]}` and no results

---

### Requirement: REQ-312 Admin Tools Bypass Project Resolution

The admin tools (`mem_delete`, `mem_merge_projects`) MUST remain outside the project-resolution contract. They do not accept `project`, do not auto-detect `project`, and operate on their own required identifiers/arguments.

#### Scenario: mem_delete schema omits project

- GIVEN the MCP tool-list response
- WHEN a client inspects the input schema for `mem_delete`
- THEN the schema does not advertise a `project` field

#### Scenario: mem_merge_projects schema omits project

- GIVEN the MCP tool-list response
- WHEN a client inspects the input schema for `mem_merge_projects`
- THEN the schema does not advertise a `project` field

---

### Requirement: REQ-313 mem_current_project Discovery Tool

The system MUST register a new `mem_current_project` tool that returns `{project, project_source, project_path, cwd, available_projects, warning}` for every cwd case, including ambiguous. The tool MUST NOT error — it always returns a result (for ambiguous: `project` empty, `available_projects` populated).

#### Scenario: Normal detection returns full metadata

- GIVEN a cwd that resolves to a single project
- WHEN `mem_current_project` is called
- THEN the response includes non-empty `project`, `project_source`, `project_path`, and the `cwd` used

#### Scenario: Ambiguous cwd returns available_projects without error

- GIVEN a cwd that is a parent of multiple git repos
- WHEN `mem_current_project` is called
- THEN `project` is empty, `available_projects` lists the repo names, and the tool does not return an error-level response

#### Scenario: Warning is included when Case 3 triggers

- GIVEN a cwd auto-promoting a single child repo
- WHEN `mem_current_project` is called
- THEN `warning` is non-empty AND `project_source` is `"git_child"`

---

### Requirement: REQ-314 Standardized Response Envelope

Project-aware MCP responses SHOULD include `project`, `project_source`, and `project_path` fields reflecting the resolved project used. Plain-text admin tools that do not participate in project resolution (`mem_delete`, `mem_merge_projects`), raw diagnostic reports (`mem_doctor`), and documented degraded/plain-text paths such as ambiguous-cwd `mem_get_observation` are exempt. Error responses MUST include `available_projects` whenever the error relates to project resolution.

#### Scenario: Successful project-aware tool response includes project metadata

- GIVEN a successful call to an enveloped project-aware MCP tool (read or write)
- WHEN the response is returned
- THEN `project`, `project_source`, and `project_path` are present at the top level

#### Scenario: Error response includes available_projects when applicable

- GIVEN a call that fails with `ErrAmbiguousProject` or `unknown_project`
- WHEN the error response is returned
- THEN `available_projects` is a non-empty array in the response body

---

### Requirement: REQ-315 store.ProjectExists

`store.Store` MUST expose `ProjectExists(name string) (bool, error)` that returns `true` if the named project has at least one record in any of `observations`, `sessions`, `prompts`, or `enrollment` tables, using a single `UNION ... LIMIT 1` query for efficiency.

#### Scenario: Known project returns true

- GIVEN a project that has observations in the store
- WHEN `ProjectExists` is called with that project's name
- THEN it returns `true, nil`

#### Scenario: Unknown project returns false

- GIVEN a project name not present in any table
- WHEN `ProjectExists` is called
- THEN it returns `false, nil`

#### Scenario: Empty store returns false for any name

- GIVEN a freshly initialized store with no data
- WHEN `ProjectExists` is called with any name
- THEN it returns `false, nil`

---

## Test Seam Summary

| REQ | Test Functions | File |
|-----|---------------|------|
| REQ-300 | `TestDetectProjectFull_ResultShape`, `TestDetectProjectFull_AmbiguousShape`, `TestDetectProjectFull_FallbackShape` | `internal/project/detect_test.go` |
| REQ-301 | `TestDetectProjectFull_Case1_Remote`, `TestDetectProjectFull_Case1_NoRemote` | `internal/project/detect_test.go` |
| REQ-302 | `TestDetectProjectFull_Case2_Subdir`, `TestDetectProjectFull_Case2_RootPriority` | `internal/project/detect_test.go` |
| REQ-303 | `TestDetectProjectFull_Case3_SingleChild`, `TestDetectProjectFull_Case3_WarningPropagated` | `internal/project/detect_test.go`, `internal/mcp/mcp_test.go` |
| REQ-304 | `TestDetectProjectFull_Case4_MultiChild`, `TestMCPWriteTool_AmbiguousError` | `internal/project/detect_test.go`, `internal/mcp/mcp_test.go` |
| REQ-305 | `TestDetectProjectFull_Case5_Basename`, `TestDetectProjectFull_Case5_Unusual` | `internal/project/detect_test.go` |
| REQ-306 | `TestChildScan_ShortCircuit`, `TestChildScan_SkipNoise`, `TestChildScan_SkipHidden`, `TestChildScan_Timeout` | `internal/project/detect_test.go` |
| REQ-307 | `TestDetectProject_MatchesFull`, `TestDetectProject_AmbiguousEmpty` | `internal/project/detect_test.go` |
| REQ-308 | `TestWriteSchema_ProjectFieldOnlyForAmbiguousRecovery`, `TestMemSave_AutoDetectsProject`, `TestMemSave_ExplicitProjectOverridesDetectedProject`, `TestMemSave_UsesSessionProjectWhenProjectOmitted`, `TestMemSave_MissingSessionIDFailsLoudly`, `TestMemSave_ExplicitProjectMustMatchExistingSessionProject`, `TestMemSave_AmbiguousChoiceRequiresExactAvailableProject`, `TestMemSave_ExplicitBackedProjectRejectsAmbiguousNormalizationCollision`, `TestMemSave_ExplicitProjectRejectsCollapsedStoreBucket` | `internal/mcp/mcp_test.go` |
| REQ-309 | `TestMemSave_AmbiguousEnvelope`, `TestMemSave_SuccessEnvelope` | `internal/mcp/mcp_test.go` |
| REQ-310 | `TestMemSearch_NoProjectAutoDetects`, `TestMemSearch_ExplicitProjectForwarded` | `internal/mcp/mcp_test.go` |
| REQ-311 | `TestMemSearch_KnownProjectSucceeds`, `TestMemSearch_UnknownProjectError` | `internal/mcp/mcp_test.go` |
| REQ-312 | `TestAdminToolsSchema_OmitsProject` | `internal/mcp/mcp_test.go` |
| REQ-313 | `TestMemCurrentProject_NormalResult`, `TestMemCurrentProject_AmbiguousNoError`, `TestMemCurrentProject_WarningCase3` | `internal/mcp/mcp_test.go` |
| REQ-314 | `TestAllTools_ReadResponseEnvelope_WithAssertions`, `TestErrorEnvelope_IncludesAvailableProjects` | `internal/mcp/mcp_test.go` |
| REQ-315 | `TestProjectExists_Known`, `TestProjectExists_Unknown`, `TestProjectExists_EmptyStore` | `internal/store/store_test.go` |
