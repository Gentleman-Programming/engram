$ErrorActionPreference = 'SilentlyContinue'
$ProgressPreference = 'SilentlyContinue'

try {
    $rawInput = [Console]::In.ReadToEnd()
    if ([string]::IsNullOrWhiteSpace($rawInput)) {
        exit 0
    }

    try {
        $payload = $rawInput | ConvertFrom-Json -ErrorAction Stop
    } catch {
        exit 0
    }

    $sessionID = [string]$payload.session_id
    if ([string]::IsNullOrWhiteSpace($sessionID)) {
        exit 0
    }

    $portText = [Environment]::GetEnvironmentVariable('ENGRAM_PORT')
    if ([string]::IsNullOrWhiteSpace($portText)) {
        $portText = '7437'
    }

    $port = 0
    if ($portText -notmatch '^[0-9]+$' -or -not [int]::TryParse($portText, [ref]$port) -or $port -lt 1 -or $port -gt 65535) {
        exit 0
    }

    $sessionID = [System.Uri]::EscapeDataString($sessionID)
    $uri = "http://127.0.0.1:$port/sessions/$sessionID/end"
    Invoke-WebRequest -Uri $uri -Method Post -ContentType 'application/json' -Body '{}' -UseBasicParsing -TimeoutSec 2 -MaximumRedirection 0 -ErrorAction Stop *> $null
} catch {
}

exit 0
