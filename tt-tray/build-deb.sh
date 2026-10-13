#!/usr/bin/env bash
# Build tt-tray + paquet .deb
set -euo pipefail
cd "$(dirname "$0")"

VERSION=$(grep -oP 'Version:\s*\K.*' packaging/DEBIAN/control)
PKG="tt-tray_${VERSION}_amd64"

CGO_ENABLED=1 go build -ldflags="-s -w" -o tt-tray .

rm -rf "dist/$PKG"
mkdir -p "dist/$PKG/DEBIAN" "dist/$PKG/usr/bin" \
  "dist/$PKG/usr/share/applications" \
  "dist/$PKG/usr/share/icons/hicolor/128x128/apps"

cp packaging/DEBIAN/control "dist/$PKG/DEBIAN/"
cp tt-tray "dist/$PKG/usr/bin/"
cp packaging/usr/share/applications/tt-tray.desktop "dist/$PKG/usr/share/applications/"
cp icon.png "dist/$PKG/usr/share/icons/hicolor/128x128/apps/tt-tray.png"

dpkg-deb --build "dist/$PKG"
echo "OK -> dist/$PKG.deb"
echo "Install : sudo dpkg -i dist/$PKG.deb"
