package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"

	"bot-news/internal/config"
	"bot-news/internal/feed"
	"bot-news/internal/notifier"
	"bot-news/internal/storage"
	"bot-news/internal/summarizer"
)

const timeFmt = "02 Jan 15:04 MST"
const digestLookback = 12 * time.Hour

// Notifier — интерфейс для отправки уведомлений.
type Notifier interface {
	Send(ctx context.Context, text string) error
	SendToChat(ctx context.Context, chatID, text string) error
	SendToAdmin(ctx context.Context, adminID int64, text string) error
	GetChatTitle(chatID string) (string, error)
	ListenCommands(ctx context.Context, adminID int64,
		onFetch, onDigest func(),
		onStats, onLatest func() string,
		onDigestCount func() int,
		onFeeds func() ([]storage.Feed, error),
		onToggleFeed func(string) error,
	)
}

// Fetcher — интерфейс для получения статей из RSS.
type Fetcher interface {
	FetchAll(ctx context.Context, urls []string) ([]feed.FetchResult, error)
}

// App — основной оркестратор приложения.
type App struct {
	cfg     config.Config
	db      *storage.DB
	fetcher Fetcher
	sum     summarizer.Summarizer
	notif   Notifier
	cron    *cron.Cron
	mu      sync.Mutex
}

func NewApp(cfg config.Config) (*App, error) {
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

	a := &App{
		cfg:     cfg,
		db:      db,
		fetcher: feed.NewFetcher(30 * time.Second),
		sum:     sum,
		notif:   notif,
	}

	// Загружаем сохраненные лимиты AI
	if limits, err := db.GetState(context.Background(), "ai_limits"); err == nil && limits != "" {
		a.sum.SetLimits(limits)
	}

	return a, nil
}

// NewAppWithDeps используется в тестах для инъекции зависимостей.
func NewAppWithDeps(cfg config.Config, db *storage.DB, fetcher Fetcher, sum summarizer.Summarizer,
	notif Notifier) *App {
	return &App{
		cfg:     cfg,
		db:      db,
		fetcher: fetcher,
		sum:     sum,
		notif:   notif,
	}
}

func (a *App) Close() error {
	return a.db.Close()
}

func (a *App) Run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Синхронизация конфига из ENV в БД при первом запуске
	if err := a.syncConfigToDB(ctx); err != nil {
		slog.Error("ошибка синхронизации конфига в БД", "error", err)
	}

	var wg sync.WaitGroup

	// Health check & Admin HTTP-сервер
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.runAdminServer(ctx)
	}()

	// Telegram-команды
	if a.cfg.TelegramAdminID != 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.notif.ListenCommands(ctx, a.cfg.TelegramAdminID,
				func() { a.FetchAll(ctx) },
				func() {
					channels, _ := a.db.GetChannels(ctx)
					for _, ch := range channels {
						if ch.Active {
							a.Digest(ctx, ch)
						}
					}
				},
				func() string {
					channels, _ := a.db.GetChannels(ctx)
					if len(channels) > 0 {
						return a.StatsText(ctx, channels[0].ID)
					}
					return "каналы не найдены"
				},
				func() string {
					channels, _ := a.db.GetChannels(ctx)
					if len(channels) > 0 {
						return a.LatestText(ctx, channels[0].ID)
					}
					return "каналы не найдены"
				},
				func() int {
					channels, _ := a.db.GetChannels(ctx)
					if len(channels) > 0 {
						n, _ := a.db.GetUnsentCount(ctx, channels[0].ID, a.digestSince())
						return n
					}
					return 0
				},
				func() ([]storage.Feed, error) {
					channels, _ := a.db.GetChannels(ctx)
					if len(channels) > 0 {
						return a.db.GetFeeds(ctx, channels[0].ID)
					}
					return nil, nil
				},
				func(url string) error {
					channels, _ := a.db.GetChannels(ctx)
					for _, ch := range channels {
						feeds, _ := a.db.GetFeeds(ctx, ch.ID)
						for _, f := range feeds {
							if f.URL == url {
								f.Active = !f.Active
								return a.db.UpsertFeed(ctx, f)
							}
						}
					}
					return fmt.Errorf("feed not found")
				},
			)
		}()
	}

	a.setupCron(ctx)

	// Первый фетч сразу при старте
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.FetchAll(ctx)
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("получен сигнал завершения, ждём завершения задач...")
	cancel()
	wg.Wait()
	slog.Info("бот остановлен")
}

