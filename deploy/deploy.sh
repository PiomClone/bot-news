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

echo "Обновление репозитория..."
git pull origin master

echo "Сборка приложения..."
go build -ldflags="-s -w" -o "$INSTALL_DIR/$BINARY_NAME" ./cmd/bot-news
chown "$USER_GROUP:$USER_GROUP" "$INSTALL_DIR/$BINARY_NAME"

echo "Копирование .env..."
if [ -f "configs/.env" ]; then
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
