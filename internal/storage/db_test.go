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
		t.Fatalf("не удалось создать тестовую БД: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustSave(t *testing.T, db *storage.DB, articles []storage.Article) {
	t.Helper()
	if err := db.SaveArticles(context.Background(), articles); err != nil {
		t.Fatalf("SaveArticles: %v", err)
	}
}

func TestSaveArticles_Deduplication(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now()
	mustSave(t, db, []storage.Article{
		{FeedURL: testFeedURL, GUID: "guid-1", Title: "Статья 1", Link: "http://link/1", FetchedAt: now, PublishedAt: now},
		{FeedURL: testFeedURL, GUID: "guid-1", Title: "Статья 1 дубль", Link: "http://link/1", FetchedAt: now, PublishedAt: now},
	})

	since := now.AddDate(0, 0, -1)
	saved, err := db.GetUnsent(ctx, since)
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

	now := time.Now()
	mustSave(t, db, []storage.Article{
		{FeedURL: testFeedURL, GUID: "guid-a", Title: "А", Link: testLinkA, FetchedAt: now, PublishedAt: now},
		{FeedURL: testFeedURL, GUID: "guid-b", Title: "Б", Link: testLinkB, FetchedAt: now, PublishedAt: now},
	})

	since := now.AddDate(0, 0, -1)
	saved, _ := db.GetUnsent(ctx, since)
	if len(saved) != 2 {
		t.Fatalf("ожидали 2 статьи, получили %d", len(saved))
	}

	if err := db.MarkSent(ctx, []int64{saved[0].ID}); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}

	unsent, _ := db.GetUnsent(ctx, since)
	if len(unsent) != 1 {
		t.Fatalf("ожидали 1 неотправленную, получили %d", len(unsent))
	}
}

func TestGetUnsent_SinceFilter(t *testing.T) {
	db := newTestDB(t)

	now := time.Now()
	old := now.AddDate(0, 0, -3)

	// Старая статья — с published_at 3 дня назад
	mustSave(t, db, []storage.Article{
		{FeedURL: testFeedF, GUID: "old-1", Title: "Старая", Link: "http://old", FetchedAt: old, PublishedAt: old},
	})
	// Новая статья — сегодня
	mustSave(t, db, []storage.Article{
		{FeedURL: testFeedF, GUID: "new-1", Title: "Новая", Link: "http://new", FetchedAt: now, PublishedAt: now},
	})

	since := now.AddDate(0, 0, -1)
	unsent, err := db.GetUnsent(context.Background(), since)
	if err != nil {
		t.Fatalf("GetUnsent: %v", err)
	}
	if len(unsent) != 1 {
		t.Fatalf("ожидали 1 статью (только новая), получили %d", len(unsent))
	}
	if unsent[0].GUID != "new-1" {
		t.Errorf("ожидали new-1, получили %q", unsent[0].GUID)
	}
}

func TestGetUnsentNullPublishedAt(t *testing.T) {
	db := newTestDB(t)

	now := time.Now()
	// Статья без published_at — должна попасть по fetched_at
	mustSave(t, db, []storage.Article{
		{FeedURL: testFeedF, GUID: "no-pub", Title: "Без даты", Link: "http://x", FetchedAt: now},
	})

	since := now.AddDate(0, 0, -1)
	unsent, err := db.GetUnsent(context.Background(), since)
	if err != nil {
		t.Fatalf("GetUnsent: %v", err)
	}
	if len(unsent) != 1 {
		t.Fatalf("ожидали 1 статью (fallback на fetched_at), получили %d", len(unsent))
	}
}

func TestMarkSent_Empty(t *testing.T) {
	db := newTestDB(t)
	if err := db.MarkSent(context.Background(), nil); err != nil {
		t.Fatalf("MarkSent(nil): %v", err)
	}
}

func TestGetStats(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Пустая БД
	stats, err := db.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats (пустая БД): %v", err)
	}
	if stats.TotalArticles != 0 || stats.SentArticles != 0 {
		t.Errorf("ожидали 0/0, получили %d/%d", stats.TotalArticles, stats.SentArticles)
	}

	now := time.Now()
	mustSave(t, db, []storage.Article{
		{FeedURL: testFeedF, GUID: "g1", Title: "A", Link: testLinkA, FetchedAt: now, PublishedAt: now},
		{FeedURL: testFeedF, GUID: "g2", Title: "B", Link: testLinkB, FetchedAt: now, PublishedAt: now},
	})

	since := now.AddDate(0, 0, -1)
	saved, _ := db.GetUnsent(ctx, since)
	db.MarkSent(ctx, []int64{saved[0].ID})

	stats, err = db.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.TotalArticles != 2 {
		t.Errorf("TotalArticles: ожидали 2, получили %d", stats.TotalArticles)
	}
	if stats.SentArticles != 1 {
		t.Errorf("SentArticles: ожидали 1, получили %d", stats.SentArticles)
	}
	if stats.LastFetchedAt.IsZero() {
		t.Error("LastFetchedAt не должен быть zero")
	}
}

