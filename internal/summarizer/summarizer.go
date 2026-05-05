package summarizer

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"bot-news/internal/storage"
)

// Summarizer формирует текст дайджеста из списка статей.
type Summarizer interface {
	Summarize(ctx context.Context, articles []storage.Article) (string, error)
}

func sourceLabel(feedURL string) string {
	if feedURL == "" {
		return "@unknown"
	}
	u, err := url.Parse(feedURL)
	if err == nil {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) > 0 {
			name := parts[len(parts)-1]
			if name != "" {
				return "@" + name
			}
		}
	}
	return feedURL
}

// SimpleSummarizer — форматирует статьи как Markdown-список без AI.
type SimpleSummarizer struct{}

func NewSimple() *SimpleSummarizer {
	return &SimpleSummarizer{}
}

func (s *SimpleSummarizer) Summarize(_ context.Context, articles []storage.Article) (string, error) {
	if len(articles) == 0 {
		return "", nil
	}

	var sb strings.Builder
	date := time.Now().Format("2 January 2006")
	fmt.Fprintf(&sb, "*Дайджест за %s*\n\n", date)

	for _, a := range articles {
		title := strings.ReplaceAll(a.Title, "[", "\\[")
		title = strings.ReplaceAll(title, "]", "\\]")
		fmt.Fprintf(&sb, "*%s* · %s\n", sourceLabel(a.FeedURL), title)
		if a.Description != "" {
			desc := strings.TrimSpace(a.Description)
			// обрезаем до 200 символов чтобы не раздувать сообщение
			runes := []rune(desc)
			if len(runes) > 200 {
				desc = string(runes[:200]) + "..."
			}
			fmt.Fprintf(&sb, "%s\n", desc)
		}
		if a.Link != "" {
			fmt.Fprintf(&sb, "🔗 %s\n", a.Link)
		}
		fmt.Fprintln(&sb)
	}

	fmt.Fprintf(&sb, "\nВсего: %d материалов", len(articles))
	return sb.String(), nil
}
