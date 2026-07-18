# Load and start a sample hoop by name (file stem under samples/hoops).
param(
    [Parameter(Mandatory = $true)]
    [string]$Name,
    [string]$Base = "http://127.0.0.1:8081"
)
$ErrorActionPreference = "Stop"
$repo = Resolve-Path (Join-Path $PSScriptRoot "..")
$file = Join-Path $repo "samples\hoops\$Name.yaml"
if (-not (Test-Path $file)) {
    Write-Error "Missing $file — try hello-critic, explain-snippet, rename-suggest, review-lite, summarize-notes"
}
Set-Location $repo
go run ./scripts/loadhoop -base $Base -file $file -start
