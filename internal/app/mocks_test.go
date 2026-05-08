package app_test

import (
	"context"

	"bot-news/internal/feed"
	"bot-news/internal/storage"
)

type mockFetcher struct {
	articles []storage.Article
	err      error
}

func (f *mockFetcher) FetchAll(ctx context.Context, urls []string) ([]feed.FetchResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	var res []feed.FetchResult
	for _, u := range urls {
		res = append(res, feed.FetchResult{URL: u, Articles: f.articles})
	}
	return res, nil
}

type mockNotifier struct {
	sent []string
	adminChan chan string
}

func (n *mockNotifier) Send(ctx context.Context, text string) error {
	n.sent = append(n.sent, text)
	return nil
}
func (n *mockNotifier) SendToChat(ctx context.Context, chatID, text string) error {
	n.sent = append(n.sent, text)
	return nil
}
func (n *mockNotifier) SendToAdmin(ctx context.Context, adminID int64, text string) error {
	n.sent = append(n.sent, text)
	if n.adminChan != nil {
		n.adminChan <- text
	}
	return nil
}
func (n *mockNotifier) GetChatTitle(chatID string) (string, error) {
	return "Mock Channel", nil
}
func (n *mockNotifier) ListenCommands(ctx context.Context, adminID int64,
	onFetch, onDigest func(),
	onStats, onLatest func() string,
	onDigestCount func() int,
	onFeeds func() ([]storage.Feed, error),
	onToggleFeed func(string) error,
) {
}

type mockSummarizer struct{}

func (s *mockSummarizer) Summarize(ctx context.Context, articles []storage.Article) (string, error) {
	return "summary", nil
}
func (s *mockSummarizer) GetLimits() string { return "" }
func (s *mockSummarizer) SetLimits(l string) {}
