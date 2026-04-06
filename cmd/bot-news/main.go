package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"

	"bot-news/internal/config"
	"bot-news/internal/feed"
	"bot-news/internal/logger"
	"bot-news/internal/notifier"
	"bot-news/internal/storage"
	"bot-news/internal/summarizer"
)

type app struct {
	cfg     config.Config
	db      *storage.DB
	fetcher *feed.Fetcher
	sum     summarizer.Summarizer
	notif   *notifier.Telegram
}

func main() {
	runNow := flag.Bool("run-digest-now", false, "немедленно отправить дайджест и выйти")
	flag.Parse()

	logger.Init("bot-news")

	config.LoadDotEnv()
	cfg := config.LoadFromEnv()

	if err := validateConfig(cfg); err != nil {
		slog.Error("конфигурация невалидна", "error", err)
		os.Exit(1)
	}

	a, err := newApp(cfg)
	if err != nil {
		slog.Error("инициализация приложения", "error", err)
		os.Exit(1)
	}
	defer a.db.Close()

	if *runNow {
		ctx := context.Background()
		a.fetch(ctx)
		a.digest(ctx)
		return
	}

	a.run()
}

func newApp(cfg config.Config) (*app, error) {
	db, err := storage.NewDB(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("база данных: %w", err)
	}

	var sum summarizer.Summarizer
	if cfg.GroqAPIKey != "" {
		slog.Info("используется Groq API", "model", cfg.GroqModel)
		sum = summarizer.NewGroq(cfg.GroqAPIKey, cfg.GroqModel)
	} else {
		slog.Info("GROQ_API_KEY не задан, используется простой дайджест")
		sum = summarizer.NewSimple()
	}

	notif, err := notifier.NewTelegram(cfg.TelegramBotToken, cfg.TelegramChannelID)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("telegram: %w", err)
	}

	return &app{
		cfg:     cfg,
		db:      db,
		fetcher: feed.NewFetcher(30 * time.Second),
		sum:     sum,
		notif:   notif,
	}, nil
}

func (a *app) run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	// Health check HTTP-сервер
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.runHealthServer(ctx)
	}()

	// Cron-планировщик
	c := cron.New()
	fetchSpec := fmt.Sprintf("@every %dm", a.cfg.FetchIntervalMin)

	if _, err := c.AddFunc(fetchSpec, func() { a.fetch(ctx) }); err != nil {
		slog.Error("ошибка регистрации cron fetch", "error", err)
		os.Exit(1)
	}
	if _, err := c.AddFunc(a.cfg.DigestCron, func() { a.digest(ctx) }); err != nil {
		slog.Error("ошибка регистрации cron digest", "error", err)
		os.Exit(1)
	}

	c.Start()
	slog.Info("bot-news запущен",
		"fetch", fetchSpec,
		"digest", a.cfg.DigestCron,
		"feeds", len(a.cfg.FeedURLs),
		"health", a.cfg.HealthAddr,
	)

	// Первый фетч сразу при старте
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.fetch(ctx)
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("получен сигнал завершения, ждём завершения задач...")
	cancel()
	<-c.Stop().Done()
	wg.Wait()
	slog.Info("бот остановлен")
}

func (a *app) runHealthServer(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	srv := &http.Server{
		Addr:         a.cfg.HealthAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	slog.Info("health check запущен", "addr", a.cfg.HealthAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("health server", "error", err)
	}
}

func (a *app) fetch(ctx context.Context) {
	articles, err := a.fetcher.FetchAll(ctx, a.cfg.FeedURLs)
	if err != nil {
		slog.Error("ошибка получения фидов", "error", err)
		return
	}
	if err := a.db.SaveArticles(ctx, articles); err != nil {
		slog.Error("ошибка сохранения статей", "error", err)
		return
	}
	slog.Info("статьи получены и сохранены", "count", len(articles))
}

func (a *app) digest(ctx context.Context) {
	since := time.Now().AddDate(0, 0, -1)

	articles, err := a.db.GetUnsent(ctx, since)
	if err != nil {
		slog.Error("ошибка чтения статей", "error", err)
		return
	}
	if len(articles) == 0 {
		slog.Info("нет новых статей для дайджеста, отправляем heartbeat")
		heartbeat := fmt.Sprintf("✅ Дайджест за %s: новых материалов нет. Система работает.",
			time.Now().Format("2 January 2006"))
		if err := a.notif.Send(ctx, heartbeat); err != nil {
			slog.Error("ошибка отправки heartbeat", "error", err)
		}
		return
	}
	slog.Info("формирую дайджест", "articles", len(articles))

	text, err := a.sum.Summarize(ctx, articles)
	if err != nil {
		slog.Error("ошибка саммаризации", "error", err)
		return
	}
	if err := a.notif.Send(ctx, text); err != nil {
		slog.Error("ошибка отправки в Telegram", "error", err)
		return
	}

	ids := make([]int64, len(articles))
	for i, ar := range articles {
		ids[i] = ar.ID
	}
	if err := a.db.MarkSent(ctx, ids); err != nil {
		slog.Error("ошибка пометки статей", "error", err)
	}
	slog.Info("дайджест отправлен", "articles", len(articles))
}

func validateConfig(cfg config.Config) error {
	if cfg.TelegramBotToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN не задан")
	}
	if cfg.TelegramChannelID == "" {
		return fmt.Errorf("TELEGRAM_CHANNEL_ID не задан")
	}
	if len(cfg.FeedURLs) == 0 {
		return fmt.Errorf("FEED_URLS не задан")
	}
	return nil
}
