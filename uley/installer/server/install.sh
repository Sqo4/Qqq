#!/usr/bin/env bash
# Ставит сервер Улья на VPS с Ubuntu или Debian одной командой:
#
#   curl -fsSL https://github.com/Sqo4/Qqq/releases/download/<версия>/uley-server-install.sh | sudo bash
#
# Без своего домена адрес будет вида https://1-2-3-4.sslip.io (по IP сервера),
# сертификат HTTPS сервер получает сам. Свой домен:  ... | sudo ULEY_DOMAIN=hive.example.ru bash
# Другая модель:                                      ... | sudo ULEY_MODEL=qwen3-8b-chain bash
# Повторный запуск обновляет сервер до свежей версии, данные сохраняются.
set -euo pipefail

REPO=Sqo4/Qqq
MODEL=${ULEY_MODEL:-qwen3-4b-chain}
APP=/opt/uley
DATA=/var/lib/uley

say() { printf '\n\033[1;33m🐝 %s\033[0m\n' "$*"; }
[ "$(id -u)" = 0 ] || { echo "Запустите через sudo"; exit 1; }
command -v systemctl >/dev/null || { echo "Нужен Linux с systemd (Ubuntu 20.04+ или Debian 11+)"; exit 1; }
[ "$(uname -m)" = x86_64 ] || { echo "Пока поддерживаются только серверы x86_64"; exit 1; }

say "Готовим систему"
if command -v apt-get >/dev/null; then
  apt-get update -qq && apt-get install -y -qq curl ca-certificates tar libgomp1 >/dev/null
fi

say "Ищем свежую версию Улья"
TAG=${ULEY_TAG:-$(curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=30" |
  grep -o '"tag_name": *"uley-v[^"]*"' | head -1 | sed 's/.*"\(uley-v[^"]*\)"/\1/')}
[ -n "$TAG" ] || { echo "Не удалось узнать версию на GitHub"; exit 1; }
echo "версия: $TAG"

say "Скачиваем сервер"
tmp=$(mktemp -d)
curl -fL --progress-bar -o "$tmp/server.tar.gz" "https://github.com/$REPO/releases/download/$TAG/uley-server-linux-amd64.tar.gz"
curl -fsSL -o "$tmp/SHA256SUMS.txt" "https://github.com/$REPO/releases/download/$TAG/SHA256SUMS.txt"
(cd "$tmp" && grep ' uley-server-linux-amd64.tar.gz$' SHA256SUMS.txt | sed 's/uley-server-linux-amd64.tar.gz$/server.tar.gz/' | sha256sum -c -)
systemctl stop uley 2>/dev/null || true
rm -rf "$APP" && mkdir -p "$APP" && tar -xzf "$tmp/server.tar.gz" -C "$APP"
rm -rf "$tmp"

if [ -z "${ULEY_DOMAIN:-}" ]; then
  IP=$(curl -fsS4 https://api.ipify.org || curl -fsS4 https://ifconfig.me)
  ULEY_DOMAIN="${IP//./-}.sslip.io"
fi
echo "адрес: https://$ULEY_DOMAIN"

id uley >/dev/null 2>&1 || useradd --system --home-dir "$DATA" --shell /usr/sbin/nologin uley
mkdir -p "$DATA" && chown -R uley:uley "$DATA"

# Маленьким VPS (1–2 ГБ памяти) нужен файл подкачки, пока модель грузится.
if [ "$(awk '/MemTotal/{print int($2/1024)}' /proc/meminfo)" -lt 3500 ] && [ "$(swapon --show | wc -l)" = 0 ]; then
  say "Добавляем файл подкачки 2 ГБ"
  fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap -q /swapfile && swapon /swapfile
  grep -q '^/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

say "Запускаем службу uley"
cat > /etc/systemd/system/uley.service <<UNIT
[Unit]
Description=Улей — сервер
After=network-online.target
Wants=network-online.target

[Service]
User=uley
WorkingDirectory=$DATA
Environment=LD_LIBRARY_PATH=$APP/engine
ExecStart=$APP/uley-coordinator -domain $ULEY_DOMAIN -model $MODEL -db $DATA/uley.db -llama-server $APP/engine/llama-server
AmbientCapabilities=CAP_NET_BIND_SERVICE
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now uley >/dev/null

if command -v ufw >/dev/null && ufw status | grep -q 'Status: active'; then
  ufw allow 80/tcp >/dev/null && ufw allow 443/tcp >/dev/null
fi

sleep 5
if systemctl is-active --quiet uley; then
  say "Готово! Улей работает: https://$ULEY_DOMAIN"
  cat <<TXT

  1. Откройте https://$ULEY_DOMAIN в браузере. Первые полминуты адрес может
     не открываться: сервер получает сертификат HTTPS.
  2. Участники вписывают этот адрес в программе Улей:
     «Настройки этого компьютера» → «Дополнительно» → «Адрес координатора».
  3. Сервер сейчас скачивает модель. Что он делает, видно на табло и командой:
       journalctl -u uley -f

TXT
else
  echo "Служба не запустилась. Посмотрите, что случилось: journalctl -u uley -n 50"
  exit 1
fi
