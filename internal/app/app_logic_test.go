package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot-news/internal/config"
	"bot-news/internal/storage"
)

func TestApp_Orchestration(t *testing.T) {
	db, _ := storage.NewDB(":memory:")
	defer db.Close()

	notif := &mockNotifier{adminChan: make(chan string, 1)}
	sum := &mockSummarizer{text: "summary"}
	fetcher := &mockFetcher{
		articles: []storage.Article{
			{GUID: "orchestra-1", Title: "T1", Link: "L1", FeedURL: "http://f1", FetchedAt: time.Now()},
		},
	}
	cfg := config.Config{
		TelegramAdminID: 123,
	}
	app := NewAppWithDeps(cfg, db, fetcher, sum, notif)

	ctx := context.Background()

	t.Run("Fetch", func(t *testing.T) {
		// Добавим активный фид
		_ = db.AddFeed(ctx, "http://f1")

		app.Fetch(ctx)

		count, _ := db.GetUnsentCount(ctx, time.Now().Add(-1*time.Hour))
		if count != 1 {
			t.Errorf("ожидали 1 статью после Fetch, получили %d", count)
		}
	})

	t.Run("Digest", func(t *testing.T) {
		app.Digest(ctx)

		if notif.sentCount != 1 {
			t.Errorf("ожидали 1 отправку в канал, получили %d", notif.sentCount)
		}

		count, _ := db.GetUnsentCount(ctx, time.Now().Add(-1*time.Hour))
		if count != 0 {
			t.Errorf("ожидали 0 неотправленных после Digest, получили %d", count)
		}
	})

	t.Run("Cleanup", func(t *testing.T) {
		// cleanup удаляет статьи старше 30 дней, которые были отправлены (sent=1)
		// У нас есть одна отправленная статья, но она свежая.
		// Нам нужно вручную добавить старую отправленную статью.
		old := time.Now().AddDate(0, 0, -40)
		_ = db.SaveArticles(ctx, []storage.Article{
			{GUID: "old-1", Title: "Old", Link: "L", FeedURL: "F", FetchedAt: old},
		})
		_ = db.MarkSent(ctx, []int64{2}) // Предполагаем ID=2, так как ID=1 был в Fetch/Digest

		app.cleanup(ctx)

		// Проверим, что сообщение админу ушло
		if notif.adminCount != 1 {
			t.Errorf("ожидали уведомление админу об очистке, получили %d", notif.adminCount)
		}
	})

	t.Run("StatsText", func(t *testing.T) {
		text := app.StatsText(ctx)
		if !strings.Contains(text, "Статистика") {
			t.Errorf("неожиданный текст статистики: %q", text)
		}
	})

	t.Run("LatestText Fallback", func(t *testing.T) {
		// Заставим суммаризатор вернуть ошибку
		sum.err = context.DeadlineExceeded

		text := app.LatestText(ctx)
		if !strings.Contains(text, "Последние материалы (AI временно недоступен)") {
			t.Errorf("не сработал fallback для LatestText: %q", text)
		}
	})

	t.Run("Digest No Articles (Heartbeat)", func(t *testing.T) {
		// У нас нет неотправленных статей сейчас
		app.Digest(ctx)
		// mockNotifier.Send должен быть вызван для heartbeat
		if notif.sentCount != 2 { // Был 1 в предыдущем тесте Digest
			t.Errorf("ожидали 2 отправки всего (включая heartbeat), получили %d", notif.sentCount)
		}
	})

	t.Run("Fetch No Feeds", func(_ *testing.T) {
		// Удалим все фиды
		_ = db.DeleteFeed(ctx, "http://f1")
		app.Fetch(ctx)
		// Ничего не должно упасть, просто лог в stdout
	})

	t.Run("Fetch With Errors", func(t *testing.T) {
		_ = db.AddFeed(ctx, "http://error-feed")
		fetcher.err = nil
		fetcher.feedErr = context.DeadlineExceeded
		app.Fetch(ctx)
		// Проверим, что статус фида обновился в базе
		feeds, _ := db.GetAllFeeds(ctx)
		found := false
		for _, f := range feeds {
			if f.URL == "http://error-feed" && f.LastError != "" {
				found = true
				break
			}
		}
		if !found {
			t.Error("ожидали LastError у фида после ошибки Fetch")
		}
	})
}
