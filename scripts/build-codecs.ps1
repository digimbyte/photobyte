$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$cache = Join-Path $root '.cache/native'
$sources = Join-Path $cache 'sources'
$out = Join-Path $root 'bin'
New-Item -ItemType Directory -Force -Path $sources, $out | Out-Null

$deps = @(
    @{ Dir = 'libpng-1.6.59'; Ref = 'pnggroup/libpng/tar.gz/refs/tags/v1.6.59'; Hash = '2540302A1844AD2B2B501977ABECFA850F265F97B78F065A712AB4074A89F5B5'; License = 'LICENSE' },
    @{ Dir = 'zlib-1.4'; Ref = 'madler/zlib/tar.gz/refs/tags/v1.4'; Hash = '4331355048E286908260FDBEAA0B8CF75CEAF2907C75855A093A6487C7032AAA'; License = 'LICENSE' },
    @{ Dir = 'Little-CMS-lcms2.20'; Ref = 'mm2/Little-CMS/tar.gz/refs/tags/lcms2.20'; Hash = '49908A4492FE7FC886517D636474BD953EBE698D38ECD3EE5862DFA308DD43AE'; License = 'LICENSE' },
    @{ Dir = 'libwebp-1.6.0'; Ref = 'webmproject/libwebp/tar.gz/refs/tags/v1.6.0'; Hash = '93A852C2B3EFAFEE3723EFD4636DE855B46F9FE1EFDDD607E1F42F60FC8F2136'; License = 'COPYING' }
)
foreach ($dep in $deps) {
    $archive = Join-Path $sources ($dep.Dir + '.tar.gz')
    if (-not (Test-Path -LiteralPath $archive)) {
        Invoke-WebRequest -UseBasicParsing -Uri ('https://codeload.github.com/' + $dep.Ref) -OutFile $archive
    }
    if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $dep.Hash) {
        throw "Source archive checksum mismatch for $($dep.Dir)."
    }
    if (-not (Test-Path -LiteralPath (Join-Path $sources $dep.Dir))) {
        tar.exe -xzf $archive -C $sources
        if ($LASTEXITCODE -ne 0) { throw "Could not extract $($dep.Dir)." }
    }
}

$cmake = (Get-Command cmake -ErrorAction SilentlyContinue).Source
if (-not $cmake) {
    $vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio/Installer/vswhere.exe'
    if (Test-Path -LiteralPath $vswhere) {
        $vs = & $vswhere -latest -products '*' -property installationPath
        if ($vs) { $cmake = Join-Path $vs 'Common7/IDE/CommonExtensions/Microsoft/CMake/CMake/bin/cmake.exe' }
    }
}
if (-not $cmake -or -not (Test-Path -LiteralPath $cmake)) {
    throw 'Building the native codecs requires CMake and the Visual Studio 2022 C++ build tools.'
}
$build = Join-Path $cache 'build'
$keys = @('DIGI_PNG', 'DIGI_ZLIB', 'DIGI_LCMS', 'DIGI_WEBP')
$options = for ($i = 0; $i -lt $deps.Count; $i++) {
    $sourceDir = (Join-Path $sources $deps[$i].Dir).Replace('\', '/')
    "-D$($keys[$i])=$sourceDir"
}
$previousTracking = $env:TrackFileAccess
$env:TrackFileAccess = 'false'
try {
    & $cmake -S (Join-Path $root 'native') -B $build -G 'Visual Studio 17 2022' -A x64 @options
    if ($LASTEXITCODE -ne 0) { throw 'Native codec configuration failed.' }
    & $cmake --build $build --config Release --target digiphoto_codecs --parallel
    if ($LASTEXITCODE -ne 0) { throw 'Native codec build failed.' }
}
finally {
    $env:TrackFileAccess = $previousTracking
}
Copy-Item -LiteralPath (Join-Path $build 'Release/digiphoto_codecs.dll') -Destination $out -Force

$licenses = Join-Path $out 'licenses'
New-Item -ItemType Directory -Force -Path $licenses | Out-Null
foreach ($dep in $deps) {
    $license = Join-Path (Join-Path $sources $dep.Dir) $dep.License
    Copy-Item -LiteralPath $license -Destination (Join-Path $licenses ($dep.Dir + '.txt')) -Force
}
Copy-Item -LiteralPath (Join-Path $sources 'libwebp-1.6.0/PATENTS') -Destination (Join-Path $licenses 'libwebp-PATENTS.txt') -Force
