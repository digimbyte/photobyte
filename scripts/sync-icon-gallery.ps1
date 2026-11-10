[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$icons = Join-Path $PSScriptRoot '../assets/icons'
$catalog = [IO.File]::ReadAllText((Join-Path $icons 'catalog.json')).Trim()
$null = ConvertFrom-Json -InputObject $catalog
$galleryPath = Join-Path $icons 'gallery.html'
$gallery = [IO.File]::ReadAllText($galleryPath)
$opening = '<script id="icon-data" type="application/json">'
$start = $gallery.IndexOf($opening, [StringComparison]::Ordinal)
if ($start -lt 0) {
    throw 'The gallery is missing its catalog marker.'
}
$start += $opening.Length
$end = $gallery.IndexOf('</script>', $start, [StringComparison]::Ordinal)
if ($end -lt 0) {
    throw 'The gallery catalog block is incomplete.'
}

# JSON strings containing a less-than sign must stay inside the script element.
$embedded = $catalog.Replace('<', '\u003c')
$updated = $gallery.Substring(0, $start) + "`n$embedded`n  " + $gallery.Substring($end)
if ($updated -cne $gallery) {
    [IO.File]::WriteAllText($galleryPath, $updated, [Text.UTF8Encoding]::new($false))
}