func (a *App) RunNow() {
	ctx := context.Background()
	a.FetchAll(ctx)
	channels, _ := a.db.GetChannels(ctx)
	for _, ch := range channels {
		if ch.Active {
			a.Digest(ctx, ch)
		}
	}
}

func (a *App) syncConfigToDB(ctx context.Context) error {
	channels, err := a.db.GetChannels(ctx)
	if err != nil {
		return err
	}

	if len(channels) == 0 {
		slog.Info("каналы не найдены в БД, создаем дефолтный из ENV")
		chID, err := a.db.UpsertChannel(ctx, storage.Channel{
			Name:           "Default",
			TelegramChatID: a.cfg.TelegramChannelID,
			DigestCron:     a.cfg.DigestCron,
			Timezone:       a.cfg.Timezone,
			Active:         true,
		})
		if err != nil {
			return err
		}

		for _, url := range a.cfg.FeedURLs {
			if err := a.db.UpsertFeed(ctx, storage.Feed{
				ChannelID: chID,
				URL:       url,
				Active:    true,
			}); err != nil {
				slog.Error("ошибка добавления фида", "url", url, "error", err)
			}
		}
	}
	return nil
}

func (a *App) setupCron(ctx context.Context) {
	a.mu.Lock()
	if a.cron != nil {
		a.cron.Stop()
	}
	a.cron = cron.New()
	c := a.cron
	a.mu.Unlock()

	fetchSpec := fmt.Sprintf("@every %dm", a.cfg.FetchIntervalMin)
	if _, err := c.AddFunc(fetchSpec, func() { a.FetchAll(ctx) }); err != nil {
		slog.Error("ошибка регистрации cron fetch", "error", err)
	}

	channels, _ := a.db.GetChannels(ctx)
	for _, ch := range channels {
		if !ch.Active {
			continue
		}
		chCopy := ch
		if _, err := c.AddFunc(ch.DigestCron, func() { a.Digest(ctx, chCopy) }); err != nil {
			slog.Error("ошибка регистрации cron digest", "channel", ch.Name, "error", err)
		}
	}

	if _, err := c.AddFunc("0 3 * * *", func() { a.cleanup(ctx) }); err != nil {
		slog.Error("ошибка регистрации cron cleanup", "error", err)
	}
	c.Start()
}

func (a *App) FetchAll(ctx context.Context) {
	channels, err := a.db.GetChannels(ctx)
	if err != nil {
		slog.Error("ошибка получения каналов для fetch", "error", err)
		return
	}

	for _, ch := range channels {
		if !ch.Active {
			continue
		}
		feeds, err := a.db.GetFeeds(ctx, ch.ID)
		if err != nil {
			slog.Error("ошибка получения фидов", "channel", ch.Name, "error", err)
			continue
		}

		var urls []string
		for _, f := range feeds {
			if f.Active {
				urls = append(urls, f.URL)
			}
		}

		if len(urls) == 0 {
			continue
		}

		results, _ := a.fetcher.FetchAll(ctx, urls)

		var articles []storage.Article
		for _, res := range results {
			if res.Err != nil {
				// В будущем можно добавить UpdateFeedStatus по ID
				continue
			}
			for i := range res.Articles {
				res.Articles[i].ChannelID = ch.ID
			}
			articles = append(articles, res.Articles...)
		}

		if err := a.db.SaveArticles(ctx, articles); err != nil {
			slog.Error("ошибка сохранения статей", "channel", ch.Name, "error", err)
			continue
		}
		slog.Info("статьи получены и сохранены", "channel", ch.Name, "count", len(articles))
	}
}

