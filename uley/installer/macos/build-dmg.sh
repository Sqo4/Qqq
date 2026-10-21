#!/usr/bin/env bash
# Builds Uley-macos-arm64.dmg on a macOS runner.
# Usage: build-dmg.sh VERSION STAGE_DIR OUT_DIR   (STAGE_DIR: uley + engine/)
set -euo pipefail
VERSION="${1#v}"; STAGE="$2"; OUT="$3"
HERE="$(cd "$(dirname "$0")" && pwd)"
W="$(mktemp -d)"
APP="$W/Uley.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$STAGE/uley" "$APP/Contents/MacOS/uley"
cp -r "$STAGE/engine" "$APP/Contents/Resources/engine"
cp "$HERE/../NOTICE.txt" "$APP/Contents/Resources/"

# Icon: build an .icns from the 512px PNG.
ICONSET="$W/uley.iconset"; mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  sips -z $s $s "$HERE/../icon-512.png" --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
  d=$((s*2)); [ $d -le 512 ] && sips -z $d $d "$HERE/../icon-512.png" --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/uley.icns"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>Uley</string>
  <key>CFBundleDisplayName</key><string>Улей</string>
  <key>CFBundleIdentifier</key><string>org.uley.client</string>
  <key>CFBundleExecutable</key><string>uley</string>
  <key>CFBundleIconFile</key><string>uley</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
  <key>LSUIElement</key><true/>
</dict></plist>
PLIST

# Ad-hoc signature so Apple Silicon agrees to run it; a Developer ID
# signature and notarization can replace this later.
codesign --force --deep --sign - "$APP"

mkdir -p "$OUT"
DMGSRC="$W/dmg"; mkdir -p "$DMGSRC"
mv "$APP" "$DMGSRC/"
ln -s /Applications "$DMGSRC/Applications"
# hdiutil sometimes hangs on CI runners ("Resource busy"): limit each try
# to 5 minutes and retry.
for i in 1 2 3 4 5; do
  if perl -e 'alarm 300; exec @ARGV' hdiutil create -volname "Uley" -srcfolder "$DMGSRC" -fs HFS+ -ov -format UDZO "$OUT/Uley-macos-arm64.dmg"; then
    exit 0
  fi
  echo "hdiutil failed (try $i), retrying"; sleep 10
done
exit 1
