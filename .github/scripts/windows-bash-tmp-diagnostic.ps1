$ErrorActionPreference = 'Stop'
$bash = $env:CODEX_TEST_TRUSTED_BASH
$cygpath = 'C:/Program Files/Git/usr/bin/cygpath.exe'
$native = [IO.Path]::GetFullPath($env:RUNNER_TEMP)
if ($bash -ne 'C:/Program Files/Git/usr/bin/bash.exe' -or
    -not (Test-Path -LiteralPath $bash -PathType Leaf) -or
    -not (Test-Path -LiteralPath $cygpath -PathType Leaf) -or
    -not (Test-Path -LiteralPath $native -PathType Container)) {
    throw 'Bash diagnostics require reviewed tools and an existing RUNNER_TEMP directory'
}
$posix = (& $cygpath -u -- $native).Trim()
if ($LASTEXITCODE -ne 0 -or -not $posix.StartsWith('/')) {
    throw 'Bash diagnostics could not resolve the existing runner temporary directory'
}
$probe = @'
printf 'BASH_VERSION=%s\nTMPDIR=%s\nTMP=%s\nTEMP=%s\n' "$BASH_VERSION" "$TMPDIR" "$TMP" "$TEMP"
for flag in e d w; do
  if test "-$flag" /tmp; then printf 'tmp_%s=yes\n' "$flag"; else printf 'tmp_%s=no\n' "$flag"; fi
done
printf 'tmp_native_path='
"$1" -w /tmp
'@
$lines = @()
foreach ($case in @('native-bootstrap', 'posix-launcher', 'native-temp-posix-home')) {
    $homePath = if ($case -eq 'native-bootstrap') { $native } else { $posix }
    $tempPath = if ($case -eq 'posix-launcher') { $posix } else { $native }
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $bash
    $info.WorkingDirectory = $native
    $info.UseShellExecute = $false
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.Environment.Clear()
    foreach ($argument in @('--noprofile', '--norc', '-p', '-c', $probe, 'bash-tmp-diagnostic', $cygpath)) {
        $info.ArgumentList.Add($argument)
    }
    foreach ($key in @('HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'CODEX_HOME', 'CURL_HOME', 'ENGRAM_DATA_DIR', 'ENGRAM_CONFIG_DIR')) {
        $info.Environment[$key] = $homePath
    }
    $info.Environment['TMPDIR'] = if ($case -eq 'native-bootstrap') { $native } else { $posix }
    $info.Environment['TMP'] = $tempPath
    $info.Environment['TEMP'] = $tempPath
    $info.Environment['PATH'] = ''
    $info.Environment['LC_ALL'] = 'C'
    $info.Environment['MSYS_NO_PATHCONV'] = '1'
    $info.Environment['SystemRoot'] = $env:SystemRoot
    $info.Environment['WINDIR'] = $env:SystemRoot
    $info.Environment['OS'] = 'Windows_NT'
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $info
    $record = [ordered]@{ case = $case; exit_code = $null; timed_out = $false; stdout = ''; stderr = ''; native_tmp = $null; native_tmp_directory = $null; native_tmp_error = $null }
    try {
        [void]$process.Start()
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(10000)) {
            $record.timed_out = $true
            $process.Kill($true)
            $process.WaitForExit()
        }
        $record.exit_code = $process.ExitCode
        $record.stdout = $stdout.GetAwaiter().GetResult()
        $record.stderr = $stderr.GetAwaiter().GetResult()
    } catch {
        $record.native_tmp_error = $_.Exception.GetType().Name
    } finally {
        $process.Dispose()
    }
    $match = [regex]::Match($record.stdout, '(?m)^tmp_native_path=([^\r\n]+)')
    if ($match.Success) {
        $record.native_tmp = $match.Groups[1].Value
        try {
            $item = Get-Item -LiteralPath $record.native_tmp -Force -ErrorAction Stop
            $record.native_tmp_directory = $item.PSIsContainer
        } catch {
            $record.native_tmp_error = $_.CategoryInfo.Category.ToString()
        }
    }
    $line = $record | ConvertTo-Json -Compress
    $lines += $line
    Write-Output $line
}
if ($env:GITHUB_STEP_SUMMARY) {
    @('## Read-only Bash temporary-directory diagnostics', 'No directories or tool files were created or changed. These observations are not a passing test baseline.', '```json') + $lines + @('```') |
        Out-File -FilePath $env:GITHUB_STEP_SUMMARY -Append -Encoding utf8
}
