#!/usr/bin/env pwsh
# Engram - Windows-native UserPromptSubmit hook for Claude Code
#
# Optional fallback for enterprise Windows environments where Git Bash/MSYS2
# fork emulation is slowed or blocked by Defender/EDR. Keep this script small
# and dependency-free; it must never block prompt submission.

# Pi owns its Engram session natively (gentle-engram); stay a silent no-op when
# pi-claude-bridge runs Claude Code inside a Pi turn.
if ($env:PI_CODING_AGENT) { exit 0 }

# Ensure UTF-8 output so JSON payloads with non-ASCII characters are not
# mangled when Claude Code reads this hook's stdout. Without this, Windows
# defaults to the system codepage (e.g. CP1252/CP850) which corrupts
# multi-byte characters in the systemMessage JSON (issue #421).
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
[Console]::InputEncoding  = [System.Text.Encoding]::UTF8

$ErrorActionPreference = 'SilentlyContinue'

function Write-EmptyHookResponse {
  Write-Output '{}'
}

# Emit the first-message ToolSearch bootstrap.
#
# Uses hookSpecificOutput.additionalContext (NOT systemMessage): on a
# UserPromptSubmit hook, only stdout/additionalContext enters the model's
# context. A systemMessage payload is rendered to the terminal as
# "UserPromptSubmit says: ..." (issue #145) and never reaches the model.
#
# The select list carries BOTH tool-name prefixes: mcp__plugin_engram_engram__*
# for plugin/marketplace installs and mcp__engram__* for direct MCP-server
# installs. ToolSearch select: returns whichever names exist and silently
# ignores the rest, so one list covers both install modes (issue #534).
function Write-ToolSearchMessage {
  $message = "CRITICAL FIRST ACTION - Execute this ToolSearch NOW before responding to the user:`nselect:mcp__plugin_engram_engram__mem_save,mcp__plugin_engram_engram__mem_search,mcp__plugin_engram_engram__mem_context,mcp__plugin_engram_engram__mem_session_summary,mcp__plugin_engram_engram__mem_session_start,mcp__plugin_engram_engram__mem_session_end,mcp__plugin_engram_engram__mem_get_observation,mcp__plugin_engram_engram__mem_suggest_topic_key,mcp__plugin_engram_engram__mem_capture_passive,mcp__plugin_engram_engram__mem_save_prompt,mcp__plugin_engram_engram__mem_update,mcp__plugin_engram_engram__mem_current_project,mcp__plugin_engram_engram__mem_judge,mcp__plugin_engram_engram__mem_doctor,mcp__plugin_engram_engram__mem_review,mcp__plugin_engram_engram__mem_pin,mcp__plugin_engram_engram__mem_unpin,mcp__engram__mem_save,mcp__engram__mem_search,mcp__engram__mem_context,mcp__engram__mem_session_summary,mcp__engram__mem_session_start,mcp__engram__mem_session_end,mcp__engram__mem_get_observation,mcp__engram__mem_suggest_topic_key,mcp__engram__mem_capture_passive,mcp__engram__mem_save_prompt,mcp__engram__mem_update,mcp__engram__mem_current_project,mcp__engram__mem_judge,mcp__engram__mem_doctor,mcp__engram__mem_review,mcp__engram__mem_pin,mcp__engram__mem_unpin`n`nAfter loading tools, call mem_context to check for prior session history before responding."
  [PSCustomObject]@{
    hookSpecificOutput = [PSCustomObject]@{
      hookEventName     = 'UserPromptSubmit'
      additionalContext = $message
    }
  } | ConvertTo-Json -Compress
}

