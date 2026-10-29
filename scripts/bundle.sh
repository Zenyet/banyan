#!/bin/sh
# Build target/release/Banyan.app. Usage: scripts/bundle.sh
set -e
cd "$(dirname "$0")/.."

VERSION=$(grep -m1 '^version' Cargo.toml | cut -d '"' -f 2)
APP=target/release/Banyan.app

cargo build --release

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp target/release/banyan "$APP/Contents/MacOS/banyan"

# Icon: render once, then every size the icns needs.
WORK=$(mktemp -d)
ICONSET="$WORK/Banyan.iconset"
mkdir "$ICONSET"
swift scripts/make_icon.swift "$WORK/icon.png" >/dev/null
for size in 16 32 128 256 512; do
    sips -z $size $size "$WORK/icon.png" --out "$ICONSET/icon_${size}x${size}.png" >/dev/null
    double=$((size * 2))
    sips -z $double $double "$WORK/icon.png" --out "$ICONSET/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/Banyan.icns"
rm -rf "$WORK"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key><string>Banyan</string>
    <key>CFBundleDisplayName</key><string>Banyan</string>
    <key>CFBundleIdentifier</key><string>dev.banyan.app</string>
    <key>CFBundleExecutable</key><string>banyan</string>
    <key>CFBundleIconFile</key><string>Banyan</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleShortVersionString</key><string>$VERSION</string>
    <key>CFBundleVersion</key><string>$VERSION</string>
    <key>LSMinimumSystemVersion</key><string>11.0</string>
    <key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
    <key>NSHighResolutionCapable</key><true/>
    <key>NSSupportsAutomaticGraphicsSwitching</key><true/>
</dict>
</plist>
PLIST

# Ad-hoc signature so the bundle launches locally.
codesign --force --sign - "$APP" >/dev/null 2>&1
echo "$APP"