func (a *App) Digest(ctx context.Context, ch storage.Channel) {
	since := a.digestSince()

	articles, err := a.db.GetUnsent(ctx, ch.ID, since)
	if err != nil {
		slog.Error("ошибка чтения статей", "channel", ch.Name, "error", err)
		return
	}
	if len(articles) == 0 {
		slog.Info("нет новых статей для дайджеста, отправляем heartbeat", "channel", ch.Name)
		heartbeat := fmt.Sprintf("✅ Дайджест [%s] за %s: новых материалов нет. Система работает.",
			ch.Name, time.Now().Format("2 January 2006"))
		heartbeat += a.statsFooter(ctx, ch.ID, ch.Timezone, 0)

		if errSend := a.notif.SendToChat(ctx, ch.TelegramChatID, heartbeat); errSend != nil {
			slog.Error("ошибка отправки heartbeat", "channel", ch.Name, "error", errSend)
		}
		return
	}
	slog.Info("формирую дайджест", "channel", ch.Name, "articles", len(articles))

	text, err := a.sum.Summarize(ctx, articles)
	if err != nil {
		slog.Warn("ошибка саммаризации, использую простой дайджест", "error", err)
		text, err = summarizer.NewSimple().Summarize(ctx, articles)
		if err != nil {
			slog.Error("ошибка fallback саммаризации", "error", err)
			return
		}
	}
	text += a.statsFooter(ctx, ch.ID, ch.Timezone, len(articles))

	if err := a.db.SaveDigest(ctx, ch.ID, text); err != nil {
		slog.Error("ошибка сохранения дайджеста в базу", "channel", ch.Name, "error", err)
	}

	_ = a.db.SetState(ctx, "ai_limits", a.sum.GetLimits())

	if err := a.notif.SendToChat(ctx, ch.TelegramChatID, text); err != nil {
		slog.Error("ошибка отправки в Telegram", "channel", ch.Name, "error", err)
		return
	}

	ids := make([]int64, len(articles))
	for i, ar := range articles {
		ids[i] = ar.ID
	}
	if err := a.db.MarkSent(ctx, ids); err != nil {
		slog.Error("ошибка пометки статей", "channel", ch.Name, "error", err)
	}
	slog.Info("дайджест отправлен", "channel", ch.Name, "articles", len(articles))
}

