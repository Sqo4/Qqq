#!/usr/bin/env bash
# Builds uley_linux_amd64.deb and uley-linux-amd64.tar.gz.
# Usage: build-packages.sh VERSION STAGE_DIR OUT_DIR
#   STAGE_DIR contains uley (binary) and engine/ (llama-server + libs).
set -euo pipefail
VERSION="$1"; STAGE="$2"; OUT="$3"
HERE="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$OUT"

# --- tar.gz: unpack anywhere and run ./uley
T="$(mktemp -d)/uley-linux-amd64"
mkdir -p "$T"
cp -r "$STAGE/uley" "$STAGE/engine" "$HERE/../NOTICE.txt" "$T/"
cp "$HERE/../icon-512.png" "$T/uley.png"
cat > "$T/README.txt" <<TXT
Улей $VERSION для Linux.
Запуск: ./uley  (откроется окно согласия в браузере, значок появится в трее).
Нужны драйверы видеокарты с поддержкой Vulkan (пакет libvulkan1 / vulkan-loader).
Сервер без экрана: ./uley -terms, затем ./uley -headless -accept
TXT
tar -C "$(dirname "$T")" -czf "$OUT/uley-linux-amd64.tar.gz" uley-linux-amd64

# --- .deb
D="$(mktemp -d)/pkg"
mkdir -p "$D/DEBIAN" "$D/usr/lib/uley" "$D/usr/bin" "$D/usr/share/applications" "$D/usr/share/icons/hicolor/512x512/apps" "$D/usr/share/doc/uley"
cp -r "$STAGE/uley" "$STAGE/engine" "$D/usr/lib/uley/"
ln -s ../lib/uley/uley "$D/usr/bin/uley"
cp "$HERE/../icon-512.png" "$D/usr/share/icons/hicolor/512x512/apps/uley.png"
cp "$HERE/../NOTICE.txt" "$D/usr/share/doc/uley/copyright"
cat > "$D/usr/share/applications/uley.desktop" <<DESK
[Desktop Entry]
Type=Application
Name=Uley
Name[ru]=Улей
Comment=Volunteer distributed neural network
Comment[ru]=Распределённая нейросеть на видеокартах добровольцев
Exec=/usr/lib/uley/uley
Icon=uley
Categories=Utility;
DESK
cat > "$D/DEBIAN/control" <<CTRL
Package: uley
Version: ${VERSION#v}
Section: utils
Priority: optional
Architecture: amd64
Depends: libvulkan1
Recommends: xprintidle
Maintainer: Uley <noreply@github.com>
Homepage: https://github.com/Sqo4/Qqq
Description: Uley volunteer client for a distributed neural network
 Lends the GPU to the Uley hive while the computer is idle, with the
 owner's explicit consent. Runs only whitelisted open language models.
CTRL
dpkg-deb --root-owner-group --build "$D" "$OUT/uley_linux_amd64.deb"
ls -la "$OUT"
