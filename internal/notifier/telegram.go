package notifier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	telebot "gopkg.in/telebot.v3"

	"bot-news/internal/retry"
	"bot-news/internal/storage"
)

const maxMessageLen = 4096

// Telegram отправляет сообщения в канал через Bot API.
type Telegram struct {
	bot  *telebot.Bot
	chat telebot.Recipient

	feedMap map[string]string // hash -> URL
	mu      sync.Mutex
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
	return &Telegram{
		bot:     b,
		chat:    chat,
		feedMap: make(map[string]string),
	}, nil
}

func (t *Telegram) Send(ctx context.Context, text string) error {
	return t.SendToChat(ctx, t.chat.Recipient(), text)
}

func (t *Telegram) SendToChat(ctx context.Context, chatID, text string) error {
	chat, err := parseRecipient(chatID)
	if err != nil {
		return err
	}
	opts := &telebot.SendOptions{
		ParseMode:             telebot.ModeHTML,
		DisableWebPagePreview: true,
	}
	for _, chunk := range splitMessage(text, maxMessageLen) {
		chunk := chunk
		err := retry.Do(ctx, 3, func() error {
			_, err := t.bot.Send(chat, chunk, opts)
			return err
		})
		if err != nil {
			return fmt.Errorf("telegram send to %s: %w", chatID, err)
		}
	}
	return nil
}

func (t *Telegram) SendToAdmin(ctx context.Context, adminID int64, text string) error {
	if adminID == 0 {
		return nil
	}
	return t.SendToChat(ctx, strconv.FormatInt(adminID, 10), text)
}

func (t *Telegram) GetChatTitle(chatID string) (string, error) {
	chat, err := parseRecipient(chatID)
	if err != nil {
		return "", err
	}
	c, err := t.bot.ChatByUsername(chat.Recipient())
	if err != nil {
		// Если по юзернейму не вышло, пробуем как ID
		// В telebot.v3 ChatByID принимает int64, а ChatByUsername принимает string.
		id, parseErr := strconv.ParseInt(chat.Recipient(), 10, 64)
		if parseErr == nil {
			c, err = t.bot.ChatByID(id)
		} else {
			c, err = t.bot.ChatByUsername(chat.Recipient())
		}
	}
	if err != nil {
		return "", err
	}
	return c.Title, nil
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
	onFetch func(chatID string), onDigest func(),
	onStats, onLatest func() string,
	onDigestCount func() int,
	onFeeds func() ([]storage.Feed, error),
	onToggleFeed func(string) error,
) {
	allowed := func(c telebot.Context) bool {
		sender := c.Sender()
		if sender == nil {
			return false
		}
		return adminID == 0 || sender.ID == adminID
	}

	t.setupCommands(allowed, onFetch, onDigest, onStats, onLatest, onDigestCount, onFeeds)
	t.setupCallbacks(allowed, onFetch, onDigest, onDigestCount, onFeeds, onToggleFeed)

	_ = t.bot.SetCommands([]telebot.Command{
		{Text: "digest", Description: "Сформировать и отправить дайджест в канал"},
		{Text: "fetch", Description: "Собрать свежие статьи"},
		{Text: "feeds", Description: "Управление источниками RSS"},
		{Text: "latest", Description: "Показать последние материалы (в бота)"},
		{Text: "stats", Description: "Статистика сбора"},
	})

	go t.bot.Start()
	<-ctx.Done()
	t.bot.Stop()
}

