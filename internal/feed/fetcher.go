package feed

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"

	"bot-news/internal/retry"
	"bot-news/internal/storage"
)

const maxConcurrent = 5

type Fetcher struct {
	timeout time.Duration
}

func NewFetcher(timeout time.Duration) *Fetcher {
	return &Fetcher{
		timeout: timeout,
	}
}

// FetchResult — результат сбора одного фида.
type FetchResult struct {
	URL      string
	Articles []storage.Article
	Err      error
}

func (f *Fetcher) FetchAll(ctx context.Context, urls []string) ([]FetchResult, error) {
	sem := make(chan struct{}, maxConcurrent)
	results := make(chan FetchResult, len(urls))
	var wg sync.WaitGroup

	for _, url := range urls {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var articles []storage.Article
			err := retry.Do(ctx, 3, func() error {
				var err error
				articles, err = f.fetchOne(ctx, u)
				return err
			})
			if err != nil {
				slog.Warn("ошибка получения фида", "url", u, "error", err)
				results <- FetchResult{URL: u, Err: err}
				return
			}
			results <- FetchResult{URL: u, Articles: articles}
		}(url)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var all []FetchResult
	for r := range results {
		all = append(all, r)
	}
	return all, nil
}

func (f *Fetcher) fetchOne(ctx context.Context, url string) ([]storage.Article, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	fp := gofeed.NewParser()
	feed, err := fp.ParseURLWithContext(url, ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	articles := make([]storage.Article, 0, len(feed.Items))
	for _, item := range feed.Items {
		guid := item.GUID
		if guid == "" {
			guid = item.Link
		}
		if guid == "" {
			continue
		}
		a := storage.Article{
			FeedURL:     url,
			FeedTitle:   feed.Title,
			GUID:        guid,
			Title:       item.Title,
			Link:        item.Link,
			Description: item.Description,
			FetchedAt:   now,
		}
		if item.PublishedParsed != nil {
			a.PublishedAt = *item.PublishedParsed
		}
		articles = append(articles, a)
	}
	return articles, nil
}
