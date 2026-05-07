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
	SendToAdmin(ctx context.Context, adminID int64, text string) error
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

	return &App{
		cfg:     cfg,
		db:      db,
		fetcher: feed.NewFetcher(30 * time.Second),
		sum:     sum,
		notif:   notif,
	}, nil
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

	// Синхронизация фидов из конфига в базу
	if err := a.db.SyncFeeds(ctx, a.cfg.FeedURLs); err != nil {
		slog.Error("ошибка синхронизации фидов", "error", err)
	}

	var wg sync.WaitGroup

	// Health check & Dashboard HTTP-сервер
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
				func() { a.Fetch(ctx) },
				func() { a.Digest(ctx) },
				func() string { return a.StatsText(ctx) },
				func() string { return a.LatestText(ctx) },
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

	if _, err := c.AddFunc(fetchSpec, func() { a.Fetch(ctx) }); err != nil {
		slog.Error("ошибка регистрации cron fetch", "error", err)
		os.Exit(1)
	}
	if _, err := c.AddFunc(a.cfg.DigestCron, func() { a.Digest(ctx) }); err != nil {
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
		a.Fetch(ctx)
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

func (a *App) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Если включен TLS (mTLS), то проверка токена не требуется,
		// так как авторизация прошла на уровне протокола.
		if a.cfg.TLSEnabled {
			next(w, r)
			return
		}

		if a.cfg.TriggerSecret != "" {
			token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token != a.cfg.TriggerSecret {
				// Также проверяем в query param для удобства доступа к дашборду
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

func (a *App) runHealthServer(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	mux.HandleFunc("GET /{$}", a.authMiddleware(a.handleDashboard(ctx)))
	a.registerTriggers(ctx, mux)

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
			slog.Error("health server", "error", err)
		}
	}
}

func (a *App) registerTriggers(ctx context.Context, mux *http.ServeMux) {
	mux.HandleFunc("POST /trigger/fetch", a.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		go a.Fetch(ctx)
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/digest", a.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		go a.Digest(ctx)
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/toggle", a.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if url := r.FormValue("url"); url != "" {
			_ = a.db.ToggleFeed(ctx, url)
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/add", a.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if url := strings.TrimSpace(r.FormValue("url")); url != "" {
			_ = a.db.AddFeed(ctx, url)
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/delete", a.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if url := r.FormValue("url"); url != "" {
			_ = a.db.DeleteFeed(ctx, url)
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/update-title", a.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		url := r.FormValue("url")
		title := strings.TrimSpace(r.FormValue("title"))
		if url != "" && title != "" {
			_ = a.db.UpdateFeedTitle(ctx, url, title)
		}
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/latest-bot", a.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		go func() {
			text := a.LatestText(ctx)
			_ = a.notif.SendToAdmin(ctx, a.cfg.TelegramAdminID, text)
		}()
		a.redirect(w, r)
	}))

	mux.HandleFunc("POST /trigger/digest-bot", a.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		go func() {
			since := a.digestSince()
			articles, _ := a.db.GetUnsent(ctx, since)
			if len(articles) == 0 {
				_ = a.notif.SendToAdmin(ctx, a.cfg.TelegramAdminID, "📭 Новых статей нет.")
				return
			}
			text, err := a.sum.Summarize(ctx, articles)
			if err != nil {
				text, _ = summarizer.NewSimple().Summarize(ctx, articles)
			}
			text = "🔔 <b>Ваш персональный дайджест:</b>\n\n" + text
			_ = a.notif.SendToAdmin(ctx, a.cfg.TelegramAdminID, text)
		}()
		a.redirect(w, r)
	}))
}

