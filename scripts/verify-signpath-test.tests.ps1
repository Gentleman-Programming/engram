param(
    [Parameter(Mandatory = $true)][string]$UnsignedDirectory,
    [int]$ChildTimeoutSeconds = 120
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$root = Join-Path ([IO.Path]::GetTempPath()) ('signpath-fixtures-' + [guid]::NewGuid())
$verifier = Join-Path $PSScriptRoot 'verify-signpath-test.ps1'
$pwsh = [Environment]::ProcessPath
$certificates = @()

function Get-Sha256($Certificate) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try { [Convert]::ToHexString($sha.ComputeHash($Certificate.RawData)) }
    finally { $sha.Dispose() }
}

function Invoke-Case($Name, $Directory, $Fingerprint, $ExpectedError) {
    Write-Output "PROGRESS case start: $Name"
    Write-Output "PROGRESS child start: $Name"
    $info = [Diagnostics.ProcessStartInfo]::new($pwsh)
    foreach ($argument in @('-NoProfile', '-NonInteractive', '-File', $verifier, '-Directory', $Directory, '-ExpectedCertificateSha256', $Fingerprint)) { $info.ArgumentList.Add($argument) }
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.RedirectStandardInput = $true
    $info.UseShellExecute = $false
    $info.Environment['SIGNPATH_VERIFY_TRACE'] = '1'
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $info
    [void]$process.Start()
    $process.StandardInput.Close()
    $outTask = $process.StandardOutput.ReadToEndAsync()
    $errTask = $process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit($ChildTimeoutSeconds * 1000)) {
        $process.Kill($true)
        $process.WaitForExit()
        $partial = ''
        if ($errTask.Wait(5000)) { $partial = ($errTask.Result -split "`r?`n" | Where-Object { $_ -clike 'SIGNPATH-STAGE *' }) -join '; ' }
        $process.Dispose()
        throw "$Name timed out after $ChildTimeoutSeconds seconds; last stages: [$partial]"
    }
    $process.WaitForExit()
    $code = $process.ExitCode
    $out = $outTask.Result.TrimEnd("`r", "`n")
    $allErr = $errTask.Result
    $process.Dispose()
    Write-Output "PROGRESS child end: $Name"
    $stages = @($allErr -split "`r?`n" | Where-Object { $_ -clike 'SIGNPATH-STAGE *' })
    foreach ($stage in $stages) { Write-Output "PROGRESS $stage" }
    $err = (@($allErr -split "`r?`n" | Where-Object { $_ -cnotlike 'SIGNPATH-STAGE *' }) -join "`n").TrimEnd("`r", "`n")
    if ($ExpectedError) {
        $wanted = "SignPath test verification failed: $ExpectedError"
        if ($code -ne 1 -or $out -cne '' -or $err -cne $wanted) { throw "$Name failed assertion: exit=$code stdout=[$out] stderr=[$err]" }
    } elseif ($code -ne 0 -or $out -cne 'SignPath TEST artifacts verified' -or $err -cne '') {
        throw "$Name failed assertion: exit=$code stdout=[$out] stderr=[$err]"
    }
    Write-Output "PASS $Name"
    Write-Output "PROGRESS case end: $Name"
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
        Write-Output "PROGRESS certificate start: $name"
        $certificate = New-SelfSignedCertificate -Type CodeSigningCert -Subject "CN=SignPath disposable $name $([guid]::NewGuid())" -CertStoreLocation 'Cert:\CurrentUser\My' -KeyExportPolicy NonExportable -NotAfter (Get-Date).AddDays(1)
        $certificates += $certificate
        Write-Output "PROGRESS certificate end: $name"
    }
    $fingerprint = Get-Sha256 $certificates[0]
    $otherFingerprint = Get-Sha256 $certificates[1]
    $signed = New-CaseDirectory 'signed' $UnsignedDirectory
    foreach ($file in Get-ChildItem -LiteralPath $signed -Filter '*.exe') {
        Write-Output "PROGRESS signing start: $($file.Name)"
        $signature = Set-AuthenticodeSignature -LiteralPath $file.FullName -Certificate $certificates[0] -HashAlgorithm SHA256
        if ($null -eq $signature.SignerCertificate) { throw 'Fixture signing failed' }
        Write-Output "PROGRESS signing end: $($file.Name)"
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

    # The verifier validates trust in memory and never opens a certificate store,
    # so no pinned test certificate may appear in (or be removed from) CurrentUser Root.
    $store = [Security.Cryptography.X509Certificates.X509Store]::new('Root', 'CurrentUser')
    try {
        $store.Open([Security.Cryptography.X509Certificates.OpenFlags]::ReadOnly)
        foreach ($candidate in $certificates) {
            $candidateHash = Get-Sha256 $candidate
            if (@($store.Certificates | Where-Object { (Get-Sha256 $_) -ceq $candidateHash }).Count -ne 0) { throw 'Verifier leaked trust anchor' }
        }
    } finally { $store.Close() }
} finally {
    foreach ($certificate in $certificates) { Remove-Item -LiteralPath ("Cert:\CurrentUser\My\" + $certificate.Thumbprint) -DeleteKey -ErrorAction Stop }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
