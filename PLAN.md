# bot-news — RSS Telegram Digest Bot (Go)

## Описание

Учебный проект: Telegram-бот на Go, который собирает статьи из RSS-лент, раз в день делает AI-саммари через бесплатный LLM API (Groq) и отправляет дайджест в Telegram-канал.

---

## Архитектура

```
[cron: каждые 30 мин] → feed.FetchAll() → storage.SaveArticles() (dedup by GUID)
[cron: 09:00 daily]   → storage.GetUnsent() → summarizer.Summarize() → notifier.Send() → storage.MarkSent()
```

---

## Структура проекта

```
bot-news/
├── cmd/bot-news/main.go          # точка входа, wiring, cron, signal handling
├── internal/
│   ├── config/config.go          # Config struct + LoadFromEnv() + LoadDotEnv()
│   ├── feed/fetcher.go           # FetchAll() — concurrent RSS fetch (gofeed)
│   ├── storage/db.go             # SQLite: Article, SaveArticles, GetUnsent, MarkSent
│   ├── summarizer/
│   │   ├── summarizer.go         # interface Summarizer + SimpleSummarizer
│   │   └── groq.go               # GroqSummarizer (OpenAI-compatible via go-openai)
│   └── notifier/telegram.go      # Send() с разбивкой по 4096 символов
├── .env.example                  # пример конфигурации
├── .gitignore
├── Makefile
├── go.mod
└── PLAN.md                       # этот файл
```

---

## Зависимости

| Библиотека | Версия | Назначение |
|---|---|---|
| `github.com/mmcdole/gofeed` | v1.3.0 | Парсинг RSS/Atom/JSON Feed |
| `github.com/go-telegram-bot-api/telegram-bot-api/v5` | v5.5.1 | Telegram Bot API |
| `modernc.org/sqlite` | v1.32.0 | Pure Go SQLite (без CGO) |
| `github.com/robfig/cron/v3` | v3.0.1 | Cron-расписание (`"0 9 * * *"`) |
| `github.com/sashabaranov/go-openai` | v1.36.1 | OpenAI-совместимый клиент для Groq |

---

## Конфигурация (.env)

```env
TELEGRAM_BOT_TOKEN=       # токен от @BotFather
TELEGRAM_CHANNEL_ID=@mychannel
FEED_URLS=https://rsshub.app/telegram/channel/hranidengi
FETCH_INTERVAL_MINUTES=30
DIGEST_CRON=0 9 * * *
DB_PATH=bot-news.db
GROQ_API_KEY=             # console.groq.com — бесплатно, без карты
GROQ_MODEL=llama-3.3-70b-versatile
```

---

## LLM (бесплатно)

**Groq API** — `llama-3.3-70b-versatile`
- 1000 запросов в день бесплатно
- Без кредитной карты
- OpenAI-совместимый endpoint: `https://api.groq.com/openai/v1`
- Регистрация: https://console.groq.com

Если `GROQ_API_KEY` не задан — используется `SimpleSummarizer` (простой список заголовков).

---

## Запуск

```bash
# Скопировать и заполнить конфиг
cp .env.example .env

# Запуск
make run

# Немедленно отправить дайджест (для теста)
make digest-now

# Сборка бинарника
make build
```

---

## Настройка Telegram

1. Создать бота через [@BotFather](https://t.me/BotFather) → получить токен
2. Создать канал (или использовать существующий)
3. Добавить бота в канал как **администратора** с правом отправки сообщений
4. Указать `@username` канала в `TELEGRAM_CHANNEL_ID`

---

## Схема БД (SQLite)

```sql
CREATE TABLE articles (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    feed_url     TEXT NOT NULL,
    guid         TEXT NOT NULL UNIQUE,   -- ключ дедупликации
    title        TEXT NOT NULL,
    link         TEXT NOT NULL,
    description  TEXT,
    published_at INTEGER,               -- unix epoch
    fetched_at   INTEGER NOT NULL,
    sent         INTEGER DEFAULT 0
);
```

---

## Возможные расширения

- **MTProto** (`gotd/td`) — чтение статистики каналов (просмотры, реакции)
- **HTTP endpoint** — ручной запуск дайджеста через веб
- **Фильтрация** — отбор статей по ключевым словам
- **Несколько каналов** — рассылка в разные Telegram-каналы
