#!/usr/bin/env pwsh
# Engram — Windows-native UserPromptSubmit hook for Claude Code
#
# Optional fallback for enterprise Windows environments where Git Bash/MSYS2
# fork emulation is slowed or blocked by Defender/EDR. Keep this script small
# and dependency-free; it must never block prompt submission.

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

function Write-ToolSearchMessage {
  $message = "CRITICAL FIRST ACTION — Execute this ToolSearch NOW before responding to the user:`nselect:mcp__engram__mem_save,mcp__engram__mem_search,mcp__engram__mem_context,mcp__engram__mem_session_summary,mcp__engram__mem_session_start,mcp__engram__mem_session_end,mcp__engram__mem_get_observation,mcp__engram__mem_suggest_topic_key,mcp__engram__mem_capture_passive,mcp__engram__mem_save_prompt,mcp__engram__mem_update,mcp__engram__mem_current_project,mcp__engram__mem_judge`n`nAfter loading tools, call mem_context to check for prior session history before responding."
  [PSCustomObject]@{ systemMessage = $message } | ConvertTo-Json -Compress
}

function Resolve-EngramProject {
  param(
    [string]$EngramUrl,
    [string]$Cwd
  )
  if ([string]::IsNullOrWhiteSpace($Cwd)) { return $null }
  try {
    $encodedCwd = [System.Uri]::EscapeDataString($Cwd)
    $resolution = Invoke-RestMethod -Method Get -Uri "$EngramUrl/project/current?cwd=$encodedCwd" -TimeoutSec 1
    $projectProperty = @($resolution.PSObject.Properties | Where-Object { $_.Name -ceq 'project' })
    $sourceProperty = @($resolution.PSObject.Properties | Where-Object { $_.Name -ceq 'project_source' })
    if ($projectProperty.Count -ne 1 -or $sourceProperty.Count -ne 1 -or $projectProperty[0].Value -isnot [string] -or $sourceProperty[0].Value -isnot [string]) {
      return $null
    }
    $project = $projectProperty[0].Value
    $source = $sourceProperty[0].Value
    $validSources = @('config', 'git_remote', 'git_root', 'git_child', 'dir_basename', 'process_override')
    if ([string]::IsNullOrWhiteSpace($project) -or $validSources -cnotcontains $source -or $null -ne $resolution.PSObject.Properties['error_hint']) {
      return $null
    }
    return $project
  } catch {
    return $null
  }
}

function Invoke-EngramPromptPersist {
  param(
    [string]$EngramUrl,
    [string]$SessionId,
    [string]$Project,
    [string]$Prompt
  )
  # Fail-silent and bounded: a short timeout keeps a slow/unreachable server
  # from stalling prompt submission, and any error is swallowed.
  if ([string]::IsNullOrWhiteSpace($Prompt) -or [string]::IsNullOrWhiteSpace($SessionId) -or [string]::IsNullOrWhiteSpace($Project)) { return }
  try {
    $body = [PSCustomObject]@{
      session_id = $SessionId
      project    = $Project
      content    = $Prompt
    } | ConvertTo-Json -Compress
    $null = Invoke-RestMethod -Method Post -Uri "$EngramUrl/prompts" `
      -ContentType 'application/json' -Body $body -TimeoutSec 1
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
  $project = Resolve-EngramProject -EngramUrl $engramUrl -Cwd $cwd
  Invoke-EngramPromptPersist -EngramUrl $engramUrl -SessionId $sessionID -Project $project -Prompt $prompt

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
