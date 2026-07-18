# One-time Windows setup for Glider direct usage
# Run in an elevated or interactive PowerShell:
#   powershell -ExecutionPolicy Bypass -File scripts\setup-windows.ps1

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot\..

$caDir = Join-Path $env:USERPROFILE ".glider\mitm"
$caCert = Join-Path $caDir "ca.crt"
$caKey = Join-Path $caDir "ca.key"

$env:PATH = "$env:LOCALAPPDATA\go-sdk\go\bin;$env:PATH"
$env:GOROOT = "$env:LOCALAPPDATA\go-sdk\go"

if (-not (Test-Path ".\glider.exe")) {
    Write-Host "Building glider.exe..."
    go build -o glider.exe ./cmd/glider
}

if (-not (Test-Path $caCert)) {
    Write-Host "Generating MITM CA..."
    go run .\scripts\gen-ca.go
}

if (-not (Test-Path $caCert)) {
    throw "CA missing at $caCert"
}

Write-Host "Importing CA into CurrentUser\Root (may show a trust prompt)..."
certutil -user -addstore Root $caCert
if ($LASTEXITCODE -ne 0) {
    Write-Host "certutil failed; trying Import-Certificate..."
    Import-Certificate -FilePath $caCert -CertStoreLocation Cert:\CurrentUser\Root | Out-Null
}

$thumb = (New-Object System.Security.Cryptography.X509Certificates.X509Certificate2($caCert)).Thumbprint
$found = Get-ChildItem Cert:\CurrentUser\Root | Where-Object { $_.Thumbprint -eq $thumb }
if (-not $found) {
    throw "CA import did not stick. Open mmc Certificates snap-in and import $caCert into Trusted Root Certification Authorities (Current User)."
}
Write-Host "CA trusted:" $found.Subject "Thumbprint=" $found.Thumbprint

@"
# Dot-source before launching Cursor from a terminal:
#   . `$HOME\.glider\mitm\env.ps1
`$env:NODE_EXTRA_CA_CERTS = '$caCert'
"@ | Set-Content -Path (Join-Path $caDir "env.ps1") -Encoding UTF8

# Cursor settings
$settingsPath = Join-Path $env:APPDATA "Cursor\User\settings.json"
$proxySettings = @{
    "http.proxy" = "http://127.0.0.1:8082"
    "http.proxySupport" = "override"
    "http.proxyStrictSSL" = $false
    "cursor.general.disableHttp2" = $true
}
if (Test-Path $settingsPath) {
    $existing = Get-Content $settingsPath -Raw | ConvertFrom-Json
    foreach ($k in $proxySettings.Keys) {
        $existing | Add-Member -NotePropertyName $k -NotePropertyValue $proxySettings[$k] -Force
    }
    $existing | ConvertTo-Json -Depth 20 | Set-Content $settingsPath -Encoding UTF8
} else {
    $proxySettings | ConvertTo-Json -Depth 5 | Set-Content $settingsPath -Encoding UTF8
}
Write-Host "Updated Cursor settings:" $settingsPath

Write-Host ""
Write-Host "Next:"
Write-Host "  1. .\scripts\start-glider.ps1"
Write-Host "  2. Fully quit Cursor (File > Exit) and relaunch"
Write-Host "  3. Optionally: . `$HOME\.glider\mitm\env.ps1 then start Cursor from that shell"
Write-Host "  4. Dashboard: http://localhost:8081"
