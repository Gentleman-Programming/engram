# Fix #1470: mem_get_observation discards a found observation when project is ambiguous

## Objective
Ensure `mem_get_observation` returns the found observation using its stored project when working in a directory with ambiguous project detection, rather than discarding the observation with an ambiguous_project error.

## Problem
In a working directory that contains multiple git repositories (so project detection returns `ambiguous`), calling `mem_get_observation(id)` loads the observation from the store successfully, but then fails project resolution for the response envelope and returns `ambiguous_project` error, discarding the already retrieved observation.

## Root Cause
In `internal/mcp/mcp.go` (`handleGetObservation`), the observation is fetched by ID via `s.GetObservation(id)`. Then `resolveReadProjectWithProcessOverride` is called. If no explicit project override was provided and `cwd` is ambiguous, `detErr != nil`, causing the handler to discard the observation and return `readProjectErrorResult(activity, detRes, detErr)`.

## Solution
When `projectOverride` is empty and `detErr != nil`, check if the retrieved observation has a stored project (`obs.Project != nil && *obs.Project != ""`). If so, construct `detRes` from the observation's stored project (`SourceObservation = "observation"` or `SourceSessionProject = "session"`) and clear `detErr`, returning the observation successfully.

## Tasks
- [ ] 1. Write failing test in `internal/mcp/mcp_test.go` proving `mem_get_observation` returns the observation when cwd is ambiguous (RED).
- [ ] 2. Implement the fallback in `handleGetObservation` in `internal/mcp/mcp.go` using `obs.Project` (GREEN).
- [ ] 3. Run full MCP test suite (`go test -v ./internal/mcp/...`) and verify no regressions.
- [ ] 4. Commit as conventional commit and open PR referencing issue #1470.
