package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	TelegramBotToken  string
	TelegramChannelID string
	FeedURLs          []string
	FetchIntervalMin  int
	DigestCron        string
	DBPath            string
	GroqAPIKey        string
	GroqModel         string
	HealthAddr        string // HTTP-адрес для /health, например ":8080"
}

func LoadFromEnv() Config {
	cfg := Config{
		TelegramBotToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChannelID: os.Getenv("TELEGRAM_CHANNEL_ID"),
		DigestCron:        getEnvOrDefault("DIGEST_CRON", "0 9 * * *"),
		DBPath:            getEnvOrDefault("DB_PATH", "bot-news.db"),
		GroqAPIKey:        os.Getenv("GROQ_API_KEY"),
		GroqModel:         getEnvOrDefault("GROQ_MODEL", "llama-3.3-70b-versatile"),
		HealthAddr:        getEnvOrDefault("HEALTH_ADDR", ":8080"),
	}

	if raw := os.Getenv("FEED_URLS"); raw != "" {
		for _, u := range strings.Split(raw, ",") {
			u = strings.TrimSpace(u)
			if u != "" {
				cfg.FeedURLs = append(cfg.FeedURLs, u)
			}
		}
	}

	if v, err := strconv.Atoi(os.Getenv("FETCH_INTERVAL_MINUTES")); err == nil && v > 0 {
		cfg.FetchIntervalMin = v
	} else {
		cfg.FetchIntervalMin = 30
	}

	return cfg
}

// LoadDotEnv ищет .env файл в нескольких стандартных местах.
// Переменные из файла не перезаписывают уже установленные env vars.
func LoadDotEnv(candidates ...string) {
	if len(candidates) == 0 {
		candidates = []string{".env", "configs/.env", "../.env"}
	}
	for _, path := range candidates {
		if loadDotEnvFile(path) {
			return
		}
	}
}

func loadDotEnvFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
	return true
}

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
