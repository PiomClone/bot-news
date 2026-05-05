#!/bin/bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "Скрипт должен быть запущен от root" >&2
    exit 1
fi

INSTALL_DIR="/opt/bot-news"
BINARY_NAME="bot-news"
USER_GROUP="botnews"
SERVICE_NAME="bot-news"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

if [ -d ".git" ]; then
    echo "Обновление репозитория..."
    git pull origin master
fi

echo "Сборка приложения..."
if command -v go >/dev/null 2>&1; then
    tmp_binary="$(mktemp)"
    go build -ldflags="-s -w" -o "$tmp_binary" ./cmd/bot-news
elif [ -x "build/$BINARY_NAME" ]; then
    tmp_binary="build/$BINARY_NAME"
else
    echo "Не найден Go и нет готового build/$BINARY_NAME" >&2
    exit 1
fi

install -o "$USER_GROUP" -g "$USER_GROUP" -m 0755 "$tmp_binary" "$INSTALL_DIR/$BINARY_NAME"
if [[ "$tmp_binary" == /tmp/* ]]; then
    rm -f "$tmp_binary"
fi

if [ -f "configs/.env" ] && [ ! -f "$INSTALL_DIR/.env" ]; then
    echo "Копирование .env..."
    cp "configs/.env" "$INSTALL_DIR/.env"
    chown "$USER_GROUP:$USER_GROUP" "$INSTALL_DIR/.env"
    chmod 600 "$INSTALL_DIR/.env"
fi

echo "Перезапуск сервиса..."
if systemctl is-active --quiet "$SERVICE_NAME"; then
    systemctl restart "$SERVICE_NAME"
else
    systemctl start "$SERVICE_NAME"
fi

echo "Готово! Статус:"
systemctl status "$SERVICE_NAME" --no-pager -l
