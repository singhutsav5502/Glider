# Seed ALL sample hoops + swarm templates into Glider's runtime store.
#
# Usage (from repo root):
#   powershell -File scripts\seed-samples.ps1
#   powershell -File scripts\seed-samples.ps1 -Start
#   .\scripts\seed-samples.ps1 -Base http://127.0.0.1:8081
#
# Default: create/update only (does not start hoops). Requires Glider dashboard up.
param(
    # Case-insensitive: -Start and -start both work.
    [switch]$Start,
    [string]$Base = "http://127.0.0.1:8081",
    [string]$Samples = "",
    [string]$HoopsDir = ""
)

$ErrorActionPreference = "Stop"
$repo = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $repo

$env:PATH = "$env:LOCALAPPDATA\go-sdk\go\bin;$env:PATH"
if ($env:GOROOT -eq $null -or $env:GOROOT -eq "") {
    if (Test-Path "$env:LOCALAPPDATA\go-sdk\go") {
        $env:GOROOT = "$env:LOCALAPPDATA\go-sdk\go"
    }
}

$argsList = @("./scripts/seedsamples", "-base", $Base)
if ($Start) {
    $argsList += "-start"
}
if ($Samples -ne "") {
    $argsList += @("-samples", $Samples)
}
if ($HoopsDir -ne "") {
    $argsList += @("-hoops-dir", $HoopsDir)
}

Write-Host "go run $($argsList -join ' ')"
& go run @argsList
exit $LASTEXITCODE
