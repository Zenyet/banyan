#!/bin/sh
# Build Banyan for Linux and put a tarball and a .deb in dist/.
# Needs cargo-deb (cargo install cargo-deb).
set -e
cd "$(dirname "$0")/.."

VERSION=$(grep -m1 '^version' Cargo.toml | cut -d '"' -f 2)
ARCH=$(uname -m)
cargo build --release

NAME="Banyan-$VERSION-linux-$ARCH"
STAGE=$(mktemp -d)
mkdir "$STAGE/$NAME"
cp target/release/banyan packaging/linux/banyan.desktop packaging/linux/banyan.png README.md "$STAGE/$NAME/"
mkdir -p dist
tar -C "$STAGE" -czf "dist/$NAME.tar.gz" "$NAME"
rm -rf "$STAGE"

cargo deb --no-build --output dist/
