package notifier

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	telebot "gopkg.in/telebot.v3"

	"bot-news/internal/retry"
)

const maxMessageLen = 4096

// Telegram отправляет сообщения в канал через Bot API.
type Telegram struct {
	bot  *telebot.Bot
	chat telebot.Recipient
}

func NewTelegram(token, channelID string) (*Telegram, error) {
	b, err := telebot.NewBot(telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	chat, err := parseRecipient(channelID)
	if err != nil {
		return nil, err
	}
	return &Telegram{bot: b, chat: chat}, nil
}

func (t *Telegram) Send(ctx context.Context, text string) error {
	opts := &telebot.SendOptions{
		ParseMode:             telebot.ModeMarkdown,
		DisableWebPagePreview: true,
	}
	for _, chunk := range splitMessage(text, maxMessageLen) {
		chunk := chunk
		err := retry.Do(ctx, 3, func() error {
			_, err := t.bot.Send(t.chat, chunk, opts)
			return err
		})
		if err != nil {
			return fmt.Errorf("telegram send: %w", err)
		}
	}
	return nil
}

// parseRecipient принимает "@username" или числовой ID канала.
func parseRecipient(channelID string) (telebot.Recipient, error) {
	if strings.HasPrefix(channelID, "@") {
		return usernameRecipient(channelID), nil
	}
	id, err := strconv.ParseInt(channelID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("TELEGRAM_CHANNEL_ID %q должен начинаться с @ или быть числом", channelID)
	}
	return numericRecipient(id), nil
}

type usernameRecipient string

func (r usernameRecipient) Recipient() string { return string(r) }

type numericRecipient int64

func (r numericRecipient) Recipient() string { return strconv.FormatInt(int64(r), 10) }

// ListenCommands запускает polling и обрабатывает команды /fetch, /digest, /stats.
// Реагирует только на сообщения от adminID (если задан).
func (t *Telegram) ListenCommands(ctx context.Context, adminID int64, onFetch, onDigest func(), onStats, onLatest func() string, onDigestCount func() int) {
	senderID := func(c telebot.Context) int64 {
		sender := c.Sender()
		if sender == nil {
			return 0
		}
		return sender.ID
	}
	allowed := func(c telebot.Context) bool {
		sender := c.Sender()
		if sender == nil {
			slog.Warn("команда без отправителя")
			return false
		}
		ok := adminID == 0 || sender.ID == adminID
		if !ok {
			slog.Warn("команда отклонена", "sender_id", sender.ID, "username", sender.Username, "admin_id", adminID)
		}
		return ok
	}

	btnFetch := telebot.Btn{Unique: "cb_fetch", Text: "🔄 Собрать статьи"}
	btnDigest := telebot.Btn{Unique: "cb_digest", Text: "📨 Запустить дайджест"}
	keyboard := &telebot.ReplyMarkup{}
	keyboard.Inline(keyboard.Row(btnFetch, btnDigest))

	doFetch := func() string {
		go onFetch()
		return "⏳ Сбор статей запущен..."
	}
	doDigest := func() string {
		n := onDigestCount()
		if n == 0 {
			return "📭 Новых статей за сегодня нет"
		}
		go onDigest()
		return fmt.Sprintf("⏳ Формирую дайджест из %d статей за сегодня...", n)
	}

	t.bot.Handle("/fetch", func(c telebot.Context) error {
		slog.Info("получена команда Telegram", "command", "fetch", "sender_id", senderID(c))
		if !allowed(c) {
			return nil
		}
		return c.Reply(doFetch())
	})
	t.bot.Handle("/digest", func(c telebot.Context) error {
		slog.Info("получена команда Telegram", "command", "digest", "sender_id", senderID(c))
		if !allowed(c) {
			return nil
		}
		return c.Reply(doDigest())
	})
	t.bot.Handle("/stats", func(c telebot.Context) error {
		slog.Info("получена команда Telegram", "command", "stats", "sender_id", senderID(c))
		if !allowed(c) {
			return nil
		}
		return c.Reply(onStats(), keyboard)
	})
	t.bot.Handle("/latest", func(c telebot.Context) error {
		slog.Info("получена команда Telegram", "command", "latest", "sender_id", senderID(c))
		if !allowed(c) {
			return nil
		}
		return c.Reply(onLatest(), keyboard)
	})
	t.bot.Handle(&btnFetch, func(c telebot.Context) error {
		slog.Info("получен callback Telegram", "callback", "fetch", "sender_id", senderID(c))
		if !allowed(c) {
			return c.Respond()
		}
		return c.Respond(&telebot.CallbackResponse{Text: doFetch()})
	})
	t.bot.Handle(&btnDigest, func(c telebot.Context) error {
		slog.Info("получен callback Telegram", "callback", "digest", "sender_id", senderID(c))
		if !allowed(c) {
			return c.Respond()
		}
		return c.Respond(&telebot.CallbackResponse{Text: doDigest()})
	})

	if err := t.bot.SetCommands([]telebot.Command{
		{Text: "digest", Description: "Сформировать и отправить дайджест"},
		{Text: "fetch", Description: "Собрать свежие статьи"},
		{Text: "latest", Description: "Показать последние материалы по фидам"},
		{Text: "stats", Description: "Статистика сбора"},
	}); err != nil {
		slog.Warn("не удалось установить команды бота", "error", err)
	}

	go t.bot.Start()
	slog.Info("бот слушает команды")

	<-ctx.Done()
	t.bot.Stop()
}

// splitMessage делит текст на части не длиннее maxLen рун,
// по возможности разрезая по переносу строки.
func splitMessage(text string, maxLen int) []string {
	runes := []rune(text)
	if len(runes) <= maxLen {
		return []string{text}
	}
	var chunks []string
	for len(runes) > 0 {
		end := maxLen
		if end > len(runes) {
			end = len(runes)
		}
		cut := end
		for i := end - 1; i > 0; i-- {
			if runes[i] == '\n' {
				cut = i + 1
				break
			}
		}
		chunks = append(chunks, string(runes[:cut]))
		runes = runes[cut:]
	}
	return chunks
}
