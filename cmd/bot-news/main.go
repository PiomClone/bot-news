package main

import (
	"context"
	"flag"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
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

const timeFmt = "02 Jan 15:04 MST"
const digestLookback = 12 * time.Hour

type app struct {
	cfg     config.Config
	db      *storage.DB
	fetcher *feed.Fetcher
	sum     summarizer.Summarizer
	notif   *notifier.Telegram
}

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

	// Синхронизация фидов из конфига в базу
	if err := a.db.SyncFeeds(ctx, a.cfg.FeedURLs); err != nil {
		slog.Error("ошибка синхронизации фидов", "error", err)
	}

	var wg sync.WaitGroup

	// Health check HTTP-сервер
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.runHealthServer(ctx)
	}()

	// Telegram-команды
	if a.cfg.TelegramAdminID != 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.notif.ListenCommands(ctx, a.cfg.TelegramAdminID,
				func() { a.fetch(ctx) },
				func() { a.digest(ctx) },
				func() string { return a.statsText(ctx) },
				func() string { return a.latestText(ctx) },
				func() int {
					n, _ := a.db.GetUnsentCount(ctx, a.digestSince())
					return n
				},
				func() ([]storage.Feed, error) { return a.db.GetAllFeeds(ctx) },
				func(url string) error { return a.db.ToggleFeed(ctx, url) },
			)
		}()
	}

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
	if _, err := c.AddFunc("0 3 * * *", func() { a.cleanup(ctx) }); err != nil {
		slog.Error("ошибка регистрации cron cleanup", "error", err)
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

func (a *app) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.TriggerSecret != "" {
			token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token != a.cfg.TriggerSecret {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (a *app) runHealthServer(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/trigger/fetch", a.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		go a.fetch(ctx)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, "fetch triggered")
	}))
	mux.HandleFunc("/trigger/digest", a.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		go a.digest(ctx)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, "digest triggered")
	}))

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
	urls, err := a.db.GetActiveFeedURLs(ctx)
	if err != nil {
		slog.Error("ошибка получения списка фидов из базы", "error", err)
		return
	}
	if len(urls) == 0 {
		slog.Warn("нет активных фидов для сбора")
		return
	}

	articles, err := a.fetcher.FetchAll(ctx, urls)
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

