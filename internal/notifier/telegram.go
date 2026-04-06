package notifier

import (
	"context"
	"fmt"
	"strconv"
	"strings"

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
	b, err := telebot.NewBot(telebot.Settings{Token: token})
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
