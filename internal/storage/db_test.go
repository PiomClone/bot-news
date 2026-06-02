package storage_test

import (
	"context"
	"testing"
	"time"

	"bot-news/internal/storage"
)

const (
	testFeedURL = "http://feed"
	testFeedF   = "http://f"
	testLinkA   = "http://a"
	testLinkB   = "http://b"
)

func newTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func mustCreateChannel(t *testing.T, db *storage.DB, name string) int64 {
	t.Helper()
	id, err := db.UpsertChannel(context.Background(), storage.Channel{
		Name:           name,
		TelegramChatID: "@test",
		DigestCron:     "0 9 * * *",
		Timezone:       "UTC",
		Active:         true,
	})
	if err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	return id
}

func mustSave(t *testing.T, db *storage.DB, articles []storage.Article) {
	t.Helper()
	for _, a := range articles {
		_ = db.UpsertFeed(context.Background(), storage.Feed{
			ChannelID: a.ChannelID,
			URL:       a.FeedURL,
			Active:    true,
		})
	}
	if err := db.SaveArticles(context.Background(), articles); err != nil {
		t.Fatalf("SaveArticles: %v", err)
	}
}

func TestGetLatestPerFeed(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	chID := mustCreateChannel(t, db, "test")

	// Сначала создаем фиды, так как теперь есть JOIN
	_ = db.UpsertFeed(ctx, storage.Feed{ChannelID: chID, URL: "f1", Active: true})
	_ = db.UpsertFeed(ctx, storage.Feed{ChannelID: chID, URL: "f2", Active: true})

	articles := []storage.Article{
		{ChannelID: chID, FeedURL: "f1", GUID: "1", Title: "T1", Link: "L1", FetchedAt: time.Now()},
		{ChannelID: chID, FeedURL: "f1", GUID: "2", Title: "T2", Link: "L2", FetchedAt: time.Now().Add(-time.Hour)},
		{ChannelID: chID, FeedURL: "f2", GUID: "3", Title: "T3", Link: "L3", FetchedAt: time.Now()},
	}
	mustSave(t, db, articles)

	res, err := db.GetLatestPerFeed(ctx, chID, 1)
	if err != nil {
		t.Fatalf("GetLatestPerFeed failed: %v", err)
	}

	if len(res) != 2 {
		t.Errorf("expected 2 articles (one from each active feed), got %d", len(res))
	}

	// Проверка фильтрации по channel_id
	chID2 := mustCreateChannel(t, db, "test2")
	_ = db.UpsertFeed(ctx, storage.Feed{ChannelID: chID2, URL: "f3", Active: true})
	
	mustSave(t, db, []storage.Article{
		{ChannelID: chID2, FeedURL: "f3", GUID: "4", Title: "T4", Link: "L4", FetchedAt: time.Now()},
	})

	res2, _ := db.GetLatestPerFeed(ctx, chID2, 1)
	if len(res2) != 1 || res2[0].Title != "T4" {
		t.Errorf("expected T4, got %v", res2)
	}

	// Делаем единственный фид второго канала неактивным
	feeds, _ := db.GetFeeds(ctx, chID2)
	f3 := feeds[0]
	f3.Active = false
	_ = db.UpsertFeed(ctx, f3)

	res3, _ := db.GetLatestPerFeed(ctx, chID2, 1)
	if len(res3) != 0 {
		t.Errorf("expected 0 articles for inactive feed, got %d", len(res3))
	}
}

func TestSaveArticles_Deduplication(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	chID := mustCreateChannel(t, db, "Test")

	now := time.Now()
	mustSave(t, db, []storage.Article{
		{
			ChannelID: chID,
			FeedURL:   testFeedURL, FeedTitle: "Source 1", GUID: "guid-1",
			Title: "Статья 1", Link: "http://link/1", FetchedAt: now, PublishedAt: now,
		},
		{
			ChannelID: chID,
			FeedURL:   testFeedURL, FeedTitle: "Source 1", GUID: "guid-1",
			Title: "Статья 1 дубль", Link: "http://link/1", FetchedAt: now, PublishedAt: now,
		},
	})

	since := now.AddDate(0, 0, -1)
	saved, err := db.GetUnsent(ctx, chID, since)
	if err != nil {
		t.Fatalf("GetUnsent: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("ожидали 1 статью (dedup), получили %d", len(saved))
	}
	if saved[0].Title != "Статья 1 дубль" {
		t.Errorf("неожиданный заголовок: %q, ожидали обновление (upsert)", saved[0].Title)
	}
}

func TestMarkSent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	chID := mustCreateChannel(t, db, "Test")

	now := time.Now()
	mustSave(t, db, []storage.Article{
		{
			ChannelID: chID,
			FeedURL:   testFeedURL, FeedTitle: "A", GUID: "guid-a",
			Title: "А", Link: testLinkA, FetchedAt: now, PublishedAt: now,
		},
		{
			ChannelID: chID,
			FeedURL:   testFeedURL, FeedTitle: "B", GUID: "guid-b",
			Title: "Б", Link: testLinkB, FetchedAt: now, PublishedAt: now,
		},
	})

	since := now.AddDate(0, 0, -1)
	saved, _ := db.GetUnsent(ctx, chID, since)
	if len(saved) != 2 {
		t.Fatalf("ожидали 2 статьи, получили %d", len(saved))
	}

	if err := db.MarkSent(ctx, []int64{saved[0].ID}); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}

	unsent, _ := db.GetUnsent(ctx, chID, since)
	if len(unsent) != 1 {
		t.Fatalf("ожидали 1 неотправленную, получили %d", len(unsent))
	}
}

