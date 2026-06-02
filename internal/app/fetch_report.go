package app

import (
	"fmt"
	"html"
	"strings"
	"time"
)

type FetchStatus string

const (
	FetchStatusSuccess FetchStatus = "success"
	FetchStatusEmpty   FetchStatus = "empty"
	FetchStatusError   FetchStatus = "error"
)

type FetchFeedReport struct {
	ChannelID    int64
	ChannelName  string
	FeedURL      string
	FeedTitle    string
	FetchedCount int
	Status       FetchStatus
	ErrorMessage string
}

type FetchChannelReport struct {
	ChannelID   int64
	ChannelName string
	Feeds       []FetchFeedReport
}

type FetchReport struct {
	Channels       []FetchChannelReport
	ActiveChannels int
	ActiveFeeds    int
	SuccessFeeds   int
	EmptyFeeds     int
	ErrorFeeds     int
	TotalArticles  int
	CompletedAt    time.Time
}

func (r FetchReport) HasResults() bool {
	return r.ActiveChannels > 0 || r.ActiveFeeds > 0
}

func (a *App) formatFetchReportTelegram(report FetchReport) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "✅ <b>Сбор статей завершен</b>\n")
	if !report.CompletedAt.IsZero() {
		fmt.Fprintf(&sb, "🕐 %s\n", report.CompletedAt.In(a.loc(a.cfg.Timezone)).Format("02 Jan 15:04:05"))
	}
	fmt.Fprintf(&sb,
		"\n<b>Сводка</b>\nКаналов: %d\nФидов: %d\nУспешно: %d\nПусто: %d\nОшибок: %d\nСтатей получено: %d\n",
		report.ActiveChannels,
		report.ActiveFeeds,
		report.SuccessFeeds,
		report.EmptyFeeds,
		report.ErrorFeeds,
		report.TotalArticles,
	)

	if len(report.Channels) == 0 {
		sb.WriteString("\nАктивных каналов или фидов не найдено.")
		return sb.String()
	}

	for _, ch := range report.Channels {
		fmt.Fprintf(&sb, "\n<b>Канал: %s</b>\n", html.EscapeString(ch.ChannelName))
		if len(ch.Feeds) == 0 {
			sb.WriteString("• Нет активных фидов\n")
			continue
		}
		for _, feed := range ch.Feeds {
			label := feed.FeedTitle
			if label == "" {
				label = sourceLabel(feed.FeedURL)
			}
			switch feed.Status {
			case FetchStatusSuccess:
				fmt.Fprintf(&sb, "✅ <b>%s</b> — %d статей\n", html.EscapeString(label), feed.FetchedCount)
			case FetchStatusEmpty:
				fmt.Fprintf(&sb, "⚪ <b>%s</b> — новых статей нет\n", html.EscapeString(label))
			default:
				fmt.Fprintf(&sb, "❌ <b>%s</b> — %s\n", html.EscapeString(label), html.EscapeString(trimFetchError(feed.ErrorMessage)))
			}
		}
	}

	return sb.String()
}

func trimFetchError(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "неизвестная ошибка"
	}
	runes := []rune(msg)
	if len(runes) > 180 {
		return string(runes[:180]) + "..."
	}
	return msg
}
