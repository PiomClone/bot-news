#!/bin/bash
set -e

# Конфигурация
CERT_DIR="certs"
CA_NAME="bot-news-ca"
SERVER_NAME="bot-news-server"
CLIENT_NAME="bot-news-client"
DAYS=3650

mkdir -p "$CERT_DIR"
cd "$CERT_DIR"

echo "=== Генерируем Root CA ==="
openssl genrsa -out ca.key 4096
openssl req -x509 -new -nodes -key ca.key -sha256 -days "$DAYS" -out ca.crt \
    -subj "/CN=$CA_NAME/O=bot-news/C=RU"

echo "=== Генерируем сертификат сервера ==="
openssl genrsa -out server.key 2048
openssl req -new -key server.key -out server.csr \
    -subj "/CN=localhost/O=bot-news"

# Настройка SAN (Subject Alternative Name) для современных браузеров
cat > server.ext <<EOF
authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage = digitalSignature, nonRepudiation, keyEncipherment, dataEncipherment
subjectAltName = @alt_names

[alt_names]
DNS.1 = localhost
IP.1 = 127.0.0.1
EOF

openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
    -out server.crt -days "$DAYS" -sha256 -extfile server.ext

echo "=== Генерируем сертификат клиента (для браузера) ==="
openssl genrsa -out client.key 2048
openssl req -new -key client.key -out client.csr \
    -subj "/CN=$CLIENT_NAME/O=bot-news"

openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
    -out client.crt -days "$DAYS" -sha256

echo "=== Конвертируем клиентский сертификат в .p12 (PKCS#12) ==="
echo "!!! ВНИМАНИЕ: Сейчас вам нужно будет ввести пароль для защиты .p12 файла."
echo "!!! Этот пароль потребуется при импорте сертификата в браузер."
openssl pkcs12 -export -out client.p12 -inkey client.key -in client.crt -certfile ca.crt

echo ""
echo "=== Готово! ==="
echo "Файлы для сервера (в папке certs/):"
echo "  - ca.crt (CA_CERT)"
echo "  - server.crt (SERVER_CERT)"
echo "  - server.key (SERVER_KEY)"
echo ""
echo "Файл для браузера (нужно скачать себе):"
echo "  - certs/client.p12"
echo ""
echo "Не забудьте включить TLS_ENABLED=true в вашем .env файле."
