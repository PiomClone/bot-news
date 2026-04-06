# bot-news

Telegram-бот на Go, который собирает RSS-ленты, раз в день делает AI-дайджест через Groq API и отправляет в канал.

## Как работает

```
[cron каждые 30 мин]  → собирает статьи из RSS → сохраняет в SQLite (без дублей)
[cron 09:00 каждый день] → берёт несохранённые статьи → делает саммари → отправляет в Telegram
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

## Конфигурация

| Переменная | По умолчанию | Описание |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | — | Токен от @BotFather |
| `TELEGRAM_CHANNEL_ID` | — | `@username` или числовой ID канала |
| `FEED_URLS` | — | RSS-ленты через запятую |
| `FETCH_INTERVAL_MINUTES` | `30` | Как часто собирать статьи |
| `DIGEST_CRON` | `0 9 * * *` | Когда отправлять дайджест (cron) |
| `DB_PATH` | `bot-news.db` | Путь к файлу SQLite |
| `GROQ_API_KEY` | — | Если пусто — простой список без AI |
| `GROQ_MODEL` | `llama-3.3-70b-versatile` | Модель Groq (1000 req/day бесплатно) |
| `HEALTH_ADDR` | `:8080` | Адрес HTTP health check |
| `LOG_LEVEL` | `info` | Уровень логов (debug/info/warn/error) |

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
sudo nano /opt/bot-news/.env   # заполнить токены
sudo chmod 600 /opt/bot-news/.env
```

**2. Первый деплой и все последующие обновления**

```bash
cd /opt/bot-news-src
sudo bash deploy/deploy.sh
```

Скрипт: делает `git pull` → собирает Go-бинарник → копирует в `/opt/bot-news/` → перезапускает сервис.

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
make dev             # запуск с автоперезагрузкой при изменении файлов
make test            # тесты с покрытием → coverage.html
make lint            # линтер
make check           # lint + test вместе
```

## Структура проекта

```
cmd/bot-news/main.go          — точка входа, инициализация, cron, graceful shutdown
internal/config/              — загрузка конфига из .env и окружения
internal/feed/                — сбор RSS (concurrent, retry per feed)
internal/storage/             — SQLite: сохранение статей, дедупликация по GUID
internal/summarizer/          — SimpleSummarizer (список) и GroqSummarizer (AI)
internal/notifier/            — отправка в Telegram (telebot.v3, retry)
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
