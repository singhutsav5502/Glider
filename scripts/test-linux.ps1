# Run the Go test suite on real Linux, from a Windows checkout.
#
#   powershell -ExecutionPolicy Bypass -File scripts\test-linux.ps1
#
# Windows and Linux are both first-class for Glider: the transparent
# redirector has a separate implementation on each (WinDivert, and iptables
# with SO_ORIGINAL_DST), selected by build tag. `go build` alone will not
# catch a Linux-only fault in one of those, because the Windows file is the
# one that compiles at home.
#
# This does NOT need Go inside WSL. It cross-compiles each package's test
# binary on Windows with GOOS=linux, then executes those binaries in WSL.
# Nothing is installed in the distribution.
#
# A package is run from its own source directory, because a test that reads
# testdata resolves it relative to the working directory.

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot\..

$distro = $env:GLIDER_WSL_DISTRO
if (-not $distro) { $distro = 'Ubuntu-24.04' }

if (-not (Get-Command wsl -ErrorAction SilentlyContinue)) {
    throw 'wsl is not installed - cannot run the Linux suite from here.'
}

$repoWin = (Get-Location).Path
$drive = $repoWin.Substring(0, 1).ToLower()
$repoWsl = '/mnt/' + $drive + $repoWin.Substring(2).Replace('\', '/')

# Every package that has at least one _test.go file.
$pkgs = Get-ChildItem -Path . -Recurse -Filter '*_test.go' -File |
    Where-Object { $_.FullName -notmatch '\\_research\\|\\_lintest\\' } |
    ForEach-Object { (Split-Path $_.FullName -Parent).Replace($repoWin + '\', '').Replace('\', '/') } |
    Sort-Object -Unique

if (-not $pkgs) { throw 'no packages with tests found' }

$outDir = Join-Path $repoWin '_lintest'
if (Test-Path $outDir) { Remove-Item $outDir -Recurse -Force }
New-Item -ItemType Directory -Path $outDir | Out-Null

Write-Host "cross-compiling $($pkgs.Count) linux test binaries..."
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
$built = @()
$skipped = @()
foreach ($p in $pkgs) {
    $name = $p.Replace('/', '_')
    $bin = Join-Path $outDir "$name.test"
    & go test -c -o $bin "./$p" 2>&1 | Out-Null
    $compiled = $LASTEXITCODE -eq 0

    # Exit 0 is not proof of a binary. A package whose only test files are
    # _windows_test.go has NO test files under GOOS=linux, so `go test -c`
    # succeeds, writes nothing, and leaves $bin absent — internal/fileacl and
    # internal/procutil are both like this. Counting those as built made the
    # runner report a pass for a binary that never ran.
    if ($compiled -and (Test-Path $bin)) { $built += @{ Pkg = $p; Name = $name } }
    elseif ($compiled) { $skipped += $p }
    else { Write-Host "  COMPILE FAIL  $p" }
}
if ($skipped) {
    Write-Host "  no linux tests (skipped): $($skipped -join ', ')"
}
Remove-Item Env:GOOS
Remove-Item Env:GOARCH

# One bash invocation for the whole run: starting the distribution is the
# slow part, and doing it per package dominates the total.
#
# Two things this command must not contain, both found the hard way, and
# both of which made the runner report that everything passed when nothing
# had run:
#
#   A double quote. It does not survive the trip through the Windows command
#   line into wsl.exe. bash received a mangled string and ran almost none of
#   it. Package paths carry no spaces, so echo needs no quoting anyway.
#
#   $? . Something in the PowerShell-to-wsl.exe argument path expands it
#   before bash sees it, and always to 0: `bash -lc '/bin/false; echo $?'`
#   prints 0 through this route, while `if /bin/false; then ...; else ...`
#   correctly takes the else branch. So the outcome is carried by an && / ||
#   chain, which is evaluated by bash itself.
$lines = foreach ($b in $built) {
    "chmod +x $repoWsl/_lintest/$($b.Name).test 2>/dev/null; cd $repoWsl/$($b.Pkg) && $repoWsl/_lintest/$($b.Name).test -test.timeout=300s > /tmp/$($b.Name).log 2>&1 && echo $($b.Pkg) PASS || echo $($b.Pkg) FAIL"
}
Write-Host "running on $distro..."
$results = @(& wsl.exe -d $distro -- bash -lc ($lines -join '; '))

$failed = @()
$reported = 0
foreach ($line in $results) {
    if ($line -notmatch '\s(PASS|FAIL)$') { continue }
    $reported++
    Write-Host "  $line"
    if ($Matches[1] -eq 'FAIL') { $failed += ($line -split '\s+')[0] }
}

# Silence is not success. If a package did not report, treat the run as
# failed rather than assuming it passed.
if ($reported -ne $built.Count) {
    Remove-Item $outDir -Recurse -Force -ErrorAction SilentlyContinue
    throw "only $reported of $($built.Count) packages reported a result - the run did not complete"
}

# Only a failing package's output is worth printing; a passing one says "ok".
foreach ($f in $failed) {
    $name = $f.Replace('/', '_')
    Write-Host ""
    Write-Host "--- $f ---"
    & wsl.exe -d $distro -- bash -lc "tail -40 /tmp/$name.log"
}

Remove-Item $outDir -Recurse -Force

if ($failed) {
    Write-Host ""
    throw "linux suite failed: $($failed -join ', ')"
}
Write-Host ""
Write-Host "all $($built.Count) packages pass on linux"
