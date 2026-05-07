#!/bin/bash
set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Usage: $0 <ssh-host> [goos] [goarch]" >&2
    exit 1
fi

HOST="$1"
GOOS_TARGET="${2:-linux}"
GOARCH_TARGET="${3:-amd64}"
INSTALL_DIR="/opt/bot-news"
BINARY_NAME="bot-news"
USER_GROUP="botnews"
SERVICE_NAME="bot-news"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
TMP_BINARY="$(mktemp)"

cleanup() {
    rm -f "$TMP_BINARY"
}
trap cleanup EXIT

cd "$PROJECT_DIR"

echo "Тесты..."
go test ./...

echo "Сборка $GOOS_TARGET/$GOARCH_TARGET..."
GOOS="$GOOS_TARGET" GOARCH="$GOARCH_TARGET" go build -ldflags="-s -w" -o "$TMP_BINARY" ./cmd/bot-news

echo "Загрузка на $HOST..."
scp "$TMP_BINARY" "$HOST:/tmp/$BINARY_NAME.new"

echo "Установка и перезапуск..."
ssh "$HOST" "set -e;
    sudo cp '$INSTALL_DIR/$BINARY_NAME' '$INSTALL_DIR/$BINARY_NAME.bak.'\$(date +%Y%m%d%H%M%S) 2>/dev/null || true;
    sudo install -o '$USER_GROUP' -g '$USER_GROUP' -m 0755 '/tmp/$BINARY_NAME.new' '$INSTALL_DIR/$BINARY_NAME';
    sudo systemctl restart '$SERVICE_NAME';
    sleep 1;
    systemctl is-active '$SERVICE_NAME';
    # Пробуем HTTP, если не вышло — HTTPS (для mTLS/TLS режима)
    curl -fsS --max-time 3 http://127.0.0.1:8080/health 2>/dev/null || \
    curl -fsSk --max-time 3 https://127.0.0.1:8080/health;
    echo "" # Перенос строки после 'ok'
    rm -f '/tmp/$BINARY_NAME.new'"

echo "Готово"