func (a *App) handleDashboard(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats, _ := a.db.GetStats(ctx)
		feeds, _ := a.db.GetAllFeeds(ctx)
		token := r.URL.Query().Get("token")

		data := struct {
			Stats      storage.Stats
			Feeds      []storage.Feed
			Version    string
			Token      string
			TLSEnabled bool
		}{
			Stats:      stats,
			Feeds:      feeds,
			Version:    "1.1.0",
			Token:      token,
			TLSEnabled: a.cfg.TLSEnabled,
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
    <div class="max-w-4xl mx-auto">
        <header class="flex flex-col md:flex-row justify-between items-start md:items-center mb-8 gap-4">
            <h1 class="text-3xl font-bold text-sky-400 flex items-center gap-3">
                bot-news
                <span class="text-xs font-normal bg-slate-800 text-slate-400 px-2 py-1 rounded border border-slate-700">
                    v{{.Version}}
                </span>
            </h1>
            <div class="flex gap-2">
                <form action="/trigger/latest-bot{{if not .TLSEnabled}}?token={{.Token}}{{end}}" method="POST">
                    <button class="bg-indigo-600 hover:bg-indigo-500 px-4 py-2 rounded 
                        text-sm font-medium transition flex items-center gap-2" 
                        title="Отправить последние новости в бота">
                        🤖 Latest
                    </button>
                </form>
                <form action="/trigger/digest-bot{{if not .TLSEnabled}}?token={{.Token}}{{end}}" method="POST">
                    <button class="bg-violet-600 hover:bg-violet-500 px-4 py-2 rounded 
                        text-sm font-medium transition flex items-center gap-2" 
                        title="Отправить персональный дайджест в бота">
                        👤 Digest
                    </button>
                </form>
                <form action="/trigger/fetch{{if not .TLSEnabled}}?token={{.Token}}{{end}}" method="POST">
                    <button class="bg-sky-600 hover:bg-sky-500 px-4 py-2 rounded 
                        text-sm font-medium transition flex items-center gap-2">
                        🔄 Собрать
                    </button>
                </form>
                <form action="/trigger/digest{{if not .TLSEnabled}}?token={{.Token}}{{end}}" method="POST">
                    <button class="bg-emerald-600 hover:bg-emerald-500 px-4 py-2 rounded 
                        text-sm font-medium transition flex items-center gap-2" 
                        title="Отправить дайджест в КАНАЛ">
                        📢 Канал
                    </button>
                </form>
            </div>
        </header>

        <div class="mb-8">
            <form action="/trigger/add{{if not .TLSEnabled}}?token={{.Token}}{{end}}" method="POST" class="flex gap-2">
                <input type="url" name="url" placeholder="URL нового RSS-фида" required
                    class="flex-1 bg-slate-800 border border-slate-700 rounded-lg px-4 py-2 text-sm 
                        focus:outline-none focus:ring-2 focus:ring-sky-500">
                <button type="submit" 
                    class="bg-sky-600 hover:bg-sky-500 px-6 py-2 rounded-lg text-sm font-medium transition">
                    Добавить фид
                </button>
            </form>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-3 gap-4 mb-8">
            <div class="bg-slate-800/50 p-4 rounded-xl border border-slate-700">
                <div class="text-slate-400 text-xs uppercase tracking-wider mb-1">Всего статей</div>
                <div class="text-2xl font-bold">{{.Stats.TotalArticles}}</div>
            </div>
            <div class="bg-slate-800/50 p-4 rounded-xl border border-slate-700">
                <div class="text-slate-400 text-xs uppercase tracking-wider mb-1">Отправлено</div>
                <div class="text-2xl font-bold text-emerald-400">{{.Stats.SentArticles}}</div>
            </div>
            <div class="bg-slate-800/50 p-4 rounded-xl border border-slate-700">
                <div class="text-slate-400 text-xs uppercase tracking-wider mb-1">Последний сбор</div>
                <div class="text-sm font-medium pt-1">
                    {{if .Stats.LastFetchedAt.IsZero}}
                        <span class="text-slate-500 italic">никогда</span>
                    {{else}}
                        {{.Stats.LastFetchedAt.Format "02 Jan 15:04"}}
                    {{end}}
                </div>
            </div>
        </div>

        <div class="bg-slate-800/30 rounded-xl border border-slate-700 overflow-hidden shadow-xl">
            <div class="px-6 py-4 border-b border-slate-700 bg-slate-800/50 flex justify-between items-center">
                <h2 class="text-lg font-semibold flex items-center gap-2">📡 Источники RSS</h2>
                <span class="text-xs text-slate-500">{{len .Feeds}} фидов</span>
            </div>
            <div class="overflow-x-auto">
                <table class="w-full text-left">
                    <thead class="bg-slate-900/50 text-slate-400 text-xs uppercase tracking-tighter">
                        <tr>
                            <th class="px-6 py-3 font-medium">Статус</th>
                            <th class="px-6 py-3 font-medium">Источник</th>
                            <th class="px-6 py-3 font-medium text-right">Действие</th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-700/50">
                        {{range .Feeds}}
                        <tr class="hover:bg-slate-700/20 transition-colors group">
                            <td class="px-6 py-4">
                                {{if .Enabled}}
                                <span class="inline-flex items-center rounded-full bg-emerald-500/10 
                                    px-2.5 py-0.5 text-xs font-medium text-emerald-400 
                                    ring-1 ring-inset ring-emerald-500/20">
                                    Активен
                                </span>
                                {{else}}
                                <span class="inline-flex items-center rounded-full bg-slate-500/10 
                                    px-2.5 py-0.5 text-xs font-medium text-slate-400 
                                    ring-1 ring-inset ring-slate-500/20">
                                    Пауза
                                </span>
                                {{end}}
                            </td>
                            <td class="px-6 py-4 text-xs font-medium">
                                <form action="/trigger/update-title{{if not $.TLSEnabled}}?token={{$.Token}}{{end}}" 
                                    method="POST" class="group/title flex items-center gap-2">
                                    <input type="hidden" name="url" value="{{.URL}}">
                                    <input type="text" name="title" 
                                        value="{{if .Title}}{{.Title}}{{else}}Без названия{{end}}" 
                                        class="bg-transparent border-b border-transparent hover:border-slate-600 
                                        focus:border-sky-500 focus:outline-none transition-colors font-medium 
                                        text-slate-200 group-hover:text-sky-400 py-0.5 px-0 w-full"
                                        onchange="this.form.submit()">
                                </form>
                                <div class="text-slate-500 truncate max-w-xs md:max-w-md mt-0.5">
                                    {{.URL}}
                                </div>
                            </td>
                            <td class="px-6 py-4 text-right">
                                <div class="flex justify-end gap-3">
                                    <form action="/trigger/toggle{{if not $.TLSEnabled}}?token={{$.Token}}{{end}}" 
                                        method="POST">
                                        <input type="hidden" name="url" value="{{.URL}}">
                                        <button class="text-sky-400 hover:text-sky-300 text-sm font-medium 
                                            underline underline-offset-4 decoration-sky-400/30">
                                            {{if .Enabled}}Выключить{{else}}Включить{{end}}
                                        </button>
                                    </form>
                                    <form action="/trigger/delete{{if not $.TLSEnabled}}?token={{$.Token}}{{end}}" 
                                        method="POST" onsubmit="return confirm('Удалить этот источник?')">
                                        <input type="hidden" name="url" value="{{.URL}}">
                                        <button class="text-rose-400 hover:text-rose-300 text-sm font-medium 
                                            underline underline-offset-4 decoration-rose-400/30">
                                            Удалить
                                        </button>
                                    </form>
                                </div>
                            </td>
                        </tr>
                        {{end}}
                    </tbody>
                </table>
            </div>
        </div>

        <footer class="mt-12 text-center text-slate-600 text-xs border-t border-slate-800 pt-8">
            &copy; 2026 bot-news · Работает на Go {{.Version}} · Статус: OK
        </footer>
    </div>
</body>
</html>
`))

func (a *App) Fetch(ctx context.Context) {
	urls, err := a.db.GetActiveFeedURLs(ctx)
	if err != nil {
		slog.Error("ошибка получения списка фидов из базы", "error", err)
		return
	}
	if len(urls) == 0 {
		slog.Warn("нет активных фидов для сбора")
		return
	}

	results, err := a.fetcher.FetchAll(ctx, urls)
	if err != nil {
		slog.Error("ошибка получения фидов", "error", err)
		return
	}

	var allArticles []storage.Article
	for _, res := range results {
		if res.Err != nil {
			_ = a.db.UpdateFeedStatus(ctx, res.URL, res.Err)
			continue
		}
		allArticles = append(allArticles, res.Articles...)
		_ = a.db.UpdateFeedStatus(ctx, res.URL, nil)
	}

	if err := a.db.SaveArticles(ctx, allArticles); err != nil {
		slog.Error("ошибка сохранения статей", "error", err)
		return
	}
	slog.Info("статьи получены и сохранены", "count", len(allArticles))
}

func (a *App) loc() *time.Location {
	loc, err := time.LoadLocation(a.cfg.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (a *App) digestSince() time.Time {
	return time.Now().Add(-digestLookback)
}

func (a *App) statsFooter(ctx context.Context, digestCount int) string {
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

func (a *App) StatsText(ctx context.Context) string {
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

func (a *App) LatestText(ctx context.Context) string {
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

func (a *App) Digest(ctx context.Context) {
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

func (a *App) cleanup(ctx context.Context) {
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
