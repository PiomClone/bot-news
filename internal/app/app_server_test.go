package app_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"bot-news/internal/app"
	"bot-news/internal/config"
	"bot-news/internal/feed"
	"bot-news/internal/storage"
)

func TestAppServer_Auth(t *testing.T) {
	db, _ := storage.NewDB(":memory:")
	defer db.Close()

	cfg := config.Config{
		TriggerSecret: "secret",
	}
	a := app.NewAppWithDeps(cfg, db, &mockFetcher{}, &mockSummarizer{}, &mockNotifier{})

	handler := a.AuthMiddleware(func(w http.ResponseWriter, _ *http.Request) {
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
}

func TestAppServer_Triggers(t *testing.T) {
	db, _ := storage.NewDB(":memory:")
	defer db.Close()

	notif := &mockNotifier{adminChan: make(chan string, 1)}
	sum := &mockSummarizer{}
	cfg := config.Config{
		TriggerSecret:   "secret",
		TelegramAdminID: 123,
	}
	a := app.NewAppWithDeps(cfg, db, &mockFetcher{}, sum, notif)

	mux := http.NewServeMux()
	a.RegisterTriggers(context.Background(), mux)

	t.Run("Add Channel", func(t *testing.T) {
		form := url.Values{}
		form.Add("name", "New Channel")
		form.Add("chat_id", "456")
		form.Add("cron", "* * * * *")
		form.Add("timezone", "UTC")

		req := httptest.NewRequest("POST", "/trigger/add-channel?token=secret", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		channels, _ := db.GetChannels(context.Background())
		if len(channels) != 1 || channels[0].Name != "New Channel" {
			t.Errorf("канал не добавился: %v", channels)
		}
	})

	t.Run("Add Feed", func(t *testing.T) {
		channels, _ := db.GetChannels(context.Background())
		form := url.Values{}
		form.Add("url", "http://new-feed")
		form.Add("channel_id", fmt.Sprintf("%d", channels[0].ID))

		req := httptest.NewRequest("POST", "/trigger/add-feed?token=secret", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		feeds, _ := db.GetAllFeeds(context.Background())
		if len(feeds) != 1 || feeds[0].URL != "http://new-feed" {
			t.Errorf("фид не добавился: %v", feeds)
		}
	})

	t.Run("Fetch All HTMX", func(t *testing.T) {
		channels, _ := db.GetChannels(context.Background())
		if len(channels) == 0 {
			t.Fatal("expected channel to exist")
		}
		if err := db.UpsertFeed(context.Background(), storage.Feed{
			ChannelID: channels[0].ID,
			URL:       "http://broken-feed",
			Title:     "Broken Feed",
			Active:    true,
		}); err != nil {
			t.Fatalf("UpsertFeed: %v", err)
		}

		fetcher := &mockFetcher{
			results: map[string]feed.FetchResult{
				"http://new-feed": {
					URL: "http://new-feed",
					Articles: []storage.Article{
						{GUID: "1", Title: "Fetched", Link: "http://article"},
					},
				},
				"http://broken-feed": {
					URL: "http://broken-feed",
					Err: fmt.Errorf("boom"),
				},
			},
		}
		a = app.NewAppWithDeps(cfg, db, fetcher, sum, notif)
		mux = http.NewServeMux()
		a.RegisterTriggers(context.Background(), mux)

		req := httptest.NewRequest("POST", "/trigger/fetch-all?token=secret", nil)
		req.Header.Set("HX-Request", "true")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("ожидали 200, получили %d", rr.Code)
		}

		body := rr.Body.String()
		if !strings.Contains(body, "Результат fetch") {
			t.Fatalf("нет fetch-отчета в ответе: %s", body)
		}
		if !strings.Contains(body, "hx-swap-oob=\"outerHTML\"") {
			t.Fatalf("нет OOB-обновления channel-list: %s", body)
		}
		if !strings.Contains(body, "boom") {
			t.Fatalf("нет текста ошибки фида в ответе: %s", body)
		}
		if !strings.Contains(body, "Последний fetch") {
			t.Fatalf("обновленный статус фида не попал в ответ: %s", body)
		}
	})
}

func TestAppServer_Dashboard(t *testing.T) {
	db, _ := storage.NewDB(":memory:")
	defer db.Close()
	_, _ = db.GetDefaultChannel(context.Background())
	_ = db.AddFeed(context.Background(), "http://test-feed")

	a := app.NewAppWithDeps(config.Config{}, db, &mockFetcher{}, &mockSummarizer{}, &mockNotifier{})

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	a.HandleDashboard(context.Background())(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("ожидали 200, получили %d", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "bot-news") {
		t.Errorf("заголовок не найден в HTML. Тело ответа: %s", body)
	}
}
