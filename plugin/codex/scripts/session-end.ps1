$ErrorActionPreference = 'SilentlyContinue'
$ProgressPreference = 'SilentlyContinue'

# Forward host input unchanged. Go closes only a previously confirmed binding.
try {
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [Console]::InputEncoding = $utf8
    [Console]::OutputEncoding = $utf8
    $OutputEncoding = $utf8
    $rawInput = [Console]::In.ReadToEnd()
    $rawInput | & engram hook codex-session-end *> $null
} catch {
}
exit 0
