package config_test

import (
	"os"
	"testing"

	"bot-news/internal/config"
)

func TestLoadFromEnv_Defaults(t *testing.T) {
	// Чистим переменные, чтобы не было влияния окружения
	os.Unsetenv("TELEGRAM_BOT_TOKEN")
	os.Unsetenv("TELEGRAM_CHANNEL_ID")
	os.Unsetenv("FEED_URLS")
	os.Unsetenv("FETCH_INTERVAL_MINUTES")
	os.Unsetenv("DIGEST_CRON")
	os.Unsetenv("DB_PATH")
	os.Unsetenv("GROQ_MODEL")
	os.Unsetenv("HEALTH_ADDR")

	cfg := config.LoadFromEnv()

	if cfg.FetchIntervalMin != 30 {
		t.Errorf("FetchIntervalMin: ожидали 30, получили %d", cfg.FetchIntervalMin)
	}
	if cfg.DigestCron != "0 9 * * *" {
		t.Errorf("DigestCron: ожидали '0 9 * * *', получили %q", cfg.DigestCron)
	}
	if cfg.DBPath != "bot-news.db" {
		t.Errorf("DBPath: ожидали 'bot-news.db', получили %q", cfg.DBPath)
	}
	if cfg.GroqModel != "llama-3.3-70b-versatile" {
		t.Errorf("GroqModel: ожидали 'llama-3.3-70b-versatile', получили %q", cfg.GroqModel)
	}
	if cfg.HealthAddr != ":8080" {
		t.Errorf("HealthAddr: ожидали ':8080', получили %q", cfg.HealthAddr)
	}
}

func TestLoadFromEnv_FeedURLs(t *testing.T) {
	os.Setenv("FEED_URLS", "https://a.com/rss, https://b.com/rss , https://c.com/rss")
	defer os.Unsetenv("FEED_URLS")

	cfg := config.LoadFromEnv()

	if len(cfg.FeedURLs) != 3 {
		t.Fatalf("ожидали 3 URL, получили %d: %v", len(cfg.FeedURLs), cfg.FeedURLs)
	}
	if cfg.FeedURLs[1] != "https://b.com/rss" {
		t.Errorf("пробелы не обрезаны: %q", cfg.FeedURLs[1])
	}
}

func TestLoadFromEnv_FetchInterval_Invalid(t *testing.T) {
	os.Setenv("FETCH_INTERVAL_MINUTES", "not-a-number")
	defer os.Unsetenv("FETCH_INTERVAL_MINUTES")

	cfg := config.LoadFromEnv()
	if cfg.FetchIntervalMin != 30 {
		t.Errorf("при невалидном значении должен быть дефолт 30, получили %d", cfg.FetchIntervalMin)
	}
}
