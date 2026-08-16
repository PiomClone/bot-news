# bot-news

[Русская версия](./README.md)

A Go Telegram bot that collects RSS feeds, creates AI digests twice a day via the Groq API, and sends them to a channel.

## How It Works

```
[cron every 30 min]  → collects articles from RSS → saves to SQLite (deduplicated)
[cron 09:00 and 21:00] → takes unsent articles from the last 12h → generates summary → sends to Telegram
```

## Quick Start (Local)

**1. Get Tokens**

- Telegram Bot: Open [@BotFather](https://t.me/BotFather), create a bot, and copy the token.
- Add the bot to your channel as an **administrator** with message sending rights.
- Groq API (Free, no card required): [console.groq.com](https://console.groq.com)

**2. Create Config**

```bash
cp configs/.env.example configs/.env
# fill in TELEGRAM_BOT_TOKEN, TELEGRAM_CHANNEL_ID, FEED_URLS, GROQ_API_KEY
```

**3. Run**

```bash
make run
```

For an immediate test (fetch RSS + send digest right now):

```bash
make digest-now
```

## Web Dashboard

The bot has a built-in web dashboard for managing feeds and viewing statistics. It is available at `HEALTH_ADDR` (default `:8080`).

The panel is protected using **mTLS** (Mutual TLS) — authorization by client certificate.

### How to set up access:
1. Generate certificates: `make certs`.
2. Install the `certs/client.p12` file into your browser (more details in [INSTRUCTIONS_MTLS.md](./INSTRUCTIONS_MTLS.md)).
3. Enable TLS in `.env`: `TLS_ENABLED=true`.
4. Now the dashboard is available only at `https://your-host:8080/`.

## Telegram Management

The administrator (`TELEGRAM_ADMIN_ID`) can manage the bot via commands:
- `/feeds` — interactive list of all sources (enable/disable with buttons).
- `/fetch` — force news collection.
- `/digest` — trigger digest generation.
- `/stats` — database statistics.
- `/latest` — quick AI overview of the latest news.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | — | Token from @BotFather |
| `TELEGRAM_CHANNEL_ID` | — | `@username` or numeric channel ID |
| `FEED_URLS` | — | Comma-separated RSS feeds |
| `FETCH_INTERVAL_MINUTES` | `30` | How often to fetch articles |
| `DIGEST_CRON` | `CRON_TZ=Europe/Moscow 0 9,21 * * *` | When to send the digest (cron format) |
| `DB_PATH` | `bot-news.db` | Path to the SQLite file |
| `GROQ_API_KEY` | — | If empty — simple list without AI |
| `GROQ_MODEL` | `openai/gpt-oss-120b` | Groq model (1000 req/day free) |
| `HEALTH_ADDR` | `:8080` | HTTP health check address |
| `TRIGGER_SECRET` | — | Bearer token for `/trigger/*` |
| `TELEGRAM_ADMIN_ID` | — | User ID allowed to send commands to the bot |
| `TIMEZONE` | `Europe/Moscow` | Timezone for dates and statistics |
| `LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |
| `TLS_ENABLED` | `false` | Enable HTTPS and mTLS for dashboard |
| `SERVER_CERT` | `certs/server.crt` | Path to server certificate |
| `SERVER_KEY` | `certs/server.key` | Path to server key |
| `CA_CERT` | `certs/ca.crt` | Path to CA for client verification |

## Deployment via Docker Compose

```bash
# Copy config (use real .env, NOT .env.example)
cp configs/.env.example configs/.env
# fill in tokens...

docker compose up -d
docker compose logs -f

# Check health
curl http://localhost:8080/health
```

## Deployment to Linux Server (systemd)

**1. On the Server: Initial Installation (Once)**

```bash
# Clone the repository
git clone <repo-url> /opt/bot-news-src
cd /opt/bot-news-src

# Install systemd unit and create 'botnews' user
sudo bash deploy/install.sh

# Create config
sudo cp configs/.env.example /opt/bot-news/.env
sudo vim /opt/bot-news/.env   # fill in tokens
sudo chmod 600 /opt/bot-news/.env
```

**2. First Deployment and All Subsequent Updates**

```bash
cd /opt/bot-news-src
sudo bash deploy/deploy.sh
```

The script: performs `git pull` if a git repo is present → builds the Go binary or uses `build/bot-news` → copies it to `/opt/bot-news/` → restarts the service.

If the server doesn't have Go or sources, you can deploy locally:

```bash
make deploy-h2
# or
bash deploy/deploy-remote.sh h2
```

**3. Service Management**

```bash
systemctl status bot-news
systemctl restart bot-news
journalctl -u bot-news -f          # real-time logs
journalctl -u bot-news --since "1h ago"
```

## Development

```bash
make install-tools   # install golangci-lint, goimports, air
make install-hooks   # install git hooks for automatic pre-push checks
make dev             # run with auto-reload on file changes
make test            # run tests with coverage → coverage.html
make lint            # run linter
make check           # run lint + test together
```

## Releases

The project uses **GoReleaser** to automatically create GitHub Releases.

To release a new version:
1. Ensure all changes are committed and pushed to `master`.
2. Create a new tag:
   ```bash
   make tag v=1.1.0
   ```
3. GitHub Actions will automatically build binaries for all platforms and create a release page.

## Project Structure

```
cmd/bot-news/main.go          — entry point, initialization, cron, graceful shutdown
internal/app/                 — core application logic and web server
internal/config/              — config loading from .env and environment variables
internal/feed/                — RSS fetching (concurrent, retry per feed)
internal/storage/             — SQLite: saving articles, feed management
internal/summarizer/          — SimpleSummarizer (list) and GroqSummarizer (AI)
internal/notifier/            — Telegram notification (telebot.v3, commands)
internal/retry/               — exponential backoff retry
internal/logger/              — slog initialization (JSON, levels)
deploy/                       — systemd unit, install.sh, deploy.sh
configs/                      — .env.example
```

## Logs

The bot writes structured JSON logs to stdout:

```json
{"time":"2026-04-06T09:00:00Z","level":"INFO","service":"bot-news","msg":"digest sent","articles":42}
```

In systemd, view them via `journalctl`; in Docker, use `docker compose logs`.