<#
.SYNOPSIS
Persists a human Claude prompt through the canonical Engram project.
.DESCRIPTION
Asks the Go decision endpoint to classify and resolve the canonical project in
place of the former current-project read. Invalid or unavailable decisions skip
persistence, without changing original text or the hook bootstrap.
#>
function Invoke-EngramPromptPersist {
  param(
    [string]$EngramUrl,
    [string]$SessionId,
    [string]$Cwd,
    [string]$Prompt
  )
  # Fail-silent and bounded: a short timeout keeps a slow/unreachable server
  # from stalling prompt submission, and any error is swallowed.
  if ([string]::IsNullOrEmpty($Prompt) -or [string]::IsNullOrWhiteSpace($SessionId) -or [string]::IsNullOrWhiteSpace($Cwd)) { return }
  try {
    $request = [PSCustomObject]@{ source = 'claude-code'; cwd = $Cwd; content = $Prompt } | ConvertTo-Json -Compress
    # UseBasicParsing and MaximumRedirection work on Windows PowerShell 5.1.
    # A redirect must not fetch another endpoint or authorize persistence.
    $response = Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -ErrorAction Stop `
      -Method Post -Uri "$EngramUrl/prompts/capture-decision" `
      -ContentType 'application/json; charset=utf-8' -Body ([System.Text.Encoding]::UTF8.GetBytes($request)) -TimeoutSec 1
    if ([int]$response.StatusCode -lt 200 -or [int]$response.StatusCode -ge 300) { return }
    # ConvertFrom-Json can unwrap a single-item array on PowerShell 5.1.
    if ($response.Content -isnot [string] -or -not $response.Content.TrimStart().StartsWith('{', [System.StringComparison]::Ordinal)) { return }
    $resolution = $response.Content | ConvertFrom-Json -ErrorAction Stop
    if ($resolution -isnot [PSCustomObject]) { return }
    $decisionProperty = @($resolution.PSObject.Properties | Where-Object { $_.Name -ceq 'decision' })
    $projectProperty = @($resolution.PSObject.Properties | Where-Object { $_.Name -ceq 'project' })
    $sourceProperty = @($resolution.PSObject.Properties | Where-Object { $_.Name -ceq 'project_source' })
    if ($decisionProperty.Count -ne 1 -or $projectProperty.Count -ne 1 -or $sourceProperty.Count -ne 1 -or
        $decisionProperty[0].Value -isnot [string] -or $decisionProperty[0].Value -cne 'capture' -or
        $projectProperty[0].Value -isnot [string] -or $sourceProperty[0].Value -isnot [string]) { return }
    $project = $projectProperty[0].Value
    # Transport sanity only; Go remains the authority for canonicalization.
    if ($project -match '[/\\\x00-\x1f\x7f]' -or $project -cne $project.Trim()) { return }
    $validSources = @('config', 'git_remote', 'git_root', 'git_child', 'dir_basename', 'process_override')
    if ([string]::IsNullOrWhiteSpace($project) -or $validSources -cnotcontains $sourceProperty[0].Value -or
        $null -ne $resolution.PSObject.Properties['error_hint']) { return }
    $body = [PSCustomObject]@{
      session_id = $SessionId
      project    = $Project
      content    = $Prompt
    } | ConvertTo-Json -Compress
    $null = Invoke-RestMethod -Method Post -Uri "$EngramUrl/prompts" `
      -ContentType 'application/json; charset=utf-8' -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 1
  } catch { }
}

try {
  $engramPort = if ($env:ENGRAM_PORT) { $env:ENGRAM_PORT } else { '7437' }
  $engramUrl  = "http://127.0.0.1:$engramPort"

  $inputJson = [Console]::In.ReadToEnd()
  $payload = $inputJson | ConvertFrom-Json
  $sessionID = [string]($payload.session_id)
  $cwd       = [string]($payload.cwd)
  $prompt    = [string]($payload.prompt)

  if ([string]::IsNullOrWhiteSpace($sessionID)) {
    $sessionID = "windows-$PID"
  }

  # Persist only after canonical server resolution; do not infer a project in
  # the hook when the server is unavailable, invalid, or ambiguous.
  Invoke-EngramPromptPersist -EngramUrl $engramUrl -SessionId $sessionID -Cwd $cwd -Prompt $prompt

  $safeSessionID = $sessionID -replace '[^a-zA-Z0-9_-]', '_'
  $stateFile = Join-Path ([IO.Path]::GetTempPath()) "engram-claude-$safeSessionID-tools-loaded"

  if (-not (Test-Path -LiteralPath $stateFile)) {
    New-Item -ItemType File -Path $stateFile -Force | Out-Null
    Write-ToolSearchMessage
    exit 0
  }

  Write-EmptyHookResponse
  exit 0
} catch {
  Write-EmptyHookResponse
  exit 0
}
