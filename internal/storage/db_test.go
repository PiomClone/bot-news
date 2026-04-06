package storage_test

import (
	"context"
	"testing"
	"time"

	"bot-news/internal/storage"
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

func TestSaveArticles_Deduplication(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	articles := []storage.Article{
		{FeedURL: "http://feed.example", GUID: "guid-1", Title: "Статья 1", Link: "http://link/1", FetchedAt: time.Now()},
		{FeedURL: "http://feed.example", GUID: "guid-1", Title: "Статья 1 дубль", Link: "http://link/1", FetchedAt: time.Now()},
	}

	if err := db.SaveArticles(ctx, articles); err != nil {
		t.Fatalf("SaveArticles: %v", err)
	}

	// Дубль не должен сохраниться
	since := time.Now().AddDate(0, 0, -1)
	saved, err := db.GetUnsent(ctx, since)
	if err != nil {
		t.Fatalf("GetUnsent: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("ожидали 1 статью (dedup), получили %d", len(saved))
	}
	if saved[0].Title != "Статья 1" {
		t.Errorf("неожиданный заголовок: %q", saved[0].Title)
	}
}

func TestMarkSent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	articles := []storage.Article{
		{FeedURL: "http://feed.example", GUID: "guid-a", Title: "А", Link: "http://a", FetchedAt: time.Now()},
		{FeedURL: "http://feed.example", GUID: "guid-b", Title: "Б", Link: "http://b", FetchedAt: time.Now()},
	}
	if err := db.SaveArticles(ctx, articles); err != nil {
		t.Fatalf("SaveArticles: %v", err)
	}

	since := time.Now().AddDate(0, 0, -1)
	saved, err := db.GetUnsent(ctx, since)
	if err != nil {
		t.Fatalf("GetUnsent: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("ожидали 2 статьи, получили %d", len(saved))
	}

	ids := []int64{saved[0].ID}
	if err := db.MarkSent(ctx, ids); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}

	unsent, err := db.GetUnsent(ctx, since)
	if err != nil {
		t.Fatalf("GetUnsent после MarkSent: %v", err)
	}
	if len(unsent) != 1 {
		t.Fatalf("ожидали 1 неотправленную, получили %d", len(unsent))
	}
}

func TestGetUnsent_SinceFilter(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	old := time.Now().AddDate(0, 0, -3)
	articles := []storage.Article{
		{FeedURL: "http://f", GUID: "old-1", Title: "Старая", Link: "http://old", FetchedAt: old},
	}
	if err := db.SaveArticles(ctx, articles); err != nil {
		t.Fatalf("SaveArticles: %v", err)
	}

	// Запрашиваем только за последние сутки — старая не должна попасть
	since := time.Now().AddDate(0, 0, -1)
	unsent, err := db.GetUnsent(ctx, since)
	if err != nil {
		t.Fatalf("GetUnsent: %v", err)
	}
	if len(unsent) != 0 {
		t.Fatalf("ожидали 0 статей (старее since), получили %d", len(unsent))
	}
}

func TestMarkSent_Empty(t *testing.T) {
	db := newTestDB(t)
	// Не должен падать на пустом списке
	if err := db.MarkSent(context.Background(), nil); err != nil {
		t.Fatalf("MarkSent(nil): %v", err)
	}
}