func (t *Telegram) setupCommands(
	allowed func(telebot.Context) bool,
	onFetch func(chatID string), onDigest func(),
	onStats, onLatest func() string,
	onDigestCount func() int,
	onFeeds func() ([]storage.Feed, error),
) {
	btnFetch := telebot.Btn{Unique: "cb_fetch", Text: "🔄 Собрать статьи"}
	btnDigest := telebot.Btn{Unique: "cb_digest", Text: "📨 Запустить дайджест"}

	t.bot.Handle("/fetch", func(c telebot.Context) error {
		if !allowed(c) {
			return nil
		}
		go onFetch(recipientFromContext(c, t.chat.Recipient()))
		return c.Reply("⏳ Сбор статей запущен...", &telebot.SendOptions{ParseMode: telebot.ModeHTML})
	})
	t.bot.Handle("/digest", func(c telebot.Context) error {
		if !allowed(c) {
			return nil
		}
		n := onDigestCount()
		if n == 0 {
			return c.Reply("📭 Новых статей за сегодня нет", &telebot.SendOptions{ParseMode: telebot.ModeHTML})
		}
		go onDigest()
		return c.Reply(fmt.Sprintf("⏳ Формирую дайджест из %d статей за сегодня...", n),
			&telebot.SendOptions{ParseMode: telebot.ModeHTML})
	})
	t.bot.Handle("/stats", func(c telebot.Context) error {
		if !allowed(c) {
			return nil
		}
		_ = c.Notify(telebot.Typing)
		kb := &telebot.ReplyMarkup{}
		kb.Inline(kb.Row(btnFetch, btnDigest))
		return c.Reply(onStats(), &telebot.SendOptions{
			ParseMode:             telebot.ModeHTML,
			DisableWebPagePreview: true,
			ReplyMarkup:           kb,
		})
	})
	t.bot.Handle("/latest", func(c telebot.Context) error {
		if !allowed(c) {
			return nil
		}
		_ = c.Notify(telebot.Typing)
		kb := &telebot.ReplyMarkup{}
		kb.Inline(kb.Row(btnFetch, btnDigest))
		return c.Reply(onLatest(), &telebot.SendOptions{
			ParseMode:             telebot.ModeHTML,
			DisableWebPagePreview: true,
			ReplyMarkup:           kb,
		})
	})
	t.bot.Handle("/feeds", func(c telebot.Context) error {
		if !allowed(c) {
			return nil
		}
		_ = c.Notify(telebot.Typing)
		feeds, err := onFeeds()
		if err != nil {
			return c.Reply("❌ Ошибка получения списка фидов")
		}
		if len(feeds) == 0 {
			return c.Reply("📋 <b>Управление фидами:</b>\n\nСписок источников пуст. Добавьте их через веб-панель.",
				&telebot.SendOptions{ParseMode: telebot.ModeHTML})
		}
		return c.Reply("📋 <b>Управление фидами:</b>\nНажмите на кнопку, чтобы включить/выключить фид.",
			&telebot.SendOptions{
				ParseMode:   telebot.ModeHTML,
				ReplyMarkup: t.makeFeedsKeyboard(feeds),
			})
	})
}

func (t *Telegram) makeFeedsKeyboard(feeds []storage.Feed) *telebot.ReplyMarkup {
	keyboard := &telebot.ReplyMarkup{}
	var rows []telebot.Row

	t.mu.Lock()
	defer t.mu.Unlock()

	for _, f := range feeds {
		status := "✅"
		if !f.Active {
			status = "❌"
		}
		label := f.Title
		if label == "" {
			label = sourceLabel(f.URL)
		}

		// Telegram API ограничивает размер CallbackData до 64 байт.
		// Слишком длинные URL ломают клавиатуру. Используем хэш.
		h := sha256.Sum256([]byte(f.URL))
		hash := hex.EncodeToString(h[:])[:8]
		t.feedMap[hash] = f.URL

		btn := keyboard.Data(fmt.Sprintf("%s %s", status, label), "cb_toggle_feed", hash)
		rows = append(rows, keyboard.Row(btn))
	}
	keyboard.Inline(rows...)
	return keyboard
}

