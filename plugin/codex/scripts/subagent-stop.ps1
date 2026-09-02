$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
[Console]::InputEncoding = [System.Text.Encoding]::UTF8

function Write-EmptyHookResponse {
    [Console]::Out.WriteLine('{}')
}

function Write-HookDiagnostic {
    param([string]$Message)

    [Console]::Error.WriteLine("engram: $Message")
}

function Resolve-EngramProject {
    param(
        [string]$EngramUrl,
        [string]$Cwd,
        [int]$TimeoutSec
    )

    if ([string]::IsNullOrWhiteSpace($Cwd)) {
        return $null
    }

    try {
        $encodedCwd = [System.Uri]::EscapeDataString($Cwd)
        $resolution = Invoke-RestMethod -Method Get -Uri "$EngramUrl/project/current?cwd=$encodedCwd" -TimeoutSec $TimeoutSec -MaximumRedirection 0 -ErrorAction Stop
        $projectProperty = @($resolution.PSObject.Properties | Where-Object { $_.Name -ceq 'project' })
        $sourceProperty = @($resolution.PSObject.Properties | Where-Object { $_.Name -ceq 'project_source' })
        $validSources = @('config', 'git_remote', 'git_root', 'git_child', 'dir_basename', 'process_override')

        if ($projectProperty.Count -ne 1 -or $sourceProperty.Count -ne 1 -or
            $projectProperty[0].Value -isnot [string] -or $sourceProperty[0].Value -isnot [string] -or
            [string]::IsNullOrWhiteSpace($projectProperty[0].Value) -or
            $validSources -cnotcontains $sourceProperty[0].Value -or
            $null -ne $resolution.PSObject.Properties['error_hint']) {
            return $null
        }

        return $projectProperty[0].Value
    } catch {
        return $null
    }
}

function Get-RemainingNetworkTimeoutSec {
    param([DateTime]$Deadline)

    $remaining = [Math]::Floor(($Deadline - [DateTime]::UtcNow).TotalSeconds)
    if ($remaining -lt 1) {
        return $null
    }

    return [int]$remaining
}

try {
    $rawInput = [Console]::In.ReadToEnd()
    $payload = $null
    if (-not [string]::IsNullOrWhiteSpace($rawInput)) {
        try {
            $payload = $rawInput | ConvertFrom-Json -ErrorAction Stop
        } catch {
            Write-HookDiagnostic 'invalid SubagentStop input; skipping passive capture'
        }
    }

    if ($null -ne $payload) {
        $output = [string]$payload.last_assistant_message
        if ([string]::IsNullOrEmpty($output)) {
            $output = [string]$payload.stdout
        }

        if (-not [string]::IsNullOrEmpty($output)) {
            $portText = [Environment]::GetEnvironmentVariable('ENGRAM_PORT')
            if ([string]::IsNullOrWhiteSpace($portText)) {
                $portText = '7437'
            }

            $port = 0
            if ($portText -notmatch '^[0-9]+$' -or -not [int]::TryParse($portText, [ref]$port) -or $port -lt 1 -or $port -gt 65535) {
                Write-HookDiagnostic 'invalid ENGRAM_PORT; skipping passive capture'
            } else {
                $engramUrl = "http://127.0.0.1:$port"
                $networkBudgetSec = 7
                $networkDeadline = [DateTime]::UtcNow.AddSeconds($networkBudgetSec)
                $project = Resolve-EngramProject -EngramUrl $engramUrl -Cwd ([string]$payload.cwd) -TimeoutSec 2

                if ([string]::IsNullOrWhiteSpace($project)) {
                    Write-HookDiagnostic 'unable to resolve project; skipping passive capture'
                } else {
                    $body = [PSCustomObject]@{
                        session_id = [string]$payload.session_id
                        content    = $output
                        project    = $project
                        source     = 'subagent-stop'
                    } | ConvertTo-Json -Compress

                    $captureTimeoutSec = Get-RemainingNetworkTimeoutSec -Deadline $networkDeadline
                    if ($null -eq $captureTimeoutSec) {
                        Write-HookDiagnostic 'network budget exhausted; skipping passive capture'
                    } else {
                        try {
                            Invoke-WebRequest -Uri "$engramUrl/observations/passive" -Method Post -ContentType 'application/json' -Body $body -UseBasicParsing -TimeoutSec $captureTimeoutSec -MaximumRedirection 0 -ErrorAction Stop *> $null
                        } catch {
                            Write-HookDiagnostic 'passive capture failed; skipping'
                        }
                    }
                }
            }
        }
    }
} catch {
    Write-HookDiagnostic 'SubagentStop hook failed; skipping passive capture'
} finally {
    Write-EmptyHookResponse
}

exit 0
