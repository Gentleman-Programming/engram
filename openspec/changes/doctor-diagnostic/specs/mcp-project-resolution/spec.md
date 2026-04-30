# Delta for mcp-project-resolution

## MODIFIED Requirements

### Requirement: REQ-310 Read Tools Accept Optional project Field

The MCP read tools (`mem_search`, `mem_context`, `mem_timeline`, `mem_get_observation`, `mem_stats`, `mem_doctor`) MUST include `project` as an OPTIONAL field in their JSON schema. When `project` is omitted or empty, the handler MUST fall back to auto-detection via `DetectProjectFull`.
(Previously: Optional `project` applied to read tools but did not include `mem_doctor`.)

#### Scenario: Omitted project falls back to auto-detect

- GIVEN a call to `mem_search` with no `project` argument
- WHEN the handler processes the request
- THEN results are scoped to the auto-detected project

#### Scenario: Explicit project is forwarded for validation

- GIVEN a call to `mem_search` with `project: "known-project"`
- WHEN the handler processes the request
- THEN `store.ProjectExists("known-project")` is called before executing the query

#### Scenario: mem_doctor omitted project uses auto-detected scope

- GIVEN a call to `mem_doctor` with no `project` argument
- WHEN the handler processes the request
- THEN diagnostics run against the auto-detected project and return the standard envelope

#### Scenario: mem_doctor explicit project is validated

- GIVEN a call to `mem_doctor` with `project: "known-project"`
- WHEN the handler processes the request
- THEN `store.ProjectExists("known-project")` is validated before running checks