func (t *Telegram) setupCallbacks(
	allowed func(telebot.Context) bool,
	onFetch func(chatID string), onDigest func(),
	onDigestCount func() int,
	onFeeds func() ([]storage.Feed, error),
	onToggleFeed func(string) error,
) {
	btnFetch := telebot.Btn{Unique: "cb_fetch"}
	btnDigest := telebot.Btn{Unique: "cb_digest"}
	btnToggle := telebot.Btn{Unique: "cb_toggle_feed"}

	t.bot.Handle(&btnFetch, func(c telebot.Context) error {
		if !allowed(c) {
			return c.Respond()
		}
		go onFetch(recipientFromContext(c, t.chat.Recipient()))
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
	t.bot.Handle(&btnToggle, func(c telebot.Context) error {
		if !allowed(c) {
			return c.Respond()
		}

		hash := c.Data()
		t.mu.Lock()
		url, ok := t.feedMap[hash]
		t.mu.Unlock()

		if !ok {
			return c.Respond(&telebot.CallbackResponse{Text: "❌ Фид не найден, обновите список (/feeds)"})
		}

		if err := onToggleFeed(url); err != nil {
			return c.Respond(&telebot.CallbackResponse{Text: "❌ Ошибка"})
		}
		feeds, _ := onFeeds()
		return c.Edit("📋 <b>Управление фидами:</b>\nНажмите на кнопку, чтобы включить/выключить фид.",
			t.makeFeedsKeyboard(feeds), telebot.ModeHTML)
	})
}

// sourceLabel извлекает метку из URL.
func sourceLabel(feedURL string) string {
	if feedURL == "" {
		return "@unknown"
	}
	parts := strings.Split(strings.Trim(feedURL, "/"), "/")
	name := parts[len(parts)-1]
	if name == "" {
		return feedURL
	}
	return "@" + name
}

func recipientFromContext(c telebot.Context, fallback string) string {
	chat := c.Chat()
	if chat == nil {
		return fallback
	}
	return chat.Recipient()
}

// splitMessage делит текст на части.
func splitMessage(text string, maxLen int) []string {
	if len([]rune(text)) <= maxLen {
		return []string{text}
	}

	var chunks []string
	var carry []htmlTag
	remaining := text

	for len(remaining) > 0 {
		prefix := renderOpenTags(carry)
		budget := maxLen - len([]rune(prefix))
		if budget <= 0 {
			budget = maxLen
		}

		cut, nextCarry := splitPoint(remaining, budget)
		if cut <= 0 {
			cut = len(remaining)
			nextCarry = nil
		}

		chunkBody := remaining[:cut]
		chunk := prefix + chunkBody + renderCloseTags(nextCarry)
		chunks = append(chunks, chunk)
		remaining = remaining[cut:]
		carry = nextCarry
	}

	return chunks
}

type htmlTag struct {
	name string
	open string
}

func splitPoint(text string, maxLen int) (int, []htmlTag) {
	if len([]rune(text)) <= maxLen {
		return len(text), nil
	}

	type candidate struct {
		idx   int
		stack []htmlTag
		score int
	}

	var stack []htmlTag
	var best candidate
	var lastSafe candidate
	runes := 0

	for i := 0; i < len(text); {
		if text[i] == '<' {
			end := strings.IndexByte(text[i:], '>')
			if end == -1 {
				break
			}
			end += i + 1
			token := text[i:end]
			runes += len([]rune(token))
			stack = applyHTMLTag(stack, token)
			if runes+closingRunes(stack) > maxLen {
				break
			}
			lastSafe = candidate{idx: end, stack: cloneTags(stack)}
			i = end
			continue
		}

		r, size := rune(text[i]), 1
		if r >= utf8.RuneSelf {
			r, size = utf8.DecodeRuneInString(text[i:])
		}
		runes++
		if runes+closingRunes(stack) > maxLen {
			break
		}

		idx := i + size
		lastSafe = candidate{idx: idx, stack: cloneTags(stack)}
		switch r {
		case '\n':
			best = candidate{idx: idx, stack: cloneTags(stack), score: 3}
		case ' ', '\t':
			if best.score <= 2 {
				best = candidate{idx: idx, stack: cloneTags(stack), score: 2}
			}
		default:
			if best.score <= 1 {
				best = candidate{idx: idx, stack: cloneTags(stack), score: 1}
			}
		}
		i = idx
	}

	if best.idx > 0 {
		return best.idx, best.stack
	}
	if lastSafe.idx > 0 {
		return lastSafe.idx, lastSafe.stack
	}
	return len(text), nil
}

func applyHTMLTag(stack []htmlTag, token string) []htmlTag {
	token = strings.TrimSpace(token)
	if len(token) < 3 || token[0] != '<' || token[len(token)-1] != '>' {
		return stack
	}
	if strings.HasPrefix(token, "<!") || strings.HasPrefix(token, "<?") {
		return stack
	}
	if strings.HasPrefix(token, "</") {
		name := strings.ToLower(strings.TrimSpace(token[2 : len(token)-1]))
		for i := len(stack) - 1; i >= 0; i-- {
			if stack[i].name == name {
				return stack[:i]
			}
		}
		return stack
	}
	if strings.HasSuffix(token, "/>") {
		return stack
	}

	body := strings.TrimSpace(token[1 : len(token)-1])
	if body == "" {
		return stack
	}
	name := body
	if sp := strings.IndexAny(body, " \t\r\n"); sp >= 0 {
		name = body[:sp]
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return stack
	}
	return append(stack, htmlTag{name: name, open: token})
}

func cloneTags(tags []htmlTag) []htmlTag {
	if len(tags) == 0 {
		return nil
	}
	out := make([]htmlTag, len(tags))
	copy(out, tags)
	return out
}

func renderOpenTags(tags []htmlTag) string {
	var sb strings.Builder
	for _, tag := range tags {
		sb.WriteString(tag.open)
	}
	return sb.String()
}

func renderCloseTags(tags []htmlTag) string {
	var sb strings.Builder
	for i := len(tags) - 1; i >= 0; i-- {
		sb.WriteString("</")
		sb.WriteString(tags[i].name)
		sb.WriteString(">")
	}
	return sb.String()
}

func closingRunes(tags []htmlTag) int {
	total := 0
	for _, tag := range tags {
		total += len(tag.name) + 3
	}
	return total
}
