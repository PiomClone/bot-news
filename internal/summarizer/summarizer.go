package summarizer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"bot-news/internal/storage"
)

// Summarizer формирует текст дайджеста из списка статей.
type Summarizer interface {
	Summarize(ctx context.Context, articles []storage.Article) (string, error)
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
		if a.Link != "" {
			fmt.Fprintf(&sb, "• [%s](%s)\n", title, a.Link)
		} else {
			fmt.Fprintf(&sb, "• %s\n", title)
		}
	}

	fmt.Fprintf(&sb, "\nВсего: %d материалов", len(articles))
	return sb.String(), nil
}
