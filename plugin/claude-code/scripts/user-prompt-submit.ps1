#!/usr/bin/env pwsh
# Engram — Windows-native UserPromptSubmit hook for Claude Code
#
# Optional fallback for enterprise Windows environments where Git Bash/MSYS2
# fork emulation is slowed or blocked by Defender/EDR. Keep this script small
# and dependency-free; it must never block prompt submission.

$ErrorActionPreference = 'SilentlyContinue'

function Write-EmptyHookResponse {
  Write-Output '{}'
}

function Write-ToolSearchMessage {
  $message = "CRITICAL FIRST ACTION — Execute this ToolSearch NOW before responding to the user:`nselect:mcp__engram__mem_save,mcp__engram__mem_search,mcp__engram__mem_context,mcp__engram__mem_session_summary,mcp__engram__mem_session_start,mcp__engram__mem_session_end,mcp__engram__mem_get_observation,mcp__engram__mem_suggest_topic_key,mcp__engram__mem_capture_passive,mcp__engram__mem_save_prompt,mcp__engram__mem_update,mcp__engram__mem_current_project,mcp__engram__mem_judge`n`nAfter loading tools, call mem_context to check for prior session history before responding."
  [PSCustomObject]@{ systemMessage = $message } | ConvertTo-Json -Compress
}

try {
  $inputJson = [Console]::In.ReadToEnd()
  $payload = $inputJson | ConvertFrom-Json
  $sessionID = [string]($payload.session_id)

  if ([string]::IsNullOrWhiteSpace($sessionID)) {
    $sessionID = "windows-$PID"
  }

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