func (a *App) StatsText(ctx context.Context, channelID int64) string {
	stats, err := a.db.GetStats(ctx, channelID)
	if err != nil {
		return "ошибка получения статистики"
	}

	channels, _ := a.db.GetChannels(ctx)
	var ch storage.Channel
	for _, c := range channels {
		if c.ID == channelID {
			ch = c
			break
		}
	}

	lastFetch := "нет данных"
	if !stats.LastFetchedAt.IsZero() {
		lastFetch = stats.LastFetchedAt.In(a.loc(ch.Timezone)).Format(timeFmt)
	}

	res := fmt.Sprintf(
		"📊 <b>Статистика [%s]</b>\n\n"+
			"📥 Всего собрано: %d\n"+
			"✅ Отправлено: %d\n"+
			"📬 Не отправлено: %d\n"+
			"🕐 Последний сбор: %s",
		ch.Name,
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

func (a *App) LatestText(ctx context.Context, channelID int64) string {
	articles, err := a.db.GetLatestPerFeed(ctx, channelID, 3)
	if err != nil {
		return "ошибка получения последних материалов"
	}
	if len(articles) == 0 {
		return "материалов пока нет"
	}

	slog.Info("формирую AI-обзор последних новостей по запросу", "channelID", channelID, "count", len(articles))

	text, err := a.sum.Summarize(ctx, articles)
	if err != nil {
		slog.Warn("ошибка AI для latest, откат к простому списку", "error", err)
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

	_ = a.db.SetState(ctx, "ai_limits", a.sum.GetLimits())

	return "🆕 <b>Свежий обзор последних новостей (по 3 с каждого канала):</b>\n\n" + text
}

func (a *App) loc(timezone string) *time.Location {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (a *App) digestSince() time.Time {
	return time.Now().Add(-digestLookback)
}

func (a *App) statsFooter(ctx context.Context, channelID int64, timezone string, digestCount int) string {
	stats, err := a.db.GetStats(ctx, channelID)
	if err != nil {
		return ""
	}
	lastFetch := "нет данных"
	if !stats.LastFetchedAt.IsZero() {
		lastFetch = stats.LastFetchedAt.In(a.loc(timezone)).Format(timeFmt)
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

func (a *App) cleanup(ctx context.Context) {
	n, err := a.db.DeleteOldArticles(ctx, 30)
	if err != nil {
		slog.Error("ошибка очистки старых статей", "error", err)
		return
	}
	if n > 0 {
		slog.Info("очистка завершена", "deleted_count", n)
		msg := fmt.Sprintf("🧹 <b>Техническое обслуживание</b>\n\nБаза данных очищена. Удалено старых статей: %d", n)
		_ = a.notif.SendToAdmin(ctx, a.cfg.TelegramAdminID, msg)
	}
}

func (a *App) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.TLSEnabled {
			if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				http.Error(w, "missing client certificate", http.StatusForbidden)
				return
			}
			next(w, r)
			return
		}

		if a.cfg.TriggerSecret != "" {
			token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token != a.cfg.TriggerSecret {
				if r.URL.Query().Get("token") != a.cfg.TriggerSecret {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			}
		}
		next(w, r)
	}
}

func (a *App) redirect(w http.ResponseWriter, r *http.Request) {
	target := "/"
	if !a.cfg.TLSEnabled {
		if token := r.URL.Query().Get("token"); token != "" {
			target += "?token=" + token
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *App) runAdminServer(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	mux.HandleFunc("GET /{$}", a.AuthMiddleware(a.HandleDashboard(ctx)))
	a.RegisterTriggers(ctx, mux)

	srv := &http.Server{
		Addr:         a.cfg.HealthAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	if a.cfg.TLSEnabled {
		caCert, err := os.ReadFile(a.cfg.CACert)
		if err != nil {
			slog.Error("не удалось прочитать CA сертификат", "path", a.cfg.CACert, "error", err)
			os.Exit(1)
		}
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)

		srv.TLSConfig = &tls.Config{
			ClientAuth: tls.VerifyClientCertIfGiven,
			ClientCAs:  caCertPool,
			MinVersion: tls.VersionTLS12,
		}
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	if a.cfg.TLSEnabled {
		slog.Info("web dashboard запущен (HTTPS + mTLS)", "addr", a.cfg.HealthAddr)
		if err := srv.ListenAndServeTLS(a.cfg.ServerCert, a.cfg.ServerKey); err != nil && err != http.ErrServerClosed {
			slog.Error("TLS server", "error", err)
		}
	} else {
		slog.Info("web dashboard запущен (HTTP)", "addr", a.cfg.HealthAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("admin server", "error", err)
		}
	}
}

func (a *App) RegisterTriggers(ctx context.Context, mux *http.ServeMux) {
	mux.HandleFunc("POST /trigger/fetch-all", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		go a.FetchAll(ctx)
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/digest", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		channels, _ := a.db.GetChannels(ctx)
		for _, ch := range channels {
			if ch.ID == id {
				go a.Digest(ctx, ch)
				break
			}
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/add-channel", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		cronSpec := r.FormValue("cron")
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(cronSpec); err != nil {
			http.Error(w, "Невалидный формат Cron: "+err.Error(), http.StatusBadRequest)
			return
		}

		a.db.UpsertChannel(ctx, storage.Channel{
			Name:           r.FormValue("name"),
			TelegramChatID: r.FormValue("chat_id"),
			DigestCron:     cronSpec,
			Timezone:       r.FormValue("timezone"),
			Active:         true,
		})
		a.setupCron(ctx)
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/del-channel", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		_ = a.db.DeleteChannel(ctx, id)
		a.setupCron(ctx)
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/add-feed", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		chID, _ := strconv.ParseInt(r.FormValue("channel_id"), 10, 64)
		_ = a.db.UpsertFeed(ctx, storage.Feed{
			ChannelID: chID,
			URL:       r.FormValue("url"),
			Active:    true,
		})
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/del-feed", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		_ = a.db.DeleteFeed(ctx, id)
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/update-feed-title", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		title := strings.TrimSpace(r.FormValue("title"))
		_ = a.db.UpdateFeedTitleByID(ctx, id, title)
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/update-channel-cron", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		cronSpec := strings.TrimSpace(r.FormValue("cron"))
		if cronSpec != "" {
			// Валидация cron-выражения
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			if _, err := parser.Parse(cronSpec); err != nil {
				slog.Error("невалидный cron", "spec", cronSpec, "error", err)
				http.Error(w, "Невалидный формат Cron: "+err.Error(), http.StatusBadRequest)
				return
			}

			if err := a.db.UpdateChannelCron(ctx, id, cronSpec); err != nil {
				slog.Error("ошибка обновления cron в БД", "id", id, "error", err)
			}
			a.setupCron(ctx)
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/latest-bot", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		channels, _ := a.db.GetChannels(ctx)
		if len(channels) > 0 {
			go func() {
				text := a.LatestText(ctx, channels[0].ID)
				_ = a.notif.SendToAdmin(ctx, a.cfg.TelegramAdminID, text)
			}()
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/digest-bot", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		channels, _ := a.db.GetChannels(ctx)
		if len(channels) > 0 {
			go func() {
				since := a.digestSince()
				articles, _ := a.db.GetUnsent(ctx, channels[0].ID, since)
				if len(articles) > 0 {
					text, _ := a.sum.Summarize(ctx, articles)
					text += a.statsFooter(ctx, channels[0].ID, channels[0].Timezone, len(articles))
					_ = a.notif.SendToAdmin(ctx, a.cfg.TelegramAdminID, "👤 <b>Персональный предпросмотр дайджеста:</b>\n\n"+text)
					// Обновляем лимиты в базе после саммаризации
					_ = a.db.SetState(ctx, "ai_limits", a.sum.GetLimits())
				} else {
					_ = a.notif.SendToAdmin(ctx, a.cfg.TelegramAdminID, "📭 Новых статей для дайджеста нет.")
				}
			}()
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/update-channel-name", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		chatID := r.FormValue("chat_id")
		if title, err := a.notif.GetChatTitle(chatID); err == nil && title != "" {
			_ = a.db.UpdateChannelName(ctx, id, title)
		}
		a.redirect(w, r)
	}))
}

func (a *App) HandleDashboard(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channels, _ := a.db.GetChannels(ctx)
		token := r.URL.Query().Get("token")

		type channelData struct {
			storage.Channel
			Feeds   []storage.Feed
			Stats   storage.Stats
			Unsent  int
			NextRun string
		}

		var data struct {
			Channels   []channelData
			AILimits   template.HTML
			Version    string
			Token      string
			TLSEnabled bool
		}

		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

		for _, ch := range channels {
			feeds, _ := a.db.GetFeeds(ctx, ch.ID)
			stats, _ := a.db.GetStats(ctx, ch.ID)
			unsent, _ := a.db.GetUnsentCount(ctx, ch.ID, a.digestSince())

			nextRun := "—"
			if ch.Active {
				if sched, err := parser.Parse(ch.DigestCron); err == nil {
					nextRun = sched.Next(time.Now()).Format("02 Jan 15:04")
				}
			}

			data.Channels = append(data.Channels, channelData{
				Channel: ch,
				Feeds:   feeds,
				Stats:   stats,
				Unsent:  unsent,
				NextRun: nextRun,
			})
		}

		data.AILimits = template.HTML(a.sum.GetLimits())
		data.Version = "1.2.0"
		data.Token = token
		data.TLSEnabled = a.cfg.TLSEnabled

		if data.AILimits == "" {
			data.AILimits = "🤖 <i>Статистика AI будет доступна после первого запроса</i>"
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = dashboardTmpl.Execute(w, data)
	}
}

var dashboardTmpl = template.Must(template.New("dashboard").Parse(`
<!DOCTYPE html>
<html lang="ru" class="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>bot-news Dashboard</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <style>
        body { background-color: #0f172a; color: #f8fafc; }
    </style>
</head>
<body class="p-4 md:p-8 font-sans">
    <div class="max-w-6xl mx-auto">
        <header class="flex flex-col md:flex-row justify-between items-start md:items-center mb-8 gap-4">
            <h1 class="text-3xl font-bold text-sky-400 flex items-center gap-3">
                bot-news
                <span class="text-xs font-normal bg-slate-800 text-slate-400 px-2 py-1 rounded border border-slate-700">
                    v{{.Version}}
                </span>
            </h1>
            <div class="flex gap-2">
                <form action="/trigger/latest-bot{{if not .TLSEnabled}}?token={{.Token}}{{end}}" method="POST">
                    <button class="bg-indigo-600 hover:bg-indigo-500 px-4 py-2 rounded text-sm font-medium transition" title="Отправить последние новости в бота">
                        🤖 Latest
                    </button>
                </form>
                <form action="/trigger/digest-bot{{if not .TLSEnabled}}?token={{.Token}}{{end}}" method="POST">
                    <button class="bg-violet-600 hover:bg-violet-500 px-4 py-2 rounded text-sm font-medium transition" title="Отправить черновик дайджеста в бота">
                        👤 Digest
                    </button>
                </form>
                <form action="/trigger/fetch-all{{if not .TLSEnabled}}?token={{.Token}}{{end}}" method="POST">
                    <button class="bg-sky-600 hover:bg-sky-500 px-4 py-2 rounded text-sm font-medium transition">
                        🔄 Собрать все
                    </button>
                </form>
                <button onclick="document.getElementById('add-channel-modal').classList.remove('hidden')" 
                    class="bg-emerald-600 hover:bg-emerald-500 px-4 py-2 rounded text-sm font-medium transition">
                    + Добавить канал
                </button>
            </div>
        </header>

        {{if .AILimits}}
        <div class="mb-8 px-4 py-2 bg-indigo-500/10 border border-indigo-500/20 rounded-lg text-xs text-indigo-300">
            {{.AILimits}}
        </div>
        {{end}}

        <div class="grid grid-cols-1 gap-8">
            {{range .Channels}}
            <div class="bg-slate-800/30 rounded-xl border border-slate-700 overflow-hidden shadow-xl">
                <div class="px-6 py-4 border-b border-slate-700 bg-slate-800/50 flex justify-between items-center">
                    <div class="flex items-center gap-3">
                        <div>
                            <div class="flex items-center gap-2">
                                <h2 class="text-xl font-bold text-sky-400">{{.Name}}</h2>
                                <form action="/trigger/update-channel-name{{if not $.TLSEnabled}}?token={{$.Token}}{{end}}" method="POST" class="inline">
                                    <input type="hidden" name="id" value="{{.ID}}">
                                    <input type="hidden" name="chat_id" value="{{.TelegramChatID}}">
                                    <button type="submit" class="text-slate-500 hover:text-sky-400 transition-colors" title="Обновить название из Telegram">
                                        <svg xmlns="http://www.w3.org/2000/svg" class="h-4 w-4" viewBox="0 0 20 20" fill="currentColor">
                                            <path fill-rule="evenodd" d="M4 2a1 1 0 011 1v2.101a7.002 7.002 0 0111.601 2.566 1 1 0 11-1.885.666A5.002 5.002 0 005.999 7H9a1 1 0 010 2H4a1 1 0 01-1-1V3a1 1 0 011-1zm.008 9.057a1 1 0 011.276.61A5.002 5.002 0 0014.001 13H11a1 1 0 110-2h5a1 1 0 011 1v5a1 1 0 11-2 0v-2.101a7.002 7.002 0 01-11.601-2.566 1 1 0 01.61-1.276z" clip-rule="evenodd" />
                                        </svg>
                                    </button>
                                </form>
                            </div>
                            <div class="text-xs text-slate-500 flex items-center gap-2">
                                <span>Chat: {{.TelegramChatID}}</span>
                                <span class="text-slate-700">|</span>
                                <form action="/trigger/update-channel-cron{{if not $.TLSEnabled}}?token={{$.Token}}{{end}}" method="POST" class="flex items-center gap-1">
                                    <input type="hidden" name="id" value="{{.ID}}">
                                    <span class="text-slate-500">Cron:</span>
                                    <input type="text" name="cron" value="{{.DigestCron}}" 
                                        class="bg-transparent border-b border-dotted border-slate-700 hover:border-sky-500 
                                        focus:border-sky-500 focus:outline-none transition-colors 
                                        text-slate-400 py-0 w-32"
                                        title="Формат: минута час день месяц день_недели (напр. 0 9,21 * * *)"
                                        onchange="this.form.submit()">
                                </form>
                                <span class="text-slate-700">|</span>
                                <span class="text-emerald-400 font-medium" title="Следующая отправка дайджеста">Next: {{.NextRun}}</span>
                            </div>
                        </div>
                    </div>
                    <div class="flex gap-2">
                        <form action="/trigger/digest{{if not $.TLSEnabled}}?token={{$.Token}}{{end}}" method="POST">
                            <input type="hidden" name="id" value="{{.ID}}">
                            <button class="text-xs bg-violet-600 hover:bg-violet-500 px-3 py-1 rounded transition">Digest Now</button>
                        </form>
                        <form action="/trigger/del-channel{{if not $.TLSEnabled}}?token={{$.Token}}{{end}}" method="POST" onsubmit="return confirm('Удалить канал?')">
                            <input type="hidden" name="id" value="{{.ID}}">
                            <button class="text-xs text-rose-400 hover:text-rose-300">Удалить</button>
                        </form>
                    </div>
                </div>
                
                <div class="p-6 grid grid-cols-2 md:grid-cols-4 gap-4 bg-slate-900/20">
                    <div class="text-center">
                        <div class="text-slate-500 text-[10px] uppercase">Собрано</div>
                        <div class="text-lg font-bold">{{.Stats.TotalArticles}}</div>
                    </div>
                    <div class="text-center">
                        <div class="text-slate-500 text-[10px] uppercase">Отправлено</div>
                        <div class="text-lg font-bold text-emerald-400">{{.Stats.SentArticles}}</div>
                    </div>
                    <div class="text-center">
                        <div class="text-slate-500 text-[10px] uppercase">В очереди</div>
                        <div class="text-lg font-bold text-amber-400">{{.Unsent}}</div>
                    </div>
                    <div class="text-center">
                        <div class="text-slate-500 text-[10px] uppercase">Последний сбор</div>
                        <div class="text-sm font-medium pt-1">{{if .Stats.LastFetchedAt.IsZero}}никогда{{else}}{{.Stats.LastFetchedAt.Format "02 Jan 15:04"}}{{end}}</div>
                    </div>
                </div>

                <div class="px-6 py-4">
                    <h3 class="text-sm font-semibold mb-3 text-slate-400">📡 Источники RSS ({{len .Feeds}})</h3>
                    <div class="overflow-x-auto">
                        <table class="w-full text-left text-xs">
                            <tbody class="divide-y divide-slate-700/50">
                                {{range .Feeds}}
                                <tr class="group">
                                    <td class="py-2 w-16">
                                        <span class="px-2 py-0.5 rounded-full {{if .Active}}bg-emerald-500/10 text-emerald-400{{else}}bg-slate-500/10 text-slate-400{{end}}">
                                            {{if .Active}}OK{{else}}Off{{end}}
                                        </span>
                                    </td>
                                    <td class="py-2">
                                        <form action="/trigger/update-feed-title{{if not $.TLSEnabled}}?token={{$.Token}}{{end}}" method="POST" class="flex items-center">
                                            <input type="hidden" name="id" value="{{.ID}}">
                                            <input type="text" name="title" 
                                                value="{{if .Title}}{{.Title}}{{else}}{{.URL}}{{end}}" 
                                                class="bg-transparent border-b border-transparent hover:border-slate-600 
                                                focus:border-sky-500 focus:outline-none transition-colors font-medium 
                                                text-slate-200 group-hover:text-sky-400 py-0 w-full"
                                                onchange="this.form.submit()">
                                        </form>
                                        <div class="text-[10px] text-slate-500">{{.URL}}</div>
                                    </td>
                                    <td class="py-2 text-right">
                                        <form action="/trigger/del-feed{{if not $.TLSEnabled}}?token={{$.Token}}{{end}}" method="POST" class="inline">
                                            <input type="hidden" name="id" value="{{.ID}}">
                                            <button class="text-rose-500 hover:text-rose-400 opacity-0 group-hover:opacity-100 transition-opacity">×</button>
                                        </form>
                                    </td>
                                </tr>
                                {{end}}
                            </tbody>
                        </table>
                    </div>
                    <form action="/trigger/add-feed{{if not $.TLSEnabled}}?token={{$.Token}}{{end}}" method="POST" class="mt-4 flex gap-2">
                        <input type="hidden" name="channel_id" value="{{.ID}}">
                        <input type="url" name="url" placeholder="Новый RSS URL" required class="flex-1 bg-slate-900/50 border border-slate-700 rounded px-3 py-1 text-xs">
                        <button class="bg-sky-600 px-4 py-1 rounded text-xs">Добавить</button>
                    </form>
                </div>
            </div>
            {{end}}
        </div>

        <div id="add-channel-modal" class="hidden fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
            <div class="bg-slate-800 rounded-xl border border-slate-700 p-6 w-full max-w-md shadow-2xl">
                <h2 class="text-xl font-bold mb-4 text-sky-400">Новый канал</h2>
                <form action="/trigger/add-channel{{if not .TLSEnabled}}?token={{.Token}}{{end}}" method="POST" class="flex flex-col gap-4">
                    <input name="name" placeholder="Название (напр. Технологии)" required class="bg-slate-900 border border-slate-700 rounded p-2 focus:ring-1 focus:ring-sky-500 outline-none">
                    <input name="chat_id" placeholder="Telegram Chat ID (@channel или -100...)" required class="bg-slate-900 border border-slate-700 rounded p-2 focus:ring-1 focus:ring-sky-500 outline-none">
                    <input name="cron" value="0 9,21 * * *" placeholder="Cron (напр. 0 9 * * *)" required class="bg-slate-900 border border-slate-700 rounded p-2 focus:ring-1 focus:ring-sky-500 outline-none">
                    <input name="timezone" value="Europe/Moscow" required class="bg-slate-900 border border-slate-700 rounded p-2 focus:ring-1 focus:ring-sky-500 outline-none">
                    <div class="flex justify-end gap-2 mt-4">
                        <button type="button" onclick="document.getElementById('add-channel-modal').classList.add('hidden')" class="px-4 py-2 text-slate-400 hover:text-slate-200">Отмена</button>
                        <button class="bg-emerald-600 hover:bg-emerald-500 px-6 py-2 rounded font-medium transition">Создать</button>
                    </div>
                </form>
            </div>
        </div>

        <footer class="mt-12 text-center text-slate-600 text-xs border-t border-slate-800 pt-8">
            &copy; 2026 bot-news · Status: OK
        </footer>
    </div>
</body>
</html>
`))

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
