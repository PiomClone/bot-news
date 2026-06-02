package app_test

import (
	"context"

	"bot-news/internal/feed"
	"bot-news/internal/storage"
)

type mockFetcher struct {
	articles []storage.Article
	err      error
	results  map[string]feed.FetchResult
}

func (f *mockFetcher) FetchAll(ctx context.Context, urls []string) ([]feed.FetchResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	var res []feed.FetchResult
	for _, u := range urls {
		if f.results != nil {
			if result, ok := f.results[u]; ok {
				cloned := cloneArticles(result.Articles)
				for i := range cloned {
					if cloned[i].FeedURL == "" {
						cloned[i].FeedURL = u
					}
				}
				res = append(res, feed.FetchResult{
					URL:      u,
					Articles: cloned,
					Err:      result.Err,
				})
				continue
			}
		}
		articles := make([]storage.Article, len(f.articles))
		copy(articles, f.articles)
		for i := range articles {
			if articles[i].FeedURL == "" {
				articles[i].FeedURL = u
			}
		}
		res = append(res, feed.FetchResult{URL: u, Articles: articles})
	}
	return res, nil
}

func cloneArticles(src []storage.Article) []storage.Article {
	if len(src) == 0 {
		return nil
	}
	dst := make([]storage.Article, len(src))
	copy(dst, src)
	return dst
}

type mockNotifier struct {
	sent      []string
	sentChats []string
	adminChan chan string
}

func (n *mockNotifier) Send(ctx context.Context, text string) error {
	n.sent = append(n.sent, text)
	return nil
}
func (n *mockNotifier) SendToChat(ctx context.Context, chatID, text string) error {
	n.sent = append(n.sent, text)
	n.sentChats = append(n.sentChats, chatID)
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
	onFetch func(chatID string), onDigest func(),
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
func (s *mockSummarizer) GetLimits() string  { return "" }
func (s *mockSummarizer) SetLimits(l string) {}