func TestGetUnsentCount(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now()
	mustSave(t, db, []storage.Article{
		{FeedURL: testFeedF, GUID: "g1", Title: "A", Link: testLinkA, FetchedAt: now, PublishedAt: now},
		{FeedURL: testFeedF, GUID: "g2", Title: "B", Link: testLinkB, FetchedAt: now, PublishedAt: now},
		{FeedURL: testFeedF, GUID: "g-no-pub", Title: "No pub", Link: "http://no-pub", FetchedAt: now},
		{FeedURL: testFeedF, GUID: "g3", Title: "C", Link: "http://c",
			FetchedAt: now.AddDate(0, 0, -3), PublishedAt: now.AddDate(0, 0, -3)},
	})

	since := now.AddDate(0, 0, -1)
	count, err := db.GetUnsentCount(ctx, since)
	if err != nil {
		t.Fatalf("GetUnsentCount: %v", err)
	}
	if count != 3 {
		t.Errorf("ожидали 3 (старая не считается, статья без published_at считается по fetched_at), получили %d", count)
	}
}

func TestGetLatestPerFeed(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	base := time.Now()
	mustSave(t, db, []storage.Article{
		{FeedURL: "http://rss/a", GUID: "a-old", Title: "A old", Link: "http://a/old", FetchedAt: base.Add(-3 * time.Hour), PublishedAt: base.Add(-3 * time.Hour)},
		{FeedURL: "http://rss/a", GUID: "a-new", Title: "A new", Link: "http://a/new", FetchedAt: base.Add(-1 * time.Hour), PublishedAt: base.Add(-1 * time.Hour)},
		{FeedURL: "http://rss/a", GUID: "a-mid", Title: "A mid", Link: "http://a/mid", FetchedAt: base.Add(-2 * time.Hour), PublishedAt: base.Add(-2 * time.Hour)},
		{FeedURL: "http://rss/b", GUID: "b-old", Title: "B old", Link: "http://b/old", FetchedAt: base.Add(-4 * time.Hour)},
		{FeedURL: "http://rss/b", GUID: "b-new", Title: "B new", Link: "http://b/new", FetchedAt: base.Add(-30 * time.Minute)},
	})

	saved, err := db.GetUnsent(ctx, base.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("GetUnsent: %v", err)
	}
	if err := db.MarkSent(ctx, []int64{saved[2].ID}); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}

	latest, err := db.GetLatestPerFeed(ctx, 2)
	if err != nil {
		t.Fatalf("GetLatestPerFeed: %v", err)
	}
	if len(latest) != 4 {
		t.Fatalf("ожидали 4 статьи, получили %d: %#v", len(latest), latest)
	}

	wantTitles := []string{"A new", "A mid", "B new", "B old"}
	for i, want := range wantTitles {
		if latest[i].Title != want {
			t.Fatalf("latest[%d]: ожидали %q, получили %q", i, want, latest[i].Title)
		}
	}
	if !latest[1].Sent {
		t.Fatalf("ожидали, что отправленный материал тоже попадет в latest")
	}
}

func TestDigests(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Сначала пусто
	last, err := db.GetLastDigest(ctx)
	if err != nil {
		t.Fatalf("GetLastDigest (empty): %v", err)
	}
	if last != "" {
		t.Errorf("ожидали пустую строку, получили %q", last)
	}

	// Сохраняем первый
	content1 := "Дайджест 1"
	if err := db.SaveDigest(ctx, content1); err != nil {
		t.Fatalf("SaveDigest 1: %v", err)
	}

	// Сохраняем второй
	content2 := "Дайджест 2"
	if err := db.SaveDigest(ctx, content2); err != nil {
		t.Fatalf("SaveDigest 2: %v", err)
	}

	// Должен вернуть последний (второй)
	last, err = db.GetLastDigest(ctx)
	if err != nil {
		t.Fatalf("GetLastDigest: %v", err)
	}
	if last != content2 {
		t.Errorf("ожидали %q, получили %q", content2, last)
	}
}

func TestDeleteOldArticles(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now()
	old := now.AddDate(0, 0, -40)

	mustSave(t, db, []storage.Article{
		{FeedURL: testFeedF, GUID: "old-sent", Title: "Старая отправленная", FetchedAt: old},
		{FeedURL: testFeedF, GUID: "old-unsent", Title: "Старая неотправленная", FetchedAt: old},
		{FeedURL: testFeedF, GUID: "new-sent", Title: "Новая отправленная", FetchedAt: now},
	})

	// Помечаем "отправленными"
	since := now.AddDate(0, 0, -50)
	saved, _ := db.GetUnsent(ctx, since)
	ids := []int64{saved[0].ID, saved[2].ID} // old-sent и new-sent
	db.MarkSent(ctx, ids)

	// Удаляем старше 30 дней
	deleted, err := db.DeleteOldArticles(ctx, 30)
	if err != nil {
		t.Fatalf("DeleteOldArticles: %v", err)
	}
	if deleted != 1 {
		t.Errorf("ожидали удаление 1 статьи, удалено %d", deleted)
	}

	// Проверяем, что осталась 1 неотправленная и 1 новая отправленная
	stats, _ := db.GetStats(ctx)
	if stats.TotalArticles != 2 {
		t.Errorf("ожидали 2 статьи в базе, осталось %d", stats.TotalArticles)
	}
}
