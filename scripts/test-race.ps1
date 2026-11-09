[CmdletBinding()]
param([string]$Compiler)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$cache = Join-Path $root '.cache'
if (-not $Compiler) {
    $toolchain = Join-Path $cache 'toolchains/winlibs-16.2.0'
    $archiveName = 'winlibs-x86_64-posix-seh-gcc-16.2.0-mingw-w64ucrt-14.0.0-r2.7z'
    $archive = Join-Path $toolchain $archiveName
    $Compiler = Join-Path $toolchain 'mingw64/bin/gcc.exe'
    if (-not (Test-Path -LiteralPath $Compiler -PathType Leaf)) {
        New-Item -ItemType Directory -Path $toolchain -Force | Out-Null
        if (-not (Test-Path -LiteralPath $archive -PathType Leaf)) {
            $url = 'https://github.com/brechtsanders/winlibs_mingw/releases/download/16.2.0posix-14.0.0-ucrt-r2/' + $archiveName
            Invoke-WebRequest -Uri $url -UseBasicParsing -OutFile $archive
        }
        $digest = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash
        if ($digest -ne '53fb6773fbdf87cdf22b69a6b2a07ee33d3ada413b42d83044bc7cc6481400d2') {
            throw 'The pinned WinLibs archive failed its SHA-256 check.'
        }
        tar.exe -xf $archive -C $toolchain
        if ($LASTEXITCODE -ne 0) { throw 'Could not extract the race compiler.' }
    }
}
$Compiler = (Resolve-Path -LiteralPath $Compiler).Path
$syncLibrary = & $Compiler --print-file-name=libsynchronization.a
if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $syncLibrary -PathType Leaf)) {
    throw 'The Windows race detector needs a MinGW-w64 compiler with libsynchronization.a.'
}

$previousCC = $env:CC
$previousCgo = $env:CGO_ENABLED
$env:CC = '"' + $Compiler + '"'
$env:CGO_ENABLED = '1'
if (-not $env:GOMODCACHE) { $env:GOMODCACHE = Join-Path $cache 'gomod' }
if (-not $env:GOCACHE) { $env:GOCACHE = Join-Path $cache 'gobuild' }
if (-not $env:GOTMPDIR) { $env:GOTMPDIR = Join-Path $cache 'gotmp' }
New-Item -ItemType Directory -Path $env:GOTMPDIR -Force | Out-Null
Push-Location -LiteralPath $root
try {
    go test -race '-ldflags=-extldflags=-static' ./...
    if ($LASTEXITCODE -ne 0) { throw 'Windows race tests failed.' }
}
finally {
    Pop-Location
    $env:CC = $previousCC
    $env:CGO_ENABLED = $previousCgo
}
