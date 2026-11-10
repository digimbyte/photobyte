[CmdletBinding()]
param(
    [ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version = '1.0.0',
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$cache = Join-Path $root '.cache'
$bin = Join-Path $root 'bin'
$zip = Join-Path $bin "PhotoByte-$Version-windows-x64.zip"
if (Test-Path -LiteralPath $zip) { throw "The $Version Windows package already exists." }
if (-not $SkipBuild) { & (Join-Path $PSScriptRoot 'build.ps1') }

$exe = Join-Path $bin 'PhotoByte.exe'
$codecs = Join-Path $bin 'photobyte_codecs.dll'
$licenses = Join-Path $bin 'licenses'
$readme = Join-Path $root 'README.md'
$mark = Join-Path $root 'assets/brand/photobyte.svg'
foreach ($file in @($exe, $codecs, $readme, $mark)) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "Missing release input $file." }
}
if (-not (Test-Path -LiteralPath (Join-Path $licenses 'libwebp-PATENTS.txt') -PathType Leaf)) {
    throw 'Build the native codec package before creating the release ZIP.'
}
if (-not $env:GOMODCACHE) { $env:GOMODCACHE = Join-Path $cache 'gomod' }
if (-not $env:GOCACHE) { $env:GOCACHE = Join-Path $cache 'gobuild' }

Push-Location -LiteralPath $root
try {
    $modules = @(go list -mod=readonly -deps -f '{{if .Module}}{{.Module.Path}}|{{.Module.Dir}}{{end}}' ./cmd/photobyte)
    if ($LASTEXITCODE -ne 0) { throw 'Could not enumerate the linked Go modules.' }
    foreach ($entry in ($modules | Where-Object { $_ -and -not $_.StartsWith('github.com/digimbyte/photobyte|') } | Sort-Object -Unique)) {
        $module, $dir = $entry -split '\|', 2
        if (-not $dir) { continue }
        $name = $module -replace '[^a-zA-Z0-9._-]', '_'
        foreach ($notice in (Get-ChildItem -LiteralPath $dir -File | Where-Object { $_.Name -match '^(LICENSE|COPYING|NOTICE|PATENTS)([.-].*)?$' })) {
            Copy-Item -LiteralPath $notice.FullName -Destination (Join-Path $licenses "$name-$($notice.Name).txt") -Force
        }
    }
    $goroot = go env GOROOT
    Copy-Item -LiteralPath (Join-Path $goroot 'LICENSE') -Destination (Join-Path $licenses 'Go-LICENSE.txt') -Force
    Copy-Item -LiteralPath (Join-Path $goroot 'PATENTS') -Destination (Join-Path $licenses 'Go-PATENTS.txt') -Force
    $stage = Join-Path $bin ('.release-' + [guid]::NewGuid().ToString('N') + '.zip')
    try {
        Compress-Archive -LiteralPath @($exe, $codecs, $licenses, $readme) -DestinationPath $stage -CompressionLevel Optimal
        $archive = [IO.Compression.ZipFile]::Open($stage, [IO.Compression.ZipArchiveMode]::Update)
        try {
            [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($archive, $mark, 'assets/brand/photobyte.svg') | Out-Null
        }
        finally {
            $archive.Dispose()
        }
        Move-Item -LiteralPath $stage -Destination $zip
    }
    finally {
        if (Test-Path -LiteralPath $stage -PathType Leaf) { Remove-Item -LiteralPath $stage }
    }
    $hash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $([IO.Path]::GetFileName($zip))" | Set-Content -LiteralPath ($zip + '.sha256') -Encoding ascii
    Write-Output $zip
}
finally {
    Pop-Location
}
