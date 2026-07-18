# Starts Glider with NODE_EXTRA_CA_CERTS set for child processes that inherit this env.
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot\..

$ca = Join-Path $env:USERPROFILE ".glider\mitm\ca.crt"
$env:NODE_EXTRA_CA_CERTS = $ca

if (-not (Test-Path ".\glider.exe")) {
    Write-Host "Building glider.exe..."
    $env:PATH = "$env:LOCALAPPDATA\go-sdk\go\bin;$env:PATH"
    $env:GOROOT = "$env:LOCALAPPDATA\go-sdk\go"
    go build -o glider.exe ./cmd/glider
}

Write-Host "Gateway:   http://localhost:8080/v1"
Write-Host "MITM:      http://127.0.0.1:8082"
Write-Host "Dashboard: http://localhost:8081"
Write-Host "CA:        $ca"
Write-Host ""
& .\glider.exe --config configs\glider.yaml
