package summarizer

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"strings"

	"bot-news/internal/storage"
)

// Summarizer формирует текст дайджеста из списка статей.
type Summarizer interface {
	Summarize(ctx context.Context, articles []storage.Article) (string, error)
	GetLimits() string
	SetLimits(string)
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

var emojis = []string{
	"💰", "🚀", "🏠", "📡", "🎬", "🎨", "🎮", "🎹", "🚜", "⚓",
	"🛡️", "🧬", "🧪", "🔭", "🔬", "🛰️", "🛸", "🧶", "🧵", "🔮",
}

func GetEmoji(feedURL string) string {
	if feedURL == "" {
		return "🔹"
	}
	var sum int
	for _, b := range []byte(feedURL) {
		sum += int(b)
	}
	return emojis[sum%len(emojis)]
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
	fmt.Fprintf(&sb, "<b>Выжимка за 8 часов:</b>\n\n")

	for _, a := range articles {
		source := a.FeedTitle
		if source == "" {
			source = sourceLabel(a.FeedURL)
		}
		emoji := GetEmoji(a.FeedURL)
		title := html.EscapeString(a.Title)
		fmt.Fprintf(&sb, "%s <b>%s</b> · %s\n", emoji, html.EscapeString(source), title)
		if a.Description != "" {
			desc := strings.TrimSpace(a.Description)
			desc = html.EscapeString(desc)
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

func (s *SimpleSummarizer) GetLimits() string {
	return ""
}

func (s *SimpleSummarizer) SetLimits(_ string) {}
