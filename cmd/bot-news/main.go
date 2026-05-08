package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"bot-news/internal/app"
	"bot-news/internal/config"
	"bot-news/internal/logger"
)

var version = "dev"

func main() {
	runNow := flag.Bool("run-digest-now", false, "немедленно отправить дайджест и выйти")
	showVersion := flag.Bool("version", false, "показать версию и выйти")
	flag.Parse()

	if *showVersion {
		fmt.Printf("bot-news version %s\n", version)
		return
	}

	logger.Init("bot-news")

	config.LoadDotEnv()
	cfg := config.LoadFromEnv()

	if err := validateConfig(cfg); err != nil {
		slog.Error("конфигурация невалидна", "error", err)
		os.Exit(1)
	}

	a, err := app.NewApp(cfg)
	if err != nil {
		slog.Error("инициализация приложения", "error", err)
		os.Exit(1)
	}
	defer a.Close()

	if *runNow {
		a.RunNow()
		return
	}

	a.Run()
}

func validateConfig(cfg config.Config) error {
	if cfg.TelegramBotToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN не задан")
	}
	if cfg.TelegramChannelID == "" {
		return fmt.Errorf("TELEGRAM_CHANNEL_ID не задан")
	}
	return nil
}
