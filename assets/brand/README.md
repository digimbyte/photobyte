# PhotoByte mark

The [SVG](photobyte.svg) is the editable source. The PNG is used in the app header and the ICO provides Windows shell sizes from 16 to 256 px.

After editing the SVG, regenerate the PNG and ICO with transparency. From this directory, use the Windows SDK resource compiler and MinGW-w64 `windres` to rebuild the Windows icon and name resource.

```powershell
rc /nologo /fo "$env:TEMP/photobyte.res" photobyte.rc
windres -F pe-x86-64 -J res -O coff "$env:TEMP/photobyte.res" ../../cmd/photobyte/photobyte_windows_amd64.syso
```
