package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"bot-news/internal/config"
	"bot-news/internal/feed"
	"bot-news/internal/storage"
)

type mockNotifier struct {
	sentCount  int
	adminCount int
	lastAdmin  string
	adminChan  chan string
}

func (m *mockNotifier) Send(_ context.Context, _ string) error {
	m.sentCount++
	return nil
}

func (m *mockNotifier) SendToAdmin(_ context.Context, _ int64, text string) error {
	m.adminCount++
	m.lastAdmin = text
	if m.adminChan != nil {
		m.adminChan <- text
	}
	return nil
}

func (m *mockNotifier) ListenCommands(_ context.Context, _ int64,
	_, _ func(),
	_, _ func() string,
	_ func() int,
	_ func() ([]storage.Feed, error),
	_ func(string) error,
) {
}

type mockSummarizer struct {
	text string
	err  error
}

func (m *mockSummarizer) Summarize(_ context.Context, _ []storage.Article) (string, error) {
	return m.text, m.err
}

func (m *mockSummarizer) GetLimits() string { return "" }

type mockFetcher struct {
	articles []storage.Article
	err      error
}

func (m *mockFetcher) FetchAll(_ context.Context, _ []string) ([]feed.FetchResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return []feed.FetchResult{{Articles: m.articles}}, nil
}

func TestAppServer_Auth(t *testing.T) {
	db, _ := storage.NewDB(":memory:")
	defer db.Close()

	cfg := config.Config{
		TriggerSecret: "secret",
	}
	app := NewAppWithDeps(cfg, db, &mockFetcher{}, &mockSummarizer{}, &mockNotifier{})

	handler := app.authMiddleware(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("Unauthorized", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("ожидали 401, получили %d", rr.Code)
		}
	})

	t.Run("Authorized via Header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", "Bearer secret")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("ожидали 200, получили %d", rr.Code)
		}
	})

	t.Run("Authorized via Query", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/?token=secret", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("ожидали 200, получили %d", rr.Code)
		}
	})
}

func TestAppServer_Triggers(t *testing.T) {
	db, _ := storage.NewDB(":memory:")
	defer db.Close()

	notif := &mockNotifier{adminChan: make(chan string, 1)}
	sum := &mockSummarizer{text: "ai summary"}
	cfg := config.Config{
		TriggerSecret:   "secret",
		TelegramAdminID: 123,
	}
	app := NewAppWithDeps(cfg, db, &mockFetcher{}, sum, notif)

	mux := http.NewServeMux()
	app.registerTriggers(context.Background(), mux)

	t.Run("Add Feed", func(t *testing.T) {
		form := url.Values{}
		form.Add("url", "http://new-feed")
		req := httptest.NewRequest("POST", "/trigger/add?token=secret", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("ожидали 303, получили %d", rr.Code)
		}

		feeds, _ := db.GetAllFeeds(context.Background())
		if len(feeds) != 1 || feeds[0].URL != "http://new-feed" {
			t.Errorf("фид не добавился: %v", feeds)
		}
	})

	t.Run("Toggle Feed", func(t *testing.T) {
		form := url.Values{}
		form.Add("url", "http://new-feed")
		req := httptest.NewRequest("POST", "/trigger/toggle?token=secret", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		feeds, _ := db.GetAllFeeds(context.Background())
		if len(feeds) != 1 || feeds[0].Enabled {
			t.Errorf("фид не выключился: %v", feeds)
		}
	})

	t.Run("Latest Bot Trigger", func(t *testing.T) {
		// Добавим статью, чтобы было что саммаризировать
		_ = db.SaveArticles(context.Background(), []storage.Article{
			{GUID: "1", Title: "T1", Link: "L1", FeedURL: "http://new-feed", FetchedAt: time.Now()},
		})

		req := httptest.NewRequest("POST", "/trigger/latest-bot?token=secret", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Ждем сообщения
		select {
		case msg := <-notif.adminChan:
			if !strings.Contains(msg, "ai summary") {
				t.Errorf("неожиданное сообщение: %q", msg)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("таймаут ожидания сообщения админу")
		}

		if notif.adminCount != 1 {
			t.Errorf("ожидали 1 сообщение админу, получили %d", notif.adminCount)
		}
	})

	t.Run("Digest Bot Trigger", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/trigger/digest-bot?token=secret", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		select {
		case msg := <-notif.adminChan:
			if !strings.Contains(msg, "ai summary") {
				t.Errorf("неожиданное сообщение: %q", msg)
			}
			if !strings.Contains(msg, "персональный дайджест") {
				t.Errorf("отсутствует заголовок дайджеста: %q", msg)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("таймаут ожидания дайджеста")
		}
	})

	t.Run("Delete Feed", func(t *testing.T) {
		form := url.Values{}
		form.Add("url", "http://new-feed")
		req := httptest.NewRequest("POST", "/trigger/delete?token=secret", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		feeds, _ := db.GetAllFeeds(context.Background())
		if len(feeds) != 0 {
			t.Errorf("фид не удалился: %v", feeds)
		}
	})
}

func TestAppServer_Dashboard(t *testing.T) {
	db, _ := storage.NewDB(":memory:")
	defer db.Close()
	_ = db.AddFeed(context.Background(), "http://test-feed")

	app := NewAppWithDeps(config.Config{}, db, &mockFetcher{}, &mockSummarizer{}, &mockNotifier{})

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	app.handleDashboard(context.Background())(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("ожидали 200, получили %d", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "bot-news Dashboard") {
		t.Error("заголовок не найден в HTML")
	}
	if !strings.Contains(body, "http://test-feed") {
		t.Error("URL фида не найден в HTML")
	}
}
