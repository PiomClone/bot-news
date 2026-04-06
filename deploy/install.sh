#!/bin/bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "Скрипт должен быть запущен от root" >&2
    exit 1
fi

INSTALL_DIR="/opt/bot-news"
USER_GROUP="botnews"
SERVICE_NAME="bot-news"

echo "Установка bot-news..."

# Пользователь и группа
if ! id "$USER_GROUP" &>/dev/null; then
    useradd --system --no-create-home --shell /bin/false "$USER_GROUP"
    echo "Пользователь $USER_GROUP создан"
fi

# Директория
mkdir -p "$INSTALL_DIR"
chown "$USER_GROUP:$USER_GROUP" "$INSTALL_DIR"

# systemd unit
cp "$(dirname "$0")/bot-news.service" /etc/systemd/system/
systemctl daemon-reload
systemctl enable "$SERVICE_NAME"

echo "Установка завершена!"
echo "Дальнейшие шаги:"
echo "  1. Скопируйте бинарник: cp build/bot-news $INSTALL_DIR/"
echo "  2. Создайте $INSTALL_DIR/.env (см. configs/.env.example)"
echo "  3. Запустите: systemctl start $SERVICE_NAME"
echo "  4. Логи: journalctl -u $SERVICE_NAME -f"
