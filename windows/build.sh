#!/bin/sh
# Builds Open Bars Connect for Windows from macOS or Linux (pure Go, no cgo):
#   dist/OpenBars-windows-<x64|arm64>-<version>.exe  one file: wintun.dll and
#                                                     the licenses are built in
#   dist/OpenBars-windows-<x64|arm64>-<version>.zip  OpenBars.exe without them,
#                                                     and the files next to it
#   dist/<amd64|arm64>/  the zip's contents
# The zip's OpenBars.exe contains only Open Bars' own code, for code signing
# services that sign only a project's own binaries (wintun.dll is already
# signed by WireGuard LLC).
#
# usage: windows/build.sh [version]     (default: dev)
#        DEV=1 windows/build.sh         console build, so output shows in terminals/SSH
set -eu
cd "$(dirname "$0")"
VERSION=${1:-dev}
WINTUN_VERSION=0.14.1
WINTUN_SHA256=07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51
CACHE=dist/cache
mkdir -p "$CACHE"

# Wintun: the official signed DLLs, checksum-verified.
zip="$CACHE/wintun-$WINTUN_VERSION.zip"
if [ ! -f "$zip" ]; then
  curl -fsSL -o "$zip.part" "https://www.wintun.net/builds/wintun-$WINTUN_VERSION.zip"
  mv "$zip.part" "$zip"
fi
echo "$WINTUN_SHA256  $zip" | shasum -a 256 -c - >/dev/null
rm -rf "$CACHE/wintun" && unzip -q -o "$zip" -d "$CACHE"

# Icon, manifest and version info for the exe.
numver=$(echo "$VERSION" | sed -E 's/^v//; s/[^0-9.].*$//')
[ -n "$numver" ] || numver=0.0.0
go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --out cmd/openbars/rsrc \
  --arch amd64,arm64 --product-version "$numver" --file-version "$numver"

go run ./tools/notices "$CACHE/THIRD_PARTY_NOTICES.txt" "$CACHE/wintun/LICENSE.txt"

ldflags="-s -w -X main.version=$VERSION"
[ "${DEV:-}" = 1 ] || ldflags="$ldflags -H windowsgui"
bundle=internal/install/bundle # embedded by -tags bundle (bundle_windows.go)
trap 'rm -rf "$bundle"' EXIT
for arch in amd64 arm64; do
  out="dist/$arch"
  rm -rf "$out" "$bundle" && mkdir -p "$out" "$bundle"
  cp "$CACHE/wintun/bin/$arch/wintun.dll" "$bundle/"
  cp "$CACHE/wintun/LICENSE.txt" "$bundle/wintun-LICENSE.txt"
  cp ../LICENSE "$bundle/LICENSE.txt"
  cp "$CACHE/THIRD_PARTY_NOTICES.txt" "$bundle/"
  name="OpenBars-windows-$(echo $arch | sed s/amd64/x64/)-$VERSION"
  CGO_ENABLED=0 GOOS=windows GOARCH=$arch go build -trimpath -tags bundle -ldflags "$ldflags" -o "dist/$name.exe" ./cmd/openbars
  CGO_ENABLED=0 GOOS=windows GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$out/OpenBars.exe" ./cmd/openbars
  cp "$bundle"/* "$out/"
  rm -f "dist/$name.zip"
  (cd "$out" && zip -q -X "../$name.zip" OpenBars.exe wintun.dll LICENSE.txt THIRD_PARTY_NOTICES.txt wintun-LICENSE.txt)
  echo "dist/$name.exe"
  echo "dist/$name.zip"
done