func TestGetUnsent_SinceFilter(t *testing.T) {
	db := newTestDB(t)
	chID := mustCreateChannel(t, db, "Test")

	now := time.Now()
	old := now.AddDate(0, 0, -3)

	mustSave(t, db, []storage.Article{
		{
			ChannelID: chID,
			FeedURL:   testFeedF, GUID: "old-1",
			Title: "Старая", Link: "http://old", FetchedAt: old, PublishedAt: old,
		},
		{
			ChannelID: chID,
			FeedURL:   testFeedF, GUID: "new-1",
			Title: "Новая", Link: "http://new", FetchedAt: now, PublishedAt: now,
		},
	})

	since := now.AddDate(0, 0, -1)
	unsent, err := db.GetUnsent(context.Background(), chID, since)
	if err != nil {
		t.Fatalf("GetUnsent: %v", err)
	}
	if len(unsent) != 1 {
		t.Fatalf("ожидали 1 статью, получили %d", len(unsent))
	}
}

func TestGetUnsentNullPublishedAt(t *testing.T) {
	db := newTestDB(t)
	chID := mustCreateChannel(t, db, "Test")

	now := time.Now()
	mustSave(t, db, []storage.Article{
		{
			ChannelID: chID,
			FeedURL:   testFeedF, GUID: "no-pub",
			Title: "Без даты", Link: "http://x", FetchedAt: now,
		},
	})

	since := now.AddDate(0, 0, -1)
	unsent, _ := db.GetUnsent(context.Background(), chID, since)
	if len(unsent) != 1 {
		t.Fatalf("ожидали 1 статью, получили %d", len(unsent))
	}
}

func TestGetStats(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	chID := mustCreateChannel(t, db, "Test")

	stats, _ := db.GetStats(ctx, chID)
	if stats.TotalArticles != 0 {
		t.Errorf("ожидали 0, получили %d", stats.TotalArticles)
	}

	now := time.Now()
	mustSave(t, db, []storage.Article{
		{ChannelID: chID, FeedURL: testFeedF, GUID: "g1", Title: "A", FetchedAt: now},
	})

	stats, _ = db.GetStats(ctx, chID)
	if stats.TotalArticles != 1 {
		t.Errorf("ожидали 1, получили %d", stats.TotalArticles)
	}
}

func TestGetUnsentCount(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	chID := mustCreateChannel(t, db, "Test")

	now := time.Now()
	mustSave(t, db, []storage.Article{
		{ChannelID: chID, FeedURL: testFeedF, GUID: "g1", Title: "A", FetchedAt: now},
		{ChannelID: chID, FeedURL: testFeedF, GUID: "g2", Title: "B", FetchedAt: now.AddDate(0, 0, -5)},
	})

	since := now.AddDate(0, 0, -1)
	count, _ := db.GetUnsentCount(ctx, chID, since)
	if count != 1 {
		t.Errorf("ожидали 1, получили %d", count)
	}
}

func TestDeleteOldArticles(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	chID := mustCreateChannel(t, db, "Test")

	now := time.Now()
	old := now.AddDate(0, 0, -40)

	mustSave(t, db, []storage.Article{
		{ChannelID: chID, FeedURL: testFeedF, GUID: "old-sent", Title: "S", FetchedAt: old},
		{ChannelID: chID, FeedURL: testFeedF, GUID: "new-unsent", Title: "U", FetchedAt: now},
	})

	since := now.AddDate(0, 0, -50)
	saved, _ := db.GetUnsent(ctx, chID, since)
	_ = db.MarkSent(ctx, []int64{saved[0].ID})

	deleted, _ := db.DeleteOldArticles(ctx, 30)
	if deleted != 1 {
		t.Errorf("удалено %d, ожидали 1", deleted)
	}

	stats, _ := db.GetStats(ctx, chID)
	if stats.TotalArticles != 1 {
		t.Errorf("осталось %d, ожидали 1", stats.TotalArticles)
	}
}
