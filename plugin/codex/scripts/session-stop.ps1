$ErrorActionPreference = 'SilentlyContinue'
$ProgressPreference = 'SilentlyContinue'

function Invoke-EngramSessionEnd {
    $rawInput = [Console]::In.ReadToEnd()
    if ([string]::IsNullOrWhiteSpace($rawInput)) {
        return
    }

    try {
        $payload = $rawInput | ConvertFrom-Json -ErrorAction Stop
    } catch {
        return
    }

    $sessionID = [string]$payload.session_id
    if ([string]::IsNullOrWhiteSpace($sessionID)) {
        return
    }

    $portText = [Environment]::GetEnvironmentVariable('ENGRAM_PORT')
    if ([string]::IsNullOrWhiteSpace($portText)) {
        $portText = '7437'
    }

    $port = 0
    if ($portText -notmatch '^[0-9]+$' -or -not [int]::TryParse($portText, [ref]$port) -or $port -lt 1 -or $port -gt 65535) {
        return
    }

    $sessionID = [System.Uri]::EscapeDataString($sessionID)
    $uri = "http://127.0.0.1:$port/sessions/$sessionID/end"
    Invoke-WebRequest -Uri $uri -Method Post -ContentType 'application/json' -Body '{}' -UseBasicParsing -TimeoutSec 3 -MaximumRedirection 0 -ErrorAction Stop *> $null
}

try {
    Invoke-EngramSessionEnd *> $null
} catch {
}

# Codex validates Stop-hook stdout as JSON on exit 0; empty output is rejected.
[Console]::Out.Write('{"continue":true,"suppressOutput":false}')
exit 0
