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
        $signature = Get-AuthenticodeSignature -LiteralPath $path
        if ($signature.Status -eq 'HashMismatch') { throw 'Authenticode signature or file digest is invalid' }
        if ($signature.SignatureType -ne 'Authenticode' -or $null -eq $signature.SignerCertificate) { throw 'Missing embedded Authenticode signature' }
        $certificate = $signature.SignerCertificate
        if ((Get-CertificateSha256 $certificate) -cne $expected) { throw 'Signer certificate SHA256 mismatch' }

        # Trust only the independently pinned public signer, only in this user
        # profile, and only long enough for Windows to validate signature and digest.
        $store = [System.Security.Cryptography.X509Certificates.X509Store]::new('Root', 'CurrentUser')
        $added = $false
        try {
            $store.Open([System.Security.Cryptography.X509Certificates.OpenFlags]::ReadWrite)
            $existing = @($store.Certificates | Where-Object { (Get-CertificateSha256 $_) -ceq $expected })
            if ($existing.Count -eq 0) {
                $publicCertificate = [System.Security.Cryptography.X509Certificates.X509Certificate2]::new($certificate.RawData)
                $store.Add($publicCertificate)
                $added = $true
            }
            $verified = Get-AuthenticodeSignature -LiteralPath $path
            if ($verified.Status -ne 'Valid' -or $verified.SignatureType -ne 'Authenticode' -or
                $null -eq $verified.SignerCertificate -or (Get-CertificateSha256 $verified.SignerCertificate) -cne $expected) {
                throw 'Authenticode signature or file digest is invalid'
            }
        } finally {
            if ($added) { $store.Remove($publicCertificate); $publicCertificate.Dispose() }
            $store.Close()
        }
    }
    [Console]::Out.WriteLine('SignPath TEST artifacts verified')
    exit 0
} catch {
    [Console]::Error.WriteLine("SignPath test verification failed: $($_.Exception.Message)")
    exit 1
}
