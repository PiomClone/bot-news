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
					var sb strings.Builder
					for _, ch := range channels {
						if ch.Active {
							if sb.Len() > 0 {
								sb.WriteString("\n\n")
							}
							sb.WriteString(a.StatsText(ctx, ch.ID))
						}
					}
					if sb.Len() == 0 {
						return "активные каналы не найдены"
					}
					return sb.String()
				},
				func() string {
					channels, _ := a.db.GetChannels(ctx)
					var sb strings.Builder
					for _, ch := range channels {
						if ch.Active {
							if sb.Len() > 0 {
								sb.WriteString("\n\n")
							}
							sb.WriteString(fmt.Sprintf("📝 <b>Канал: %s</b>\n", ch.Name))
							sb.WriteString(a.LatestText(ctx, ch.ID))
						}
					}
					if sb.Len() == 0 {
						return "активные каналы не найдены"
					}
					return sb.String()
				},
				func() int {
					channels, _ := a.db.GetChannels(ctx)
					total := 0
					for _, ch := range channels {
						if ch.Active {
							n, _ := a.db.GetUnsentCount(ctx, ch.ID, a.digestSince())
							total += n
						}
					}
					return total
				},
				func() ([]storage.Feed, error) {
					channels, _ := a.db.GetChannels(ctx)
					var allFeeds []storage.Feed
					for _, ch := range channels {
						if ch.Active {
							feeds, _ := a.db.GetFeeds(ctx, ch.ID)
							allFeeds = append(allFeeds, feeds...)
						}
					}
					return allFeeds, nil
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
			ch.Name, time.Now().In(a.loc(ch.Timezone)).Format("2 January 2006"))
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
		slog.Error("ошибка GetLatestPerFeed", "channelID", channelID, "error", err)
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
	if timezone == "" {
		timezone = "Europe/Moscow"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc, err = time.LoadLocation("Europe/Moscow")
		if err != nil {
			return time.UTC
		}
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

func (a *App) getDashboardData(ctx context.Context, token string) interface{} {
	channels, _ := a.db.GetChannels(ctx)

	type channelData struct {
		storage.Channel
		Feeds       []storage.Feed
		Stats       storage.Stats
		Unsent      int
		NextRun     string
		Description string
		NextRuns    []string
		Error       string
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
		var nextRuns []string
		if ch.Active {
			if sched, err := parser.Parse(ch.DigestCron); err == nil {
				loc := a.loc(ch.Timezone)
				curr := time.Now().In(loc)
				nextRun = sched.Next(curr).Format("02 Jan 15:04")
				
				// Заполняем 5 ближайших для превью
				temp := curr
				for i := 0; i < 5; i++ {
					temp = sched.Next(temp)
					nextRuns = append(nextRuns, temp.Format("02 Jan 15:04"))
				}
			}
		}

		data.Channels = append(data.Channels, channelData{
			Channel:     ch,
			Feeds:       feeds,
			Stats:       stats,
			Unsent:      unsent,
			NextRun:     nextRun,
			Description: a.DescribeCron(ch.DigestCron),
			NextRuns:    nextRuns,
		})
	}

	data.AILimits = template.HTML(a.sum.GetLimits())
	data.Version = "1.3.0"
	data.Token = token
	data.TLSEnabled = a.cfg.TLSEnabled

	return data
}

func (a *App) getChannelData(ctx context.Context, channelID int64) interface{} {
	channels, _ := a.db.GetChannels(ctx)
	var ch storage.Channel
	for _, c := range channels {
		if c.ID == channelID {
			ch = c
			break
		}
	}

	feeds, _ := a.db.GetFeeds(ctx, ch.ID)
	stats, _ := a.db.GetStats(ctx, ch.ID)
	unsent, _ := a.db.GetUnsentCount(ctx, ch.ID, a.digestSince())

	nextRun := "—"
	var nextRuns []string
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if ch.Active {
		if sched, err := parser.Parse(ch.DigestCron); err == nil {
			loc := a.loc(ch.Timezone)
			curr := time.Now().In(loc)
			nextRun = sched.Next(curr).Format("02 Jan 15:04")
			
			temp := curr
			for i := 0; i < 5; i++ {
				temp = sched.Next(temp)
				nextRuns = append(nextRuns, temp.Format("02 Jan 15:04"))
			}
		}
	}

	return struct {
		storage.Channel
		Feeds       []storage.Feed
		Stats       storage.Stats
		Unsent      int
		NextRun     string
		Description string
		NextRuns    []string
		Error       string
	}{
		Channel:     ch,
		Feeds:       feeds,
		Stats:       stats,
		Unsent:      unsent,
		NextRun:     nextRun,
		Description: a.DescribeCron(ch.DigestCron),
		NextRuns:    nextRuns,
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

func (a *App) DescribeCron(spec string) string {
	parts := strings.Fields(spec)
	if len(parts) != 5 {
		return ""
	}

	minute, hour, dom, month, dow := parts[0], parts[1], parts[2], parts[3], parts[4]

	// Очень упрощенный парсер для частых случаев
	if dom == "*" && month == "*" && dow == "*" {
		if minute == "0" {
			if hour == "*" {
				return "Каждый час"
			}
			if !strings.Contains(hour, ",") && !strings.Contains(hour, "/") {
				return fmt.Sprintf("Ежедневно в %s:00", hour)
			}
			if strings.Contains(hour, ",") {
				return fmt.Sprintf("Ежедневно в %s:00", strings.ReplaceAll(hour, ",", " и "))
			}
		}
		if strings.HasPrefix(minute, "*/") {
			return fmt.Sprintf("Каждые %s мин.", strings.TrimPrefix(minute, "*/"))
		}
	}

	if dow != "*" && dom == "*" {
		days := map[string]string{
			"1-5": "по будням",
			"0,6": "по выходным",
			"1":   "по понедельникам",
			"5":   "по пятницам",
		}
		if d, ok := days[dow]; ok {
			return fmt.Sprintf("Ежедневно %s в %s:%s", d, hour, minute)
		}
	}

	return "" // Если слишком сложно, вернем пустоту
}

func (a *App) RegisterTriggers(ctx context.Context, mux *http.ServeMux) {
	mux.HandleFunc("GET /dashboard/cron-preview", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		cronSpec := strings.TrimSpace(r.URL.Query().Get("cron"))
		timezone := r.URL.Query().Get("timezone")
		if timezone == "" {
			timezone = "Europe/Moscow"
		}

		if cronSpec == "" {
			fmt.Fprint(w, `<span class="text-slate-500 text-[10px]">Введите крон-выражение</span>`)
			return
		}

		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		sched, err := parser.Parse(cronSpec)
		
		data := struct {
			Error       string
			Description string
			NextRuns    []string
		}{}

		if err != nil {
			data.Error = err.Error()
		} else {
			data.Description = a.DescribeCron(cronSpec)
			loc := a.loc(timezone)
			curr := time.Now().In(loc)
			for i := 0; i < 5; i++ {
				curr = sched.Next(curr)
				data.NextRuns = append(data.NextRuns, curr.Format("02 Jan 15:04"))
			}
		}

		_ = a.renderTemplate(w, "cron-preview", data)
	}))

	mux.HandleFunc("GET /dashboard/ai-limits", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		// Принудительно читаем из БД, если там свежее
		if limits, err := a.db.GetState(ctx, "ai_limits"); err == nil && limits != "" {
			a.sum.SetLimits(limits)
		}

		data := struct {
			AILimits template.HTML
		}{
			AILimits: template.HTML(a.sum.GetLimits()),
		}
		_ = a.renderTemplate(w, "ai-limits", data)
	}))

	mux.HandleFunc("POST /trigger/fetch-all", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		a.FetchAll(ctx)
		if r.Header.Get("HX-Request") != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/digest", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if id == 0 {
			id, _ = strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		}
		channels, _ := a.db.GetChannels(ctx)
		for _, ch := range channels {
			if ch.ID == id {
				a.Digest(ctx, ch)
				break
			}
		}
		if r.Header.Get("HX-Request") != "" {
			w.WriteHeader(http.StatusOK)
			return
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

		if r.Header.Get("HX-Request") != "" {
			data := a.getDashboardData(ctx, r.URL.Query().Get("token"))
			_ = a.renderTemplate(w, "channel-list", data)
			return
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/del-channel", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if id == 0 {
			id, _ = strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		}
		_ = a.db.DeleteChannel(ctx, id)
		a.setupCron(ctx)

		if r.Header.Get("HX-Request") != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/add-feed", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		chID, _ := strconv.ParseInt(r.FormValue("channel_id"), 10, 64)
		if chID == 0 {
			chID, _ = strconv.ParseInt(r.URL.Query().Get("channel_id"), 10, 64)
		}
		_ = a.db.UpsertFeed(ctx, storage.Feed{
			ChannelID: chID,
			URL:       r.FormValue("url"),
			Active:    true,
		})

		if r.Header.Get("HX-Request") != "" {
			data := a.getChannelData(ctx, chID)
			_ = a.renderTemplate(w, "feed-list", data)
			return
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/del-feed", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if id == 0 {
			id, _ = strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		}
		chID, _ := strconv.ParseInt(r.URL.Query().Get("channel_id"), 10, 64)

		_ = a.db.DeleteFeed(ctx, id)

		if r.Header.Get("HX-Request") != "" {
			data := a.getChannelData(ctx, chID)
			_ = a.renderTemplate(w, "feed-list", data)
			return
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/update-feed-title", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if id == 0 {
			id, _ = strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		}
		title := strings.TrimSpace(r.FormValue("title"))
		_ = a.db.UpdateFeedTitleByID(ctx, id, title)

		if r.Header.Get("HX-Request") != "" {
			feed, _ := a.db.GetFeedByID(ctx, id)
			data := a.getChannelData(ctx, feed.ChannelID)
			_ = a.renderTemplate(w, "feed-list", data)
			return
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/update-channel-cron", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if id == 0 {
			id, _ = strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		}
		cronSpec := strings.TrimSpace(r.FormValue("cron"))
		if cronSpec != "" {
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			if _, err := parser.Parse(cronSpec); err != nil {
				http.Error(w, "Невалидный формат Cron: "+err.Error(), http.StatusBadRequest)
				return
			}

			if err := a.db.UpdateChannelCron(ctx, id, cronSpec); err != nil {
				slog.Error("ошибка обновления cron в БД", "id", id, "error", err)
			}
			a.setupCron(ctx)
		}

		if r.Header.Get("HX-Request") != "" {
			data := a.getChannelData(ctx, id)
			_ = a.renderTemplate(w, "channel-card", data)
			return
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/latest-bot", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if id == 0 {
			id, _ = strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		}
		channels, _ := a.db.GetChannels(ctx)
		var targetCh *storage.Channel
		for _, ch := range channels {
			if ch.ID == id {
				targetCh = &ch
				break
			}
		}
		if targetCh != nil {
			text := a.LatestText(ctx, targetCh.ID)
			_ = a.notif.SendToAdmin(ctx, a.cfg.TelegramAdminID, text)
		}
		if r.Header.Get("HX-Request") != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/digest-bot", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if id == 0 {
			id, _ = strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		}
		channels, _ := a.db.GetChannels(ctx)
		var targetCh *storage.Channel
		for _, ch := range channels {
			if ch.ID == id {
				targetCh = &ch
				break
			}
		}

		if targetCh != nil {
			since := a.digestSince()
			articles, _ := a.db.GetUnsent(ctx, targetCh.ID, since)
			if len(articles) > 0 {
				text, _ := a.sum.Summarize(ctx, articles)
				text += a.statsFooter(ctx, targetCh.ID, targetCh.Timezone, len(articles))
				_ = a.notif.SendToAdmin(ctx, a.cfg.TelegramAdminID, fmt.Sprintf("👤 <b>Черновик дайджеста [%s]:</b>\n\n", targetCh.Name)+text)
				_ = a.db.SetState(ctx, "ai_limits", a.sum.GetLimits())
			} else {
				_ = a.notif.SendToAdmin(ctx, a.cfg.TelegramAdminID, fmt.Sprintf("📭 В канале [%s] новых статей нет.", targetCh.Name))
			}
		}
		if r.Header.Get("HX-Request") != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/update-channel-name", a.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if id == 0 {
			id, _ = strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		}
		chatID := r.FormValue("chat_id")
		if chatID == "" {
			chatID = r.URL.Query().Get("chat_id")
		}
		if title, err := a.notif.GetChatTitle(chatID); err == nil && title != "" {
			_ = a.db.UpdateChannelName(ctx, id, title)
		}

		if r.Header.Get("HX-Request") != "" {
			data := a.getChannelData(ctx, id)
			_ = a.renderTemplate(w, "channel-card", data)
			return
		}
		a.redirect(w, r)
	}))
}

func (a *App) HandleDashboard(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		data := a.getDashboardData(ctx, token)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := a.renderTemplate(w, "dashboard", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
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
