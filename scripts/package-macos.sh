#!/bin/sh
# Build Banyan.app for Apple silicon and for Intel and put each in dist/ as
# a disk image.
set -e
cd "$(dirname "$0")/.."

VERSION=$(scripts/version.sh)
mkdir -p dist
for arch in arm64 x86_64; do
    goarch=$([ $arch = arm64 ] && echo arm64 || echo amd64)
    scripts/bundle.sh --arch $goarch
    STAGE=$(mktemp -d)
    cp -R "build/$goarch/Banyan.app" "$STAGE/"
    ln -s /Applications "$STAGE/Applications"
    hdiutil create -volname Banyan -srcfolder "$STAGE" -ov -format UDZO "dist/Banyan-$VERSION-macos-$arch.dmg"
    # The zip is what the app updates from, replacing the bundle.
    ditto -c -k --keepParent "$STAGE/Banyan.app" "dist/Banyan-$VERSION-macos-$arch.zip"
    rm -rf "$STAGE"
done
