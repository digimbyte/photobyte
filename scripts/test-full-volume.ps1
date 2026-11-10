$ErrorActionPreference = 'Stop'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'The disposable VHD test requires an elevated PowerShell session.'
}

$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$cache = Join-Path $root '.cache'
$testRoot = Join-Path $cache ('volume-test-' + [guid]::NewGuid().ToString('N'))
$mount = Join-Path $testRoot 'mount'
$image = Join-Path $testRoot 'PhotoByteScratch.vhd'
$commands = Join-Path $testRoot 'create-vhd.txt'
$fullPath = [IO.Path]::GetFullPath($image)
if (-not $fullPath.StartsWith($root + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase) -or
    $fullPath.Contains('"') -or (Test-Path -LiteralPath $image)) {
    throw 'Refusing to create a VHD outside the new workspace test directory.'
}
New-Item -ItemType Directory -Path $mount -Force | Out-Null
$attachAttempted = $false
$mountRegistered = $false
$previousMount = $env:PHOTOBYTE_FULL_VOLUME
$previousID = $env:PHOTOBYTE_FULL_VOLUME_ID
Push-Location -LiteralPath $root
try {
    # DiskPart creates only the named image file. All partition and format
    # operations below require a verified association with that new image.
    Set-Content -LiteralPath $commands -Encoding ascii -Value ('create vdisk file="' + $fullPath + '" maximum=64 type=expandable')
    & "$env:SystemRoot/System32/diskpart.exe" /s $commands
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $image -PathType Leaf)) {
        throw 'DiskPart did not create the disposable VHD.'
    }
    $attachAttempted = $true
    $attached = Mount-DiskImage -ImagePath $fullPath -NoDriveLetter -PassThru
    $disks = @($attached | Get-Disk)
    if ($disks.Count -ne 1) {
        throw 'Expected exactly one disk associated with the new VHD.'
    }
    $disk = $disks[0]
    if ($attached.ImagePath -ne $fullPath -or $attached.DevicePath -notmatch 'PhysicalDrive(\d+)$' -or
        [int]$Matches[1] -ne $disk.Number -or $disk.IsBoot -or $disk.IsSystem -or
        $disk.PartitionStyle -ne 'RAW' -or $disk.Size -lt 48MB -or $disk.Size -gt 80MB) {
        throw 'The attached device does not match the blank disposable VHD.'
    }
    $partition = Initialize-Disk -InputObject $disk -PartitionStyle MBR -PassThru |
        New-Partition -UseMaximumSize
    $volume = Format-Volume -Partition $partition -FileSystem NTFS -NewFileSystemLabel PhotoByteTest -Confirm:$false
    Add-PartitionAccessPath -InputObject $partition -AccessPath ($mount + '\')
    $mountRegistered = $true
    if (-not $volume.Path -or $volume.Size -gt 80MB -or $volume.FileSystemLabel -ne 'PhotoByteTest') {
        throw 'Could not verify the dedicated test volume.'
    }
    $env:PHOTOBYTE_FULL_VOLUME = $mount + '\'
    $env:PHOTOBYTE_FULL_VOLUME_ID = $volume.Path
    if (-not $env:GOMODCACHE) { $env:GOMODCACHE = Join-Path $cache 'gomod' }
    if (-not $env:GOCACHE) { $env:GOCACHE = Join-Path $cache 'gobuild' }
    if (-not $env:GOTMPDIR) { $env:GOTMPDIR = Join-Path $cache 'gotmp' }
    New-Item -ItemType Directory -Path $env:GOTMPDIR -Force | Out-Null
    go test ./internal/project -run '^TestRecoveryOnFullVolume$' -count=1 -v
    if ($LASTEXITCODE -ne 0) { throw 'Full-volume recovery test failed.' }
}
finally {
    $env:PHOTOBYTE_FULL_VOLUME = $previousMount
    $env:PHOTOBYTE_FULL_VOLUME_ID = $previousID
    try {
        if ($mountRegistered) { Remove-PartitionAccessPath -InputObject $partition -AccessPath ($mount + '\') }
    }
    finally {
        if ($attachAttempted -and (Get-DiskImage -ImagePath $fullPath -ErrorAction SilentlyContinue).Attached) {
            Dismount-DiskImage -ImagePath $fullPath -ErrorAction Stop | Out-Null
        }
    }
    if (Test-Path -LiteralPath $image -PathType Leaf) { Remove-Item -LiteralPath $image }
    if (Test-Path -LiteralPath $commands -PathType Leaf) { Remove-Item -LiteralPath $commands }
    if ((Test-Path -LiteralPath $mount -PathType Container) -and -not (Get-ChildItem -LiteralPath $mount -Force)) {
        Remove-Item -LiteralPath $mount
    }
    if (-not (Get-ChildItem -LiteralPath $testRoot -Force)) { Remove-Item -LiteralPath $testRoot }
    Pop-Location
}
