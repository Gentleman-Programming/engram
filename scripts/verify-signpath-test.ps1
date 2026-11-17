param(
    [Parameter(Mandatory = $true)][string]$Directory,
    [Parameter(Mandatory = $true)][string]$ExpectedCertificateSha256
)

$ErrorActionPreference = 'Stop'

function Get-CertificateSha256($Certificate) {
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { return [Convert]::ToHexString($sha.ComputeHash($Certificate.RawData)) }
    finally { $sha.Dispose() }
}

function Write-Stage([string]$Message) {
    # Opt-in diagnostics for locating hangs; silent by default so output stays exact.
    if ($env:SIGNPATH_VERIFY_TRACE -ceq '1') { [Console]::Error.WriteLine("SIGNPATH-STAGE $Message") }
}

function Assert-PinnedChain($Certificate, [string]$Expected) {
    $pinned = [System.Security.Cryptography.X509Certificates.X509Certificate2]::new($Certificate.RawData)
    $chain = [System.Security.Cryptography.X509Certificates.X509Chain]::new()
    try {
        $chain.ChainPolicy.TrustMode = [System.Security.Cryptography.X509Certificates.X509ChainTrustMode]::CustomRootTrust
        $chain.ChainPolicy.CustomTrustStore.AddRange([System.Security.Cryptography.X509Certificates.X509Certificate2[]]@($pinned))
        # Revocation is disabled only because the disposable self-signed chain has
        # no CRL/OCSP endpoint; this also guarantees no network access.
        $chain.ChainPolicy.RevocationMode = [System.Security.Cryptography.X509Certificates.X509RevocationMode]::NoCheck
        $chain.ChainPolicy.ApplicationPolicy.Add([System.Security.Cryptography.Oid]::new('1.3.6.1.5.5.7.3.3')) | Out-Null
        if (-not $chain.Build($pinned)) { throw 'Signer certificate chain is invalid' }
        $root = $chain.ChainElements[$chain.ChainElements.Count - 1].Certificate
        if ((Get-CertificateSha256 $root) -cne $Expected) { throw 'Signer certificate chain is invalid' }
    } finally { $chain.Dispose(); $pinned.Dispose() }
}

# Independent cryptographic check of the embedded PKCS#7: the signature over the
# signed attributes must verify with the pinned signer. The file digest binding is
# proven by Get-AuthenticodeSignature (HashMismatch) above.
function Assert-EmbeddedSignature($Path, [string]$Expected) {
    $bytes = [System.IO.File]::ReadAllBytes($Path)
    $pe = [BitConverter]::ToUInt32($bytes, 0x3c)
    $optional = $pe + 24
    $magic = [BitConverter]::ToUInt16($bytes, $optional)
    $directory = if ($magic -eq 0x20b) { $optional + 112 } elseif ($magic -eq 0x10b) { $optional + 96 } else { throw 'Invalid PE header' }
    $entry = $directory + 32
    $offset = [BitConverter]::ToUInt32($bytes, $entry)
    $size = [BitConverter]::ToUInt32($bytes, $entry + 4)
    if ($offset -eq 0 -or $size -lt 8 -or ([long]$offset + $size) -gt $bytes.Length) { throw 'Missing embedded Authenticode signature' }
    $length = [BitConverter]::ToUInt32($bytes, $offset)
    if ($length -lt 8 -or $length -gt $size) { throw 'Missing embedded Authenticode signature' }
    $blob = [byte[]]::new($length - 8)
    [Array]::Copy($bytes, [long]$offset + 8, $blob, 0, $blob.Length)
    $cms = [System.Security.Cryptography.Pkcs.SignedCms]::new()
    $cms.Decode($blob)
    if ($cms.SignerInfos.Count -ne 1) { throw 'Authenticode signature or file digest is invalid' }
    $signer = $cms.SignerInfos[0]
    if ($null -eq $signer.Certificate -or (Get-CertificateSha256 $signer.Certificate) -cne $Expected) { throw 'Signer certificate SHA256 mismatch' }
    try { $signer.CheckSignature($true) } catch { throw 'Authenticode signature or file digest is invalid' }
}

function Assert-Machine($Path, [int]$ExpectedMachine) {
    $stream = [System.IO.File]::OpenRead($Path)
    $reader = [System.IO.BinaryReader]::new($stream)
    try {
        if ($stream.Length -lt 64 -or $reader.ReadUInt16() -ne 0x5A4D) { throw 'Invalid PE header' }
        $stream.Position = 0x3c
        $offset = $reader.ReadUInt32()
        if ($offset -gt ($stream.Length - 6)) { throw 'Invalid PE offset' }
        $stream.Position = $offset
        if ($reader.ReadUInt32() -ne 0x00004550 -or $reader.ReadUInt16() -ne $ExpectedMachine) {
            throw 'Incorrect PE architecture'
        }
    } finally { $reader.Dispose() }
}

try {
    if ($ExpectedCertificateSha256 -cnotmatch '^[0-9a-fA-F]{64}$') { throw 'Invalid expected certificate SHA256' }
    $expected = $ExpectedCertificateSha256.ToUpperInvariant()
    $machines = [ordered]@{
        'engram-windows-amd64.exe' = 0x8664
        'engram-windows-arm64.exe' = 0xAA64
    }
    $items = @(Get-ChildItem -LiteralPath $Directory -Force)
    if ($items.Count -ne 2) { throw 'Expected exactly two files' }
    foreach ($item in $items) {
        if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -or
            -not ($machines.Keys -ccontains $item.Name)) { throw 'Unexpected file' }
    }
    foreach ($name in $machines.Keys) {
        $path = Join-Path $Directory $name
        Assert-Machine $path $machines[$name]
        Write-Stage "authenticode start: $name"
        $signature = Get-AuthenticodeSignature -LiteralPath $path
        Write-Stage "authenticode end: $name"
        # WinVerifyTrust checks the signed digest before chain policy, so a modified
        # file reports HashMismatch even when the self-signed root is untrusted.
        if ($signature.Status -eq 'HashMismatch') { throw 'Authenticode signature or file digest is invalid' }
        if ($signature.SignatureType -ne 'Authenticode' -or $null -eq $signature.SignerCertificate) { throw 'Missing embedded Authenticode signature' }
        $certificate = $signature.SignerCertificate
        if ((Get-CertificateSha256 $certificate) -cne $expected) { throw 'Signer certificate SHA256 mismatch' }
        # Valid: a trust anchor already exists, owned by someone else. NotTrusted /
        # UnknownError: Windows found no anchor for the pinned self-signed signer,
        # which is expected here; every other status (NotSigned, Incompatible, ...) fails.
        if ($signature.Status -notin @('Valid', 'NotTrusted', 'UnknownError')) { throw 'Authenticode signature or file digest is invalid' }

        # Trust is evaluated in memory only: the pinned public signer is the sole
        # custom root. No certificate store is opened, so nothing can prompt or leak.
        Write-Stage "chain start: $name"
        Assert-PinnedChain $certificate $expected
        Write-Stage "chain end: $name"
        Write-Stage "signature start: $name"
        Assert-EmbeddedSignature $path $expected
        Write-Stage "signature end: $name"
    }
    [Console]::Out.WriteLine('SignPath TEST artifacts verified')
    exit 0
} catch {
    [Console]::Error.WriteLine("SignPath test verification failed: $($_.Exception.Message)")
    exit 1
}
