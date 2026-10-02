$ErrorActionPreference = 'SilentlyContinue'
$ProgressPreference = 'SilentlyContinue'

# Forward host input unchanged. Go closes only a previously confirmed binding.
try {
    $rawInput = [Console]::In.ReadToEnd()
    $rawInput | & engram hook codex-session-end *> $null
} catch {
}
exit 0
