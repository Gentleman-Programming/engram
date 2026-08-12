# Scheduled explicit cloud sync wrapper (PowerShell 7) — ALTERNATIVE to native
# autosync. Runs `engram sync --cloud --project <project>` once per explicitly
# named project. Choose ONE mode: native autosync (recommended) OR this wrapper.

[CmdletBinding()]
param(
  [string]$LogPath,
  [Parameter(Position = 0, ValueFromRemainingArguments = $true)]
  [string[]]$Projects
)

$ErrorActionPreference = 'Stop'
$defaultLogName = 'cloud-sync-projects.log'

if ($PSVersionTable.PSVersion.Major -lt 7) {
  [Console]::Error.WriteLine('cloud-sync-projects.ps1: error: PowerShell 7 (pwsh) is required. 5.1 is not supported.')
  exit 2
}

function Write-Usage {
  @'
Usage: cloud-sync-projects.ps1 [-LogPath <path>] <project> [<project> ...]
Run `engram sync --cloud --project <project>` once per explicitly named project,
in order, continuing through all. Exit 0 if all succeed, 1 if any project sync
or logging op fails, 2 on usage error.
  -LogPath <path>  Append-only log. Overrides default and ENGRAM_CLOUD_SYNC_LOG.
  -Help            Show this help.
Requires PowerShell 7 (pwsh); 5.1 is not supported.
'@ | Out-Host
}

# Strip -Help from remaining args.
$helpRequested = $false
$cleanProjects = @()
foreach ($a in $Projects) { if ($a -in @('-Help', '--help', '-h')) { $helpRequested = $true } else { $cleanProjects += $a } }
$Projects = $cleanProjects
if ($helpRequested) { Write-Usage; exit 2 }
if ($Projects.Count -eq 0) {
  [Console]::Error.WriteLine('cloud-sync-projects.ps1: error: at least one project is required'); exit 2
}

# Log path: -LogPath > ENGRAM_CLOUD_SYNC_LOG > ENGRAM_DATA_DIR default.
$resolvedLog = $LogPath
if ([string]::IsNullOrEmpty($resolvedLog)) { $resolvedLog = $env:ENGRAM_CLOUD_SYNC_LOG }
if ([string]::IsNullOrEmpty($resolvedLog)) {
  $dataDir = if ($env:ENGRAM_DATA_DIR) { $env:ENGRAM_DATA_DIR } else { (Join-Path $HOME '.engram') }
  $resolvedLog = Join-Path $dataDir $defaultLogName
}
$resolvedLog = [System.IO.Path]::GetFullPath($resolvedLog)
if (-not (Test-Path -LiteralPath ([System.IO.Path]::GetDirectoryName($resolvedLog)) -PathType Container)) {
  [Console]::Error.WriteLine("cloud-sync-projects.ps1: error: log directory does not exist: $resolvedLog"); exit 2
}

function Write-LogLine {
  param([string]$Message)
  $line = "[$(Get-Date -Format 'yyyy-MM-ddTHH:mm:sszzz')] $Message"
  try { Add-Content -LiteralPath $resolvedLog -Value $line -Encoding UTF8 -ErrorAction Stop }
  catch { [Console]::Error.WriteLine("cloud-sync-projects.ps1: error: failed to append to log: $resolvedLog"); return $false }
  Write-Host $line
  return $true
}

# Run the verified command for one project via native call operator (safe
# argument tokens), Tee-Object -Append. Scoped Continue lets native stderr
# stream without aborting. $LASTEXITCODE captured before any later native cmd.
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
