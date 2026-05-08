package app_test

import (
	"context"
	"testing"
	"time"

	"bot-news/internal/app"
	"bot-news/internal/config"
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
	a.FetchAll(context.Background())
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
