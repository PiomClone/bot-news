package notifier

import (
	"context"
	"fmt"
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
		ParseMode:             telebot.ModeHTML,
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

func (t *Telegram) SendToAdmin(ctx context.Context, adminID int64, text string) error {
	if adminID == 0 {
		return nil
	}
	opts := &telebot.SendOptions{
		ParseMode:             telebot.ModeHTML,
		DisableWebPagePreview: true,
	}
	for _, chunk := range splitMessage(text, maxMessageLen) {
		chunk := chunk
		err := retry.Do(ctx, 3, func() error {
			_, err := t.bot.Send(numericRecipient(adminID), chunk, opts)
			return err
		})
		if err != nil {
			return fmt.Errorf("telegram admin send: %w", err)
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

// ListenCommands запускает polling и обрабатывает команды.
func (t *Telegram) ListenCommands(
	ctx context.Context,
	adminID int64,
	onFetch, onDigest func(),
	onStats, onLatest func() string,
	onDigestCount func() int,
) {
	allowed := func(c telebot.Context) bool {
		sender := c.Sender()
		if sender == nil {
			return false
		}
		return adminID == 0 || sender.ID == adminID
	}

	btnFetch := telebot.Btn{Unique: "cb_fetch", Text: "🔄 Собрать статьи"}
	btnDigest := telebot.Btn{Unique: "cb_digest", Text: "📨 Запустить дайджест"}
	keyboard := &telebot.ReplyMarkup{}
	keyboard.Inline(keyboard.Row(btnFetch, btnDigest))

	opts := &telebot.SendOptions{ParseMode: telebot.ModeHTML, DisableWebPagePreview: true}

	t.bot.Handle("/fetch", func(c telebot.Context) error {
		if !allowed(c) {
			return nil
		}
		go onFetch()
		return c.Reply("⏳ Сбор статей запущен...", opts)
	})
	t.bot.Handle("/digest", func(c telebot.Context) error {
		if !allowed(c) {
			return nil
		}
		n := onDigestCount()
		if n == 0 {
			return c.Reply("📭 Новых статей за сегодня нет", opts)
		}
		go onDigest()
		return c.Reply(fmt.Sprintf("⏳ Формирую дайджест из %d статей за сегодня...", n), opts)
	})
	t.bot.Handle("/stats", func(c telebot.Context) error {
		if !allowed(c) {
			return nil
		}
		return c.Reply(onStats(), keyboard, opts)
	})
	t.bot.Handle("/latest", func(c telebot.Context) error {
		if !allowed(c) {
			return nil
		}
		return c.Reply(onLatest(), keyboard, opts)
	})
	t.setupCallbacks(allowed, onFetch, onDigest, onDigestCount)

	_ = t.bot.SetCommands([]telebot.Command{
		{Text: "digest", Description: "Сформировать и отправить дайджест"},
		{Text: "fetch", Description: "Собрать свежие статьи"},
		{Text: "latest", Description: "Показать последние материалы по фидам"},
		{Text: "stats", Description: "Статистика сбора"},
	})

	go t.bot.Start()
	<-ctx.Done()
	t.bot.Stop()
}

func (t *Telegram) setupCallbacks(
	allowed func(telebot.Context) bool,
	onFetch, onDigest func(),
	onDigestCount func() int,
) {
	btnFetch := telebot.Btn{Unique: "cb_fetch"}
	btnDigest := telebot.Btn{Unique: "cb_digest"}

	t.bot.Handle(&btnFetch, func(c telebot.Context) error {
		if !allowed(c) {
			return c.Respond()
		}
		go onFetch()
		return c.Respond(&telebot.CallbackResponse{Text: "⏳ Сбор статей запущен..."})
	})
	t.bot.Handle(&btnDigest, func(c telebot.Context) error {
		if !allowed(c) {
			return c.Respond()
		}
		n := onDigestCount()
		if n == 0 {
			return c.Respond(&telebot.CallbackResponse{Text: "📭 Новых статей нет"})
		}
		go onDigest()
		return c.Respond(&telebot.CallbackResponse{Text: "⏳ Формирую дайджест..."})
	})
}

// splitMessage делит текст на части.
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
