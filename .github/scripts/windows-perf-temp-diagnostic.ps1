# Observations only: never change the test process environment or repair /tmp.
$ErrorActionPreference = 'Stop'
$lines = @()
$workspace = [IO.Path]::GetFullPath($env:GITHUB_WORKSPACE)
$owned = Join-Path $workspace ('.runtime-perf-temp-' + [guid]::NewGuid().ToString('N'))
$cygpath = 'C:/Program Files/Git/usr/bin/cygpath.exe'
$mktemp = 'C:/Program Files/Git/usr/bin/mktemp.exe'
$awk = 'C:/Program Files/Git/usr/bin/awk.exe'
$allowedBash = @('C:/Program Files/Git/bin/bash.exe', 'C:/Program Files/Git/usr/bin/bash.exe')
$probe = @'
printf 'BASH_VERSION=%s\nBASH=%s\nTMPDIR=%s\nTMP=%s\nTEMP=%s\n' "$BASH_VERSION" "$BASH" "${TMPDIR-}" "${TMP-}" "${TEMP-}"
for tool in bash mktemp awk cygpath; do printf 'SELECTED_%s=%s\n' "$tool" "$(command -v "$tool")"; done
printf 'TMP_NATIVE=%s\n' "$("$1" -w /tmp)"
[[ "$4" == private-file-control ]] || exit 0
# Execute PATH-selected tools only when their native paths match reviewed payloads.
for tool in mktemp awk; do
  selected="$(command -v "$tool")"
  native="$("$1" -am "$selected")" || exit 0
  expected="$2"; [[ "$tool" == awk ]] && expected="$3"
  if [[ "${native,,}" != "${expected,,}" && "${native,,}.exe" != "${expected,,}" ]]; then
    printf 'tool_admission=unreviewed_%s\n' "$tool"
    exit 0
  fi
done
printf 'tool_admission=reviewed\n'
mktemp --version
awk --version
# An explicit template confines creation even if the default /tmp mapping is bad.
p="$(mktemp "$TMPDIR/perf.XXXXXXXXXX")" || exit $?
printf 'perf-temp-probe\n' > "$p"
printf 'CREATED_NATIVE=%s\n' "$("$1" -w "$p")"
if [[ "$(<"$p")" == perf-temp-probe ]]; then printf 'CREATOR_READ=ok\n'; fi
"$BASH" --noprofile --norc -p -c '[[ "$(<"$1")" == perf-temp-probe ]]' probe "$p"
printf 'CHILD_READ=%s\n' "$?"
awk '$0 == "perf-temp-probe" { found=1 } END { exit !found }' "$p"
printf 'AWK_READ=%s\n' "$?"
'@
try {
    if (-not (Test-Path -LiteralPath $workspace -PathType Container)) { throw 'Existing workspace required' }
    $bash = (Get-Command bash.exe -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source.Replace('\', '/')
    if ($bash -notin $allowedBash) { throw "Unreviewed PATH Bash: $bash" }
    foreach ($path in @($bash, $cygpath, $mktemp, $awk)) {
        $item = Get-Item -LiteralPath $path -ErrorAction Stop
        if ($item.PSIsContainer -or [IO.Path]::GetExtension($path) -ne '.exe' -or
            ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw "Unreviewed diagnostic executable: $path" }
    }
    [void][IO.Directory]::CreateDirectory($owned)
    $posix = (& $cygpath -u -- $owned).Trim()
    if ($LASTEXITCODE -ne 0 -or -not $posix.StartsWith('/')) { throw 'Private path conversion failed' }
    foreach ($case in @('ambient-metadata', 'private-file-control')) {
        $info = [Diagnostics.ProcessStartInfo]::new()
        $info.FileName = $bash
        $info.WorkingDirectory = $owned
        $info.UseShellExecute = $false
        $info.RedirectStandardOutput = $true
        $info.RedirectStandardError = $true
        $info.Environment.Clear()
        foreach ($key in @('PATH', 'SystemRoot', 'WINDIR', 'OS', 'MSYSTEM', 'MSYS_NO_PATHCONV', 'MSYS2_ARG_CONV_EXCL')) {
            $value = [Environment]::GetEnvironmentVariable($key)
            if ($null -ne $value) { $info.Environment[$key] = $value }
        }
        foreach ($key in @('HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME')) { $info.Environment[$key] = $owned }
        foreach ($key in @('TMPDIR', 'TMP', 'TEMP')) {
            $value = [Environment]::GetEnvironmentVariable($key)
            if ($case -eq 'private-file-control') { $value = if ($key -eq 'TMPDIR') { $posix } else { $owned } }
            if ($null -ne $value) { $info.Environment[$key] = $value }
        }
        $info.Environment['LC_ALL'] = 'C'
        foreach ($argument in @('--noprofile', '--norc', '-p', '-c', $probe, 'perf-temp-diagnostic', $cygpath, $mktemp, $awk, $case)) { $info.ArgumentList.Add($argument) }
        $record = [ordered]@{ case=$case; selected_bash=$bash; exit_code=$null; timed_out=$false; stdout=''; stderr=''; native_read=$null; error=$null }
        $process = [Diagnostics.Process]::new()
        $process.StartInfo = $info
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
            $match = [regex]::Match($record.stdout, '(?m)^CREATED_NATIVE=([^\r\n]+)')
            if ($case -eq 'private-file-control' -and $match.Success) {
                $native = [IO.Path]::GetFullPath($match.Groups[1].Value)
                if ($native.StartsWith($owned + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
                    $record.native_read = [IO.File]::ReadAllText($native) -ceq "perf-temp-probe`n"
                } else { $record.error = 'Refused native read outside owned directory' }
            }
        } catch { $record.error = $_.Exception.Message } finally { $process.Dispose() }
        $line = $record | ConvertTo-Json -Compress
        $lines += $line
        Write-Output $line
    }
} catch {
    $line = @{ case='unavailable'; error=$_.Exception.Message } | ConvertTo-Json -Compress
    $lines += $line
    Write-Output $line
}
if ($env:GITHUB_STEP_SUMMARY) {
    @('## PerfRatchet temporary visibility observations', 'Ambient mode creates no files. Private controls retain only owned workspace files; they are not a reproduction or a passing baseline. Raw child streams and exit/timeout results follow.', '```json') + $lines + @('```') |
        Out-File -FilePath $env:GITHUB_STEP_SUMMARY -Append -Encoding utf8
}
