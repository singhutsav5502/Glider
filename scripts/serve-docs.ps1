# Serves docs/site on http://127.0.0.1:8090 (override with -Port).
param(
    [int]$Port = 8090
)
$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..\docs\site")
Write-Host "Glider docs: http://127.0.0.1:$Port/"
Write-Host "Root: $root"
Set-Location $root
python -m http.server $Port
