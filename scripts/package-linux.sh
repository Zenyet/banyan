#!/bin/sh
# Build Banyan for Linux and put a tarball and a .deb in dist/. Go builds
# without cgo, so this runs on any system with Go and dpkg-deb.
# Usage: scripts/package-linux.sh [<arch>], amd64 (the default) or arm64.
set -e
cd "$(dirname "$0")/.."

VERSION=$(scripts/version.sh)
ARCH=${1:-amd64}
NAME_ARCH=$([ "$ARCH" = amd64 ] && echo x86_64 || echo aarch64)
mkdir -p build dist
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -tags mygo_noinspector \
    -ldflags "-s -w -X main.version=$VERSION -X github.com/egoist/mygo.production=1" -o build/banyan .

NAME="Banyan-$VERSION-linux-$NAME_ARCH"
STAGE=$(mktemp -d)
mkdir "$STAGE/$NAME"
cp build/banyan packaging/linux/banyan.desktop packaging/linux/banyan.png README.md "$STAGE/$NAME/"
tar -C "$STAGE" -czf "dist/$NAME.tar.gz" "$NAME"

DEB="$STAGE/deb"
mkdir -p "$DEB/DEBIAN" "$DEB/usr/bin" "$DEB/usr/share/applications" "$DEB/usr/share/icons/hicolor/512x512/apps"
install -m 755 build/banyan "$DEB/usr/bin/banyan"
install -m 644 packaging/linux/banyan.desktop "$DEB/usr/share/applications/"
install -m 644 packaging/linux/banyan.png "$DEB/usr/share/icons/hicolor/512x512/apps/"
cat > "$DEB/DEBIAN/control" <<CONTROL
Package: banyan
Version: $VERSION
Architecture: $ARCH
Maintainer: Banyan <noreply@github.com>
Section: devel
Priority: optional
Depends: git, libgl1
Description: A Git client in the spirit of JetBrains' Git tooling
CONTROL
dpkg-deb --root-owner-group --build "$DEB" "dist/banyan_${VERSION}_$ARCH.deb"
rm -rf "$STAGE"
