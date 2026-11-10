[CmdletBinding()]
param([switch]$SkipBuild, [switch]$RequirePresentedSamples)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$cache = Join-Path $root '.cache'
$run = Join-Path $cache ('live-release-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $run -Force | Out-Null
$report = Join-Path $run 'report.json'

if (-not $SkipBuild) {
    if (-not (Test-Path -LiteralPath (Join-Path $root 'bin/photobyte_codecs.dll'))) {
        & (Join-Path $PSScriptRoot 'build-codecs.ps1')
    }
    & (Join-Path $PSScriptRoot 'build.ps1') -GoOnly -ReleaseProbe -Console
}
$previousReport = $env:PHOTOBYTE_RELEASE_PROBE
$previousWorkers = $env:GOMAXPROCS
$previousPixelProbe = $env:PHOTOBYTE_PROBE_DESKTOP_PIXEL
try {
    $env:PHOTOBYTE_RELEASE_PROBE = $report
    $env:GOMAXPROCS = '8'
    if ($RequirePresentedSamples) {
        $env:PHOTOBYTE_PROBE_DESKTOP_PIXEL = '1'
    }
    $exe = Join-Path $cache 'release-probe/PhotoByte.exe'
    if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw 'Build the live probe before using -SkipBuild.' }
    $data = Join-Path $run 'workspace'
    # A regular native window is required to measure successful swap-chain
    # presentations. Hidden windows can report DXGI_STATUS_OCCLUDED.
    & $exe --data-dir $data --quit-after 185s
    $exitCode = $LASTEXITCODE
    if (-not (Test-Path -LiteralPath $report)) {
        throw "The editor closed without writing a probe report (exit $exitCode)."
    }
    $result = Get-Content -Raw -LiteralPath $report
    Write-Host "Probe report $report"
    Write-Output $result
    $measurements = $result | ConvertFrom-Json
    if ($measurements.failure -or $exitCode -ne 0) {
        throw 'The live Windows release probe failed.'
    }
    if ($RequirePresentedSamples -and
        ($measurements.timing_method -ne 'desktop_pixel' -or $measurements.occluded_frames -ne 0 -or
         $measurements.hardware_presented_samples -ne $measurements.hardware_samples -or
         $measurements.warp_presented_samples -ne $measurements.warp_samples -or
         $measurements.hardware_composited_samples -ne $measurements.hardware_samples -or
         $measurements.warp_composited_samples -ne $measurements.warp_samples)) {
        throw 'Display qualification requires a successful Present and a visible desktop marker for every input sample.'
    }
}
finally {
    $env:PHOTOBYTE_RELEASE_PROBE = $previousReport
    $env:GOMAXPROCS = $previousWorkers
    $env:PHOTOBYTE_PROBE_DESKTOP_PIXEL = $previousPixelProbe
}
