[CmdletBinding()]
param()

$ProgressPreference = 'SilentlyContinue'
$ErrorActionPreference = 'Stop'

try {
    $codexHome = [Environment]::GetEnvironmentVariable('CODEX_HOME')
    if (-not [string]::IsNullOrWhiteSpace($codexHome)) {
        $codexHome = $codexHome.Replace('/', '\')
    }
    if ([string]::IsNullOrWhiteSpace($codexHome) -or
        ($codexHome -notmatch '^[a-zA-Z]:\\' -and $codexHome -notmatch '^\\\\[^\\]+\\[^\\]+')) {
        $userProfile = [Environment]::GetEnvironmentVariable('USERPROFILE')
        if ([string]::IsNullOrWhiteSpace($userProfile) -or -not [System.IO.Path]::IsPathRooted($userProfile)) { exit 0 }
        $codexHome = Join-Path $userProfile '.codex'
    }

    $configPath = Join-Path $codexHome 'config.toml'
    if (-not [System.IO.File]::Exists($configPath)) { exit 0 }
    $marker = '# engram-windows-hook-command-v1: '
    $reader = [System.IO.File]::OpenText($configPath)
    try {
        $firstLine = $reader.ReadLine()
    }
    finally {
        $reader.Dispose()
    }
    if ($null -eq $firstLine -or -not $firstLine.StartsWith($marker, [System.StringComparison]::Ordinal)) { exit 0 }

    $commandPath = ConvertFrom-Json -InputObject $firstLine.Substring($marker.Length) -ErrorAction Stop
    if ($commandPath -isnot [string] -or -not [System.IO.Path]::IsPathRooted($commandPath) -or
        ($commandPath -notmatch '^[a-zA-Z]:\\' -and $commandPath -notmatch '^\\\\[^\\]+\\[^\\]+\\')) { exit 0 }
    $commandPath = [System.IO.Path]::GetFullPath($commandPath)
    if (-not [string]::Equals([System.IO.Path]::GetExtension($commandPath), '.exe', [System.StringComparison]::OrdinalIgnoreCase) -or
        -not [System.IO.File]::Exists($commandPath)) { exit 0 }

    & $commandPath hook codex-user-prompt-submit
    exit $LASTEXITCODE
}
catch { exit 0 }
