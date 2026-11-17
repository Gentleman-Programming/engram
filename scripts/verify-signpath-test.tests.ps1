param([Parameter(Mandatory = $true)][string]$UnsignedDirectory)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$root = Join-Path ([IO.Path]::GetTempPath()) ('signpath-fixtures-' + [guid]::NewGuid())
$verifier = Join-Path $PSScriptRoot 'verify-signpath-test.ps1'
$pwsh = Join-Path $PSHOME 'pwsh.exe'
$certificates = @()
$anchor = $null
$store = $null

function Get-Sha256($Certificate) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try { [Convert]::ToHexString($sha.ComputeHash($Certificate.RawData)) }
    finally { $sha.Dispose() }
}

function Invoke-Case($Name, $Directory, $Fingerprint, $ExpectedError) {
    $stdout = Join-Path $root 'stdout.txt'
    $stderr = Join-Path $root 'stderr.txt'
    & $pwsh -NoProfile -NonInteractive -File $verifier -Directory $Directory -ExpectedCertificateSha256 $Fingerprint > $stdout 2> $stderr
    $code = $LASTEXITCODE
    $out = [IO.File]::ReadAllText($stdout).TrimEnd("`r", "`n")
    $err = [IO.File]::ReadAllText($stderr).TrimEnd("`r", "`n")
    if ($ExpectedError) {
        $wanted = "SignPath test verification failed: $ExpectedError"
        if ($code -ne 1 -or $out -cne '' -or $err -cne $wanted) { throw "$Name failed assertion: exit=$code stdout=[$out] stderr=[$err]" }
    } elseif ($code -ne 0 -or $out -cne 'SignPath TEST artifacts verified' -or $err -cne '') {
        throw "$Name failed assertion: exit=$code stdout=[$out] stderr=[$err]"
    }
    Write-Output "PASS $Name"
}

function New-CaseDirectory($Name, $Source) {
    $directory = Join-Path $root $Name
    New-Item -ItemType Directory -Path $directory | Out-Null
    Copy-Item -Path (Join-Path $Source '*.exe') -Destination $directory
    return $directory
}

try {
    New-Item -ItemType Directory -Path $root -ErrorAction Stop | Out-Null
    foreach ($name in @('signer', 'other')) {
        $certificate = New-SelfSignedCertificate -Type CodeSigningCert -Subject "CN=SignPath disposable $name $([guid]::NewGuid())" -CertStoreLocation 'Cert:\CurrentUser\My' -KeyExportPolicy NonExportable -NotAfter (Get-Date).AddDays(1)
        $certificates += $certificate
    }
    $fingerprint = Get-Sha256 $certificates[0]
    $otherFingerprint = Get-Sha256 $certificates[1]
    $signed = New-CaseDirectory 'signed' $UnsignedDirectory
    foreach ($file in Get-ChildItem -LiteralPath $signed -Filter '*.exe') {
        $signature = Set-AuthenticodeSignature -LiteralPath $file.FullName -Certificate $certificates[0] -HashAlgorithm SHA256
        if ($null -eq $signature.SignerCertificate) { throw 'Fixture signing failed' }
    }

    Invoke-Case 'intact' $signed $fingerprint ''
    Invoke-Case 'wrong signer fingerprint' $signed $otherFingerprint 'Signer certificate SHA256 mismatch'
    $unsigned = New-CaseDirectory 'unsigned' $UnsignedDirectory
    Invoke-Case 'unsigned' $unsigned $fingerprint 'Missing embedded Authenticode signature'
    $missing = New-CaseDirectory 'missing' $signed
    Remove-Item -LiteralPath (Join-Path $missing 'engram-windows-arm64.exe')
    Invoke-Case 'missing' $missing $fingerprint 'Expected exactly two files'
    $extra = New-CaseDirectory 'extra' $signed
    [IO.File]::WriteAllText((Join-Path $extra 'extra.txt'), 'extra')
    Invoke-Case 'extra' $extra $fingerprint 'Expected exactly two files'
    $wrongArchitecture = New-CaseDirectory 'architecture' $signed
    Copy-Item -LiteralPath (Join-Path $signed 'engram-windows-arm64.exe') -Destination (Join-Path $wrongArchitecture 'engram-windows-amd64.exe') -Force
    Invoke-Case 'incorrect architecture' $wrongArchitecture $fingerprint 'Incorrect PE architecture'
    $tampered = New-CaseDirectory 'tampered' $signed
    $path = Join-Path $tampered 'engram-windows-amd64.exe'
    $bytes = [IO.File]::ReadAllBytes($path)
    $bytes[512] = $bytes[512] -bxor 1
    [IO.File]::WriteAllBytes($path, $bytes)
    Invoke-Case 'tampered' $tampered $fingerprint 'Authenticode signature or file digest is invalid'

    # A preexisting identical trust anchor belongs to its owner, not the verifier.
    $store = [Security.Cryptography.X509Certificates.X509Store]::new('Root', 'CurrentUser')
    $store.Open([Security.Cryptography.X509Certificates.OpenFlags]::ReadWrite)
    if (@($store.Certificates | Where-Object { (Get-Sha256 $_) -ceq $fingerprint }).Count -ne 0) { throw 'Verifier leaked its temporary trust anchor' }
    $anchor = [Security.Cryptography.X509Certificates.X509Certificate2]::new($certificates[0].RawData)
    $store.Add($anchor)
    Invoke-Case 'preexisting anchor' $signed $fingerprint ''
    if (@($store.Certificates | Where-Object { (Get-Sha256 $_) -ceq $fingerprint }).Count -ne 1) { throw 'Verifier removed preexisting anchor' }
} finally {
    if ($null -ne $anchor) { $store.Remove($anchor); $anchor.Dispose() }
    if ($null -ne $store) { $store.Close() }
    foreach ($certificate in $certificates) { Remove-Item -LiteralPath ("Cert:\CurrentUser\My\" + $certificate.Thumbprint) -DeleteKey -ErrorAction Stop }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
