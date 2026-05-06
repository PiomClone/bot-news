# bot-news

[English version](./README.en.md)

Telegram-бот на Go, который собирает RSS-ленты, два раза в день делает AI-дайджест через Groq API и отправляет в канал.

## Как работает

```
[cron каждые 30 мин]  → собирает статьи из RSS → сохраняет в SQLite (без дублей)
[cron 09:00 и 21:00] → берёт несохранённые статьи за 12 часов → делает саммари → отправляет в Telegram
```

## Быстрый старт (локально)

**1. Получить токены**

- Telegram-бот: открыть [@BotFather](https://t.me/BotFather), создать бота, скопировать токен
- Добавить бота в канал как **администратора** с правом отправки сообщений
- Groq API (бесплатно, без карты): [console.groq.com](https://console.groq.com)

**2. Создать конфиг**

```bash
cp configs/.env.example configs/.env
# заполнить TELEGRAM_BOT_TOKEN, TELEGRAM_CHANNEL_ID, FEED_URLS, GROQ_API_KEY
```

**3. Запустить**

```bash
make run
```

Для немедленного теста (получить RSS + отправить дайджест прямо сейчас):

```bash
make digest-now
```

## Веб-панель (Dashboard)

Бот имеет встроенную веб-панель для управления фидами и просмотра статистики. Она доступна по адресу `HEALTH_ADDR` (по умолчанию `:8080`).

Для защиты панели используется **mTLS** (Mutual TLS) — авторизация по клиентскому сертификату.

### Как настроить доступ:
1. Сгенерируйте сертификаты: `make certs`.
2. Установите файл `certs/client.p12` в свой браузер (подробнее в [INSTRUCTIONS_MTLS.md](./INSTRUCTIONS_MTLS.md)).
3. Включите TLS в `.env`: `TLS_ENABLED=true`.
4. Теперь админка доступна только по `https://your-host:8080/`.

## Управление в Telegram

Администратор (`TELEGRAM_ADMIN_ID`) может управлять ботом через команды:
- `/feeds` — интерактивный список всех источников (включение/выключение кнопками).
- `/fetch` — принудительный сбор новостей.
- `/digest` — запуск формирования дайджеста.
- `/stats` — статистика базы данных.
- `/latest` — быстрый AI-обзор последних новостей.

## Конфигурация

| Переменная | По умолчанию | Описание |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | — | Токен от @BotFather |
| `TELEGRAM_CHANNEL_ID` | — | `@username` или числовой ID канала |
| `FEED_URLS` | — | RSS-ленты через запятую |
| `FETCH_INTERVAL_MINUTES` | `30` | Как часто собирать статьи |
| `DIGEST_CRON` | `CRON_TZ=Europe/Moscow 0 9,21 * * *` | Когда отправлять дайджест (cron) |
| `DB_PATH` | `bot-news.db` | Путь к файлу SQLite |
| `GROQ_API_KEY` | — | Если пусто — простой список без AI |
| `GROQ_MODEL` | `llama-3.3-70b-versatile` | Модель Groq (1000 req/day бесплатно) |
| `HEALTH_ADDR` | `:8080` | Адрес HTTP health check |
| `TRIGGER_SECRET` | — | Bearer-токен для `/trigger/*` |
| `TELEGRAM_ADMIN_ID` | — | ID пользователя, которому разрешены команды боту |
| `TIMEZONE` | `Europe/Moscow` | Часовой пояс для дат и статистики |
| `LOG_LEVEL` | `info` | Уровень логов (debug/info/warn/error) |
| `TLS_ENABLED` | `false` | Включить HTTPS и mTLS для админки |
| `SERVER_CERT` | `certs/server.crt` | Путь к сертификату сервера |
| `SERVER_KEY` | `certs/server.key` | Путь к ключу сервера |
| `CA_CERT` | `certs/ca.crt` | Путь к CA для проверки клиентов |

## Деплой через Docker Compose

```bash
# Скопировать конфиг (НЕ .env.example, а реальный .env)
cp configs/.env.example configs/.env
# заполнить токены...

docker compose up -d
docker compose logs -f

# Проверить здоровье
curl http://localhost:8080/health
```

## Деплой на Linux-сервер (systemd)

**1. На сервере: первичная установка (один раз)**

```bash
# Клонировать репозиторий
git clone <repo-url> /opt/bot-news-src
cd /opt/bot-news-src

# Установить systemd unit и создать пользователя botnews
sudo bash deploy/install.sh

# Создать конфиг
sudo cp configs/.env.example /opt/bot-news/.env
sudo vim /opt/bot-news/.env   # заполнить токены
sudo chmod 600 /opt/bot-news/.env
```

**2. Первый деплой и все последующие обновления**

```bash
cd /opt/bot-news-src
sudo bash deploy/deploy.sh
```

Скрипт: делает `git pull` при наличии git-репозитория → собирает Go-бинарник или берёт `build/bot-news` → копирует в `/opt/bot-news/` → перезапускает сервис.

Если на сервере нет Go и исходников, обновление можно запускать локально:

```bash
make deploy-h2
# или
bash deploy/deploy-remote.sh h2
```

**3. Управление сервисом**

```bash
systemctl status bot-news
systemctl restart bot-news
journalctl -u bot-news -f          # логи в реальном времени
journalctl -u bot-news --since "1h ago"
```

## Разработка

```bash
make install-tools   # установить golangci-lint, goimports, air
make install-hooks   # установить git-хуки для автоматической проверки перед пушем
make dev             # запуск с автоперезагрузкой при изменении файлов
make test            # тесты с покрытием → coverage.html
make lint            # линтер
make check           # lint + test вместе
```

## Релизы

Проект использует **GoReleaser** для автоматического создания релизов на GitHub.

Чтобы выпустить новую версию:
1. Убедитесь, что все изменения закоммичены и запушены в `master`.
2. Создайте новый тег:
   ```bash
   make tag v=1.1.0
   ```
3. GitHub Actions автоматически соберет бинарники под все платформы и создаст страницу релиза.

## Структура проекта

```
cmd/bot-news/main.go          — точка входа, инициализация, крон, graceful shutdown
internal/app/                 — основная логика приложения и веб-сервер
internal/config/              — загрузка конфига из .env и окружения
internal/feed/                — сбор RSS (concurrent, retry per feed)
internal/storage/             — SQLite: сохранение статей, управление фидами
internal/summarizer/          — SimpleSummarizer (список) и GroqSummarizer (AI)
internal/notifier/            — отправка в Telegram (telebot.v3, команды)
internal/retry/               — exponential backoff retry
internal/logger/              — инициализация slog (JSON, уровни)
deploy/                       — systemd unit, install.sh, deploy.sh
configs/                      — .env.example
```

## Логи

Бот пишет структурированные JSON-логи в stdout:

```json
{"time":"2026-04-06T09:00:00Z","level":"INFO","service":"bot-news","msg":"дайджест отправлен","articles":42}
```

В systemd смотреть через `journalctl`, в Docker через `docker compose logs`.
