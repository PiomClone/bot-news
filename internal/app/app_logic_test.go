package app_test

import (
	"context"
	"testing"
	"time"

	"bot-news/internal/app"
	"bot-news/internal/config"
	"bot-news/internal/feed"
	"bot-news/internal/storage"
)

func TestApp_FetchAndDigest(t *testing.T) {
	db, _ := storage.NewDB(":memory:")
	defer db.Close()

	chID, _ := db.UpsertChannel(context.Background(), storage.Channel{
		Name: "Test", TelegramChatID: "123", DigestCron: "* * * * *", Active: true,
	})
	_ = db.UpsertFeed(context.Background(), storage.Feed{ChannelID: chID, URL: "http://f1", Active: true})

	fetcher := &mockFetcher{articles: []storage.Article{
		{GUID: "1", Title: "T1", Link: "L1", FetchedAt: time.Now()},
	}}
	notif := &mockNotifier{}
	sum := &mockSummarizer{}

	cfg := config.Config{FetchIntervalMin: 1, Timezone: "UTC"}
	a := app.NewAppWithDeps(cfg, db, fetcher, sum, notif)

	// Fetch
	report, err := a.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if report.TotalArticles != 1 {
		t.Fatalf("ожидали 1 статью в отчете, получили %d", report.TotalArticles)
	}
	count, _ := db.GetUnsentCount(context.Background(), chID, time.Now().Add(-24*time.Hour))
	if count != 1 {
		t.Errorf("ожидали 1 статью, получили %d", count)
	}

	// Digest
	channels, _ := db.GetChannels(context.Background())
	a.Digest(context.Background(), channels[0])

	if len(notif.sent) == 0 {
		t.Error("дайджест не был отправлен")
	}

	count, _ = db.GetUnsentCount(context.Background(), chID, time.Now().Add(-24*time.Hour))
	if count != 0 {
		t.Errorf("после дайджеста должно быть 0 неотправленных, получили %d", count)
	}
}

func TestApp_StatsAndLatest(t *testing.T) {
	db, _ := storage.NewDB(":memory:")
	chID, _ := db.UpsertChannel(context.Background(), storage.Channel{Name: "T", Active: true})

	a := app.NewAppWithDeps(config.Config{}, db, &mockFetcher{}, &mockSummarizer{}, &mockNotifier{})

	stats := a.StatsText(context.Background(), chID)
	if stats == "" {
		t.Error("пустая статистика")
	}

	latest := a.LatestText(context.Background(), chID)
	if latest == "" {
		t.Error("пустой latest")
	}
}

func TestApp_FetchReportAndFeedStatus(t *testing.T) {
	db, _ := storage.NewDB(":memory:")
	defer db.Close()

	chID, _ := db.UpsertChannel(context.Background(), storage.Channel{
		Name: "Tech", TelegramChatID: "123", DigestCron: "* * * * *", Active: true,
	})
	_ = db.UpsertFeed(context.Background(), storage.Feed{ChannelID: chID, URL: "http://ok", Title: "OK Feed", Active: true})
	_ = db.UpsertFeed(context.Background(), storage.Feed{ChannelID: chID, URL: "http://empty", Title: "Empty Feed", Active: true})
	_ = db.UpsertFeed(context.Background(), storage.Feed{ChannelID: chID, URL: "http://bad", Title: "Bad Feed", Active: true})

	fetcher := &mockFetcher{
		results: map[string]feed.FetchResult{
			"http://ok": {
				URL: "http://ok",
				Articles: []storage.Article{
					{GUID: "1", Title: "A1", Link: "L1", FetchedAt: time.Now()},
					{GUID: "2", Title: "A2", Link: "L2", FetchedAt: time.Now()},
				},
			},
			"http://empty": {URL: "http://empty"},
			"http://bad":   {URL: "http://bad", Err: context.DeadlineExceeded},
		},
	}
	a := app.NewAppWithDeps(config.Config{Timezone: "UTC"}, db, fetcher, &mockSummarizer{}, &mockNotifier{})

	report, err := a.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}

	if report.ActiveChannels != 1 || report.ActiveFeeds != 3 {
		t.Fatalf("неверные агрегаты отчета: %+v", report)
	}
	if report.SuccessFeeds != 1 || report.EmptyFeeds != 1 || report.ErrorFeeds != 1 {
		t.Fatalf("неверные статусы отчета: %+v", report)
	}
	if report.TotalArticles != 2 {
		t.Fatalf("ожидали 2 статьи, получили %d", report.TotalArticles)
	}

	feeds, _ := db.GetFeeds(context.Background(), chID)
	statusByURL := make(map[string]storage.Feed)
	for _, f := range feeds {
		statusByURL[f.URL] = f
	}
	if statusByURL["http://ok"].LastFetchedAt.IsZero() || statusByURL["http://ok"].LastError != "" {
		t.Fatalf("успешный фид должен обновить время и очистить ошибку: %+v", statusByURL["http://ok"])
	}
	if statusByURL["http://empty"].LastFetchedAt.IsZero() || statusByURL["http://empty"].LastError != "" {
		t.Fatalf("пустой фид должен считаться успешным fetch: %+v", statusByURL["http://empty"])
	}
	if statusByURL["http://bad"].LastFetchedAt.IsZero() || statusByURL["http://bad"].LastError == "" {
		t.Fatalf("ошибочный фид должен сохранить ошибку: %+v", statusByURL["http://bad"])
	}
}
