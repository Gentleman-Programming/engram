# Scheduled explicit cloud sync wrapper (PowerShell) — ALTERNATIVE to native
# autosync. Runs `engram sync --cloud --project <project>` once per explicitly
# named project, continuing through all; nonzero if any project or logging op
# fails. Choose ONE mode: native autosync (recommended) OR this wrapper —
# running both creates redundant overlapping sync. Exit: 0 ok, 1 fail, 2 usage.

[CmdletBinding()]
param(
  [string]$LogPath,
  [Parameter(Position = 0, ValueFromRemainingArguments = $true)]
  [string[]]$Projects
)

$ErrorActionPreference = 'Stop'
$defaultLogName = 'cloud-sync-projects.log'

function Write-Usage {
  @'
Usage: cloud-sync-projects.ps1 [-LogPath <path>] <project> [<project> ...]
Run `engram sync --cloud --project <project>` once per explicitly named project,
in order, continuing through all. Exit 0 if all succeed, 1 if any project sync
or logging op fails, 2 on usage error.
  -LogPath <path>  Append-only log. Overrides default ($ENGRAM_DATA_DIR\
                  cloud-sync-projects.log) and ENGRAM_CLOUD_SYNC_LOG.
  -Help           Show this help.
Env: ENGRAM_DATA_DIR (defaults to ~/.engram); ENGRAM_CLOUD_SYNC_LOG (log override).
'@ | Out-Host
}

# Strip -Help from remaining args.
$helpRequested = $false
$cleanProjects = @()
foreach ($a in $Projects) {
  if ($a -in @('-Help', '--help', '-h')) { $helpRequested = $true } else { $cleanProjects += $a }
}
$Projects = $cleanProjects
if ($helpRequested) { Write-Usage; exit 2 }
if ($Projects.Count -eq 0) {
  [Console]::Error.WriteLine('cloud-sync-projects.ps1: error: at least one project is required')
  [Console]::Error.WriteLine('Run with -Help for usage.')
  exit 2
}

# Log path precedence: -LogPath > ENGRAM_CLOUD_SYNC_LOG > ENGRAM_DATA_DIR default.
$resolvedLog = $LogPath
if ([string]::IsNullOrEmpty($resolvedLog)) { $resolvedLog = $env:ENGRAM_CLOUD_SYNC_LOG }
if ([string]::IsNullOrEmpty($resolvedLog)) {
  $dataDir = if ($env:ENGRAM_DATA_DIR) { $env:ENGRAM_DATA_DIR } else { (Join-Path $HOME '.engram') }
  $resolvedLog = Join-Path $dataDir $defaultLogName
}
$resolvedLog = [System.IO.Path]::GetFullPath($resolvedLog)
$logDir = [System.IO.Path]::GetDirectoryName($resolvedLog)
if (-not (Test-Path -LiteralPath $logDir -PathType Container)) {
  [Console]::Error.WriteLine("cloud-sync-projects.ps1: error: log directory does not exist: $logDir"); exit 2
}

# Timestamped [ts] message to BOTH console and the append-only log; returns
# $false on log write failure so callers aggregate failures.
function Write-LogLine {
  param([string]$Message)
  $line = "[$(Get-Date -Format 'yyyy-MM-ddTHH:mm:sszzz')] $Message"
  try { Add-Content -LiteralPath $resolvedLog -Value $line -Encoding UTF8 -ErrorAction Stop }
  catch { [Console]::Error.WriteLine("cloud-sync-projects.ps1: error: failed to append to log: $resolvedLog"); return $false }
  Write-Host $line
  return $true
}

# Run the verified command for one project via native call operator (safe
# argument tokens), piping combined stdout/stderr through Tee-Object -Append.
# A scoped Continue preference lets ordinary native stderr stream without
# aborting a successful command; Tee-Object -ErrorAction Stop makes log write
# failures terminating. $LASTEXITCODE captured before any later native command.
# Returns the engram exit, or -1 if invoke/tee/logging failed.
function Invoke-Project {
  param([string]$Project)
  if (-not (Write-LogLine "project START project=$Project")) { return -1 }
  $exitCode = 0
  $prevPref = $ErrorActionPreference
  try {
    $ErrorActionPreference = 'Continue'
    & engram sync --cloud --project $Project 2>&1 | Tee-Object -FilePath $resolvedLog -Append -ErrorAction Stop | ForEach-Object { Write-Host $_ }
    $exitCode = $LASTEXITCODE
    if ($null -eq $exitCode) { $exitCode = 0 }
  } catch {
    [Console]::Error.WriteLine("cloud-sync-projects.ps1: error: invoke/tee failed for '$Project': $($_.Exception.Message)")
    return -1
  } finally {
    $ErrorActionPreference = $prevPref
  }
  if ($exitCode -eq 0) { if (-not (Write-LogLine "project SUCCESS project=$Project exit=0")) { return -1 } }
  else { if (-not (Write-LogLine "project FAILURE project=$Project exit=$exitCode")) { return -1 } }
  return $exitCode
}

$overall = 0
if (-not (Write-LogLine "wrapper START projects=$($Projects.Count) log=$resolvedLog")) { $overall = 1 }
foreach ($proj in $Projects) { if ((Invoke-Project -Project $proj) -ne 0) { $overall = 1 } }
if ($overall -eq 0) { if (-not (Write-LogLine 'wrapper END result=success')) { $overall = 1 } }
else { if (-not (Write-LogLine "wrapper END result=failure overall=$overall")) { $overall = 1 } }
exit $overall
