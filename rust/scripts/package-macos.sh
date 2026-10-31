#!/bin/sh
# Build Banyan.app for Apple silicon and for Intel and put each in dist/ as
# a disk image.
set -e
cd "$(dirname "$0")/.."

VERSION=$(grep -m1 '^version' Cargo.toml | cut -d '"' -f 2)
mkdir -p dist
for arch in arm64 x86_64; do
    target=$([ $arch = arm64 ] && echo aarch64-apple-darwin || echo x86_64-apple-darwin)
    scripts/bundle.sh --target $target
    STAGE=$(mktemp -d)
    cp -R "target/$target/release/Banyan.app" "$STAGE/"
    ln -s /Applications "$STAGE/Applications"
    hdiutil create -volname Banyan -srcfolder "$STAGE" -ov -format UDZO "dist/Banyan-$VERSION-macos-$arch.dmg"
    rm -rf "$STAGE"
done