func (a *app) loc() *time.Location {
	loc, err := time.LoadLocation(a.cfg.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (a *app) digestSince() time.Time {
	return time.Now().Add(-digestLookback)
}

func (a *app) statsFooter(ctx context.Context, digestCount int) string {
	stats, err := a.db.GetStats(ctx)
	if err != nil {
		return ""
	}
	lastFetch := "нет данных"
	if !stats.LastFetchedAt.IsZero() {
		lastFetch = stats.LastFetchedAt.In(a.loc()).Format(timeFmt)
	}
	if digestCount > 0 {
		return fmt.Sprintf(
			"\n\n—\n📥 В дайджесте: %d | Всего собрано: %d | Отправлено: %d\n🕐 Последний сбор: %s",
			digestCount, stats.TotalArticles, stats.SentArticles, lastFetch,
		)
	}
	return fmt.Sprintf(
		"\n\n—\n📥 Всего собрано: %d | Отправлено: %d\n🕐 Последний сбор: %s",
		stats.TotalArticles, stats.SentArticles, lastFetch,
	)
}

func (a *app) statsText(ctx context.Context) string {
	stats, err := a.db.GetStats(ctx)
	if err != nil {
		return "ошибка получения статистики"
	}
	lastFetch := "нет данных"
	if !stats.LastFetchedAt.IsZero() {
		lastFetch = stats.LastFetchedAt.In(a.loc()).Format(timeFmt)
	}

	res := fmt.Sprintf(
		"📊 <b>Статистика</b>\n\n"+
			"📥 Всего собрано: %d\n"+
			"✅ Отправлено: %d\n"+
			"📬 Не отправлено: %d\n"+
			"🕐 Последний сбор: %s",
		stats.TotalArticles,
		stats.SentArticles,
		stats.TotalArticles-stats.SentArticles,
		lastFetch,
	)

	if aiLimits := a.sum.GetLimits(); aiLimits != "" {
		res += "\n\n" + aiLimits
	}

	return res
}

func (a *app) latestText(ctx context.Context) string {
	articles, err := a.db.GetLatestPerFeed(ctx, 3)
	if err != nil {
		return "ошибка получения последних материалов"
	}
	if len(articles) == 0 {
		return "материалов пока нет"
	}

	slog.Info("формирую AI-обзор последних новостей по запросу", "count", len(articles))

	text, err := a.sum.Summarize(ctx, articles)
	if err != nil {
		slog.Warn("ошибка AI для latest, откат к простому списку", "error", err)
		// Если AI подвел, выводим просто список
		var sb strings.Builder
		sb.WriteString("📰 <b>Последние материалы (AI временно недоступен)</b>\n")
		var currentFeed string
		for _, article := range articles {
			if article.FeedURL != currentFeed {
				currentFeed = article.FeedURL
				source := article.FeedTitle
				if source == "" {
					source = sourceLabel(article.FeedURL)
				}
				emoji := summarizer.GetEmoji(article.FeedURL)
				fmt.Fprintf(&sb, "\n%s <b>%s</b>\n", emoji, html.EscapeString(source))
			}
			fmt.Fprintf(&sb, "- %s\n  %s\n", html.EscapeString(article.Title), html.EscapeString(article.Link))
		}
		return sb.String()
	}

	return "🆕 <b>Свежий обзор последних новостей (по 3 с каждого канала):</b>\n\n" + text
}

func sourceLabel(feedURL string) string {
	if feedURL == "" {
		return "@unknown"
	}
	parts := strings.Split(strings.Trim(feedURL, "/"), "/")
	name := parts[len(parts)-1]
	if name == "" {
		return feedURL
	}
	return "@" + name
}

func (a *app) digest(ctx context.Context) {
	since := a.digestSince()

	articles, err := a.db.GetUnsent(ctx, since)
	if err != nil {
		slog.Error("ошибка чтения статей", "error", err)
		return
	}
	if len(articles) == 0 {
		slog.Info("нет новых статей для дайджеста, отправляем heartbeat")
		heartbeat := fmt.Sprintf("✅ Дайджест за %s: новых материалов нет. Система работает.",
			time.Now().Format("2 January 2006"))
		heartbeat += a.statsFooter(ctx, 0)
		if errSend := a.notif.Send(ctx, heartbeat); errSend != nil {
			slog.Error("ошибка отправки heartbeat", "error", errSend)
		}
		return
	}
	slog.Info("формирую дайджест", "articles", len(articles))

	text, err := a.sum.Summarize(ctx, articles)
	if err != nil {
		slog.Warn("ошибка саммаризации, использую простой дайджест", "error", err)
		text, err = summarizer.NewSimple().Summarize(ctx, articles)
		if err != nil {
			slog.Error("ошибка fallback саммаризации", "error", err)
			return
		}
	}
	text += a.statsFooter(ctx, len(articles))

	if err := a.db.SaveDigest(ctx, text); err != nil {
		slog.Error("ошибка сохранения дайджеста в базу", "error", err)
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

func (a *app) cleanup(ctx context.Context) {
	n, err := a.db.DeleteOldArticles(ctx, 30)
	if err != nil {
		slog.Error("ошибка очистки старых статей", "error", err)
		return
	}
	if n > 0 {
		slog.Info("очистка завершена", "deleted_count", n)
		msg := fmt.Sprintf("🧹 <b>Техническое обслуживание</b>\n\nБаза данных очищена. Удалено старых статей: %d", n)
		if err := a.notif.SendToAdmin(ctx, a.cfg.TelegramAdminID, msg); err != nil {
			slog.Error("ошибка отправки уведомления об очистке", "error", err)
		}
	}
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
