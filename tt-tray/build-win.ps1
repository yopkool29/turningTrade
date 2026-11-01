# Build tt-tray + installeur Windows (Inno Setup) - equivalent de build-deb.sh
# Usage : .\build-win.ps1   ->   dist\tt-tray_<version>_win-x64_setup.exe
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$VERSION = (Select-String -Path packaging\DEBIAN\control -Pattern 'Version:\s*(\S+)').Matches[0].Groups[1].Value

go build -ldflags="-s -w -H=windowsgui" -o tt-tray.exe .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$iscc = @(
    "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe",
    "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
    "$env:ProgramFiles\Inno Setup 6\ISCC.exe"
) | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $iscc) {
    Write-Error 'Inno Setup 6 introuvable - installe-le : winget install JRSoftware.InnoSetup'
    exit 1
}

& $iscc "/DAppVersion=$VERSION" packaging\tt-tray.iss
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "OK -> dist\tt-tray_${VERSION}_win-x64_setup.exe"
Write-Host 'Install : lancer le setup (pas admin requis, install par utilisateur)'
