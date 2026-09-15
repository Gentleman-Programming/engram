[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$HookScript
)

$ProgressPreference = 'SilentlyContinue'
$ErrorActionPreference = 'Stop'

try {
    $approvedScriptNames = @('session-start.sh', 'post-compaction.sh', 'user-prompt-submit.sh')
    $scriptName = [System.IO.Path]::GetFileName($HookScript)
    if ($approvedScriptNames -notcontains $scriptName) {
        exit 0
    }

    $scriptPath = [System.IO.Path]::GetFullPath($HookScript)
    $expectedPath = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot $scriptName))
    if (-not [string]::Equals($scriptPath, $expectedPath, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not [System.IO.File]::Exists($scriptPath)) {
        exit 0
    }

    $git = Get-Command -Name 'git.exe' -CommandType Application -ErrorAction Stop | Select-Object -First 1
    $gitDirectory = [System.IO.DirectoryInfo]::new([System.IO.Path]::GetDirectoryName([System.IO.Path]::GetFullPath($git.Path)))
    $bashPath = $null
    for ($depth = 0; $depth -lt 4 -and $null -ne $gitDirectory; $depth++) {
        foreach ($relativePath in @('bin/bash.exe', 'usr/bin/bash.exe')) {
            $candidate = Join-Path $gitDirectory.FullName $relativePath
            if ([System.IO.File]::Exists($candidate)) {
                $bashPath = $candidate
                break
            }
        }
        if ($null -ne $bashPath) {
            break
        }
        $gitDirectory = $gitDirectory.Parent
    }
    if ($null -eq $bashPath) {
        exit 0
    }

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $bashPath
    $startInfo.Arguments = '--noprofile --norc "' + $scriptPath + '"'
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardInput = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true

    $originalInputEncoding = [Console]::InputEncoding
    try {
        [Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false)
        $process = [System.Diagnostics.Process]::Start($startInfo)
        if ($null -ne $process) {
            $standardInput = $process.StandardInput
        }
    }
    finally {
        [Console]::InputEncoding = $originalInputEncoding
    }
    if ($null -eq $process -or $null -eq $standardInput) {
        exit 0
    }

    $stdinTask = [Console]::OpenStandardInput().CopyToAsync($standardInput.BaseStream)
    $stdoutTask = $process.StandardOutput.BaseStream.CopyToAsync([Console]::OpenStandardOutput())
    $stderrTask = $process.StandardError.BaseStream.CopyToAsync([Console]::OpenStandardError())
    [void]$stdinTask.GetAwaiter().GetResult()
    $standardInput.Close()
    $process.WaitForExit()
    [System.Threading.Tasks.Task]::WaitAll(@($stdoutTask, $stderrTask))
    exit $process.ExitCode
}
catch {
    exit 0
}
