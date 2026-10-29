#!/bin/sh
# Build the universal Banyan.app and put it in dist/ as a disk image.
set -e
cd "$(dirname "$0")/.."

VERSION=$(grep -m1 '^version' Cargo.toml | cut -d '"' -f 2)
scripts/bundle.sh --universal

STAGE=$(mktemp -d)
cp -R target/release/Banyan.app "$STAGE/"
ln -s /Applications "$STAGE/Applications"
mkdir -p dist
hdiutil create -volname Banyan -srcfolder "$STAGE" -ov -format UDZO "dist/Banyan-$VERSION-macos-universal.dmg"
rm -rf "$STAGE"
