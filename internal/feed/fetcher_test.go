package feed_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"bot-news/internal/feed"
)

const rssTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Feed</title>
    <link>https://example.com</link>
    <item>
      <title>%s</title>
      <link>https://example.com/1</link>
      <guid>guid-1</guid>
      <pubDate>Mon, 07 Apr 2026 10:00:00 +0000</pubDate>
      <description>Описание первой статьи</description>
    </item>
    <item>
      <title>Статья два</title>
      <link>https://example.com/2</link>
      <guid>guid-2</guid>
      <pubDate>Mon, 07 Apr 2026 11:00:00 +0000</pubDate>
    </item>
  </channel>
</rss>`

func newTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchAll(t *testing.T) {
	rss := fmt.Sprintf(rssTemplate, "Статья один")
	srv := newTestServer(t, rss)

	f := feed.NewFetcher(5 * time.Second)
	articles, err := f.FetchAll(context.Background(), []string{srv.URL})
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if len(articles) != 2 {
		t.Fatalf("ожидали 2 статьи, получили %d", len(articles))
	}
	if articles[0].Title != "Статья один" {
		t.Errorf("неожиданный заголовок: %q", articles[0].Title)
	}
	if articles[0].Description != "Описание первой статьи" {
		t.Errorf("неожиданное описание: %q", articles[0].Description)
	}
	if articles[0].PublishedAt.IsZero() {
		t.Error("PublishedAt не должен быть zero")
	}
}

func TestFetchAllBadURL(t *testing.T) {
	f := feed.NewFetcher(1 * time.Second)
	articles, err := f.FetchAll(context.Background(), []string{"http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("FetchAll должен возвращать nil при ошибке фида: %v", err)
	}
	if len(articles) != 0 {
		t.Errorf("ожидали 0 статей при недоступном URL, получили %d", len(articles))
	}
}

func TestFetchAllEmpty(t *testing.T) {
	f := feed.NewFetcher(5 * time.Second)
	articles, err := f.FetchAll(context.Background(), nil)
	if err != nil {
		t.Fatalf("FetchAll(nil): %v", err)
	}
	if len(articles) != 0 {
		t.Errorf("ожидали 0 статей, получили %d", len(articles))
	}
}

func TestFetchAllMultipleFeeds(t *testing.T) {
	srv1 := newTestServer(t, fmt.Sprintf(rssTemplate, "Feed1 статья"))
	srv2 := newTestServer(t, fmt.Sprintf(rssTemplate, "Feed2 статья"))

	f := feed.NewFetcher(5 * time.Second)
	articles, err := f.FetchAll(context.Background(), []string{srv1.URL, srv2.URL})
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if len(articles) != 4 {
		t.Fatalf("ожидали 4 статьи (2 фида × 2), получили %d", len(articles))
	}
}
