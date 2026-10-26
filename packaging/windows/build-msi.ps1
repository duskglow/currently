# Builds the nut-ng MSI with the WiX Toolset.
#
#   packaging\windows\build-msi.ps1 -Version 1.2.3 -BinDir dist\nutng_windows_amd64_v1 -Out dist\nutng_1.2.3_windows_amd64.msi
#
# BinDir holds nutng.exe and plugins\nutng-output-mqtt.exe. Needs the .NET
# SDK; the WiX tool is installed into a local folder if it is missing.
param(
    [Parameter(Mandatory)] [string] $Version,
    [Parameter(Mandatory)] [string] $BinDir,
    [Parameter(Mandatory)] [string] $Out,
    [string] $Arch = "x64"
)
$ErrorActionPreference = "Stop"
$WixVersion = "5.0.2"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$src = Resolve-Path (Join-Path $here "..\..")
$tools = Join-Path $env:TEMP "nutng-wix-$WixVersion"

$wix = Join-Path $tools "wix.exe"
if (-not (Test-Path $wix)) {
    dotnet tool install wix --version $WixVersion --tool-path $tools | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "could not install the WiX tool" }
}
$bin = Resolve-Path $BinDir
$outPath = [IO.Path]::GetFullPath((Join-Path (Get-Location) $Out))
# WiX keeps extensions in .wix under the current folder; use the tool folder.
Push-Location $tools
try {
    & $wix extension add "WixToolset.Util.wixext/$WixVersion" | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "could not add the WiX util extension" }

    # MSI versions are numeric: drop a leading v and any pre-release suffix.
    $msiVersion = ($Version -replace '^v', '') -replace '[-+].*$', ''
    & $wix build -arch $Arch -ext WixToolset.Util.wixext `
        -d "Version=$msiVersion" -d "BinDir=$bin" -d "SrcDir=$src" `
        -o $outPath (Join-Path $here "nutng.wxs") | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "wix build failed" }
} finally {
    Pop-Location
}
Write-Host "built $outPath"
