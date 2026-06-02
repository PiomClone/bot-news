package summarizer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"bot-news/internal/storage"
)

const groqBaseURL = "https://api.groq.com/openai/v1"

type chatClient interface {
	CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
}

// GroqSummarizer использует Groq API (OpenAI-совместимый) для AI-саммари.
type GroqSummarizer struct {
	client chatClient
	model  string
	limits string
}

func NewGroq(apiKey, model string) *GroqSummarizer {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = groqBaseURL
	return &GroqSummarizer{
		client: openai.NewClientWithConfig(cfg),
		model:  model,
	}
}

func (g *GroqSummarizer) GetLimits() string {
	return g.limits
}

func (g *GroqSummarizer) SetLimits(limits string) {
	g.limits = limits
}

func (g *GroqSummarizer) Summarize(ctx context.Context, articles []storage.Article) (string, error) {
	if len(articles) == 0 {
		return "", nil
	}

	// Формируем легенду эмодзи и список статей
	emojiMap := make(map[string]string)
	var sb strings.Builder
	for i, a := range articles {
		source := a.FeedTitle
		if source == "" {
			source = sourceLabel(a.FeedURL)
		}
		emoji := GetEmoji(a.FeedURL)
		emojiMap[source] = emoji

		desc := a.Description
		if len([]rune(desc)) > 300 {
			desc = string([]rune(desc)[:300]) + "..."
		}
		fmt.Fprintf(&sb, "%d. [%s] Источник: %s\n   Заголовок: %s\n   Описание: %s\n   Ссылка: %s\n",
			i+1, emoji, source, a.Title, desc, a.Link)
	}

	var legendSB strings.Builder
	for name, emoji := range emojiMap {
		fmt.Fprintf(&legendSB, "- %s %s\n", emoji, name)
	}

	loc, _ := time.LoadLocation("Europe/Moscow")
	if loc == nil {
		loc = time.UTC
	}
	date := time.Now().In(loc).Format("2 January 2006")
	prompt := fmt.Sprintf(
		"Ты — главный редактор ИТ-издания. Твоя задача: превратить список сырых новостей за %s в элитный аналитический дайджест для Telegram.\n\n"+
			"Критически важные правила группировки:\n"+
			"1. СИНТЕЗ: Если несколько источников пишут об одном и том же (например, одна и та же новость на Хабре и в Telegram), НЕ делай два пункта. Слей их в один качественный абзац, указав ссылки на все источники.\n"+
			"2. ГРУППИРОВКА: Объедини статьи по 3-4 глобальным темам. Придумай для каждой темы яркий заголовок с 2-3 эмодзи. Заголовок выдели жирным (тег <b>...</b>).\n"+
			"3. ГЛАВНОЕ: Если есть одна супер-важная новость, вынеси её в самое начало под заголовком <b>🔥 ГЛАВНОЕ СОБЫТИЕ</b>.\n"+
			"4. ФОРМАТ ПУНКТОВ: Под каждой темой пиши краткие и емкие пункты «- ». Каждый пункт должен быть законченной мыслью.\n"+
			"5. ССЫЛКИ: В каждом пункте ОБЯЗАТЕЛЬНО делай гиперссылки на ключевые слова: <a href=\"url\">слово</a>. Если источников несколько, дай ссылки на каждый: (<a href=\"url1\">ист1</a>, <a href=\"url2\">ист2</a>).\n"+
			"6. ЭМОДЗИ: В начале каждого пункта ставь эмодзи канала-источника (см. список ниже).\n"+
			"7. МУСОР: Игнорируй рекламу, вакансии, анонсы вебинаров и малозначимые события.\n\n"+
			"Список каналов и их эмодзи:\n%s\n"+
			"Формат вывода:\n"+
			"<b>Дайджест за %s</b>\n\n"+
			"<b>эмодзи НАЗВАНИЕ ТЕМЫ</b>\n"+
			"- [эмодзи] @channel: Суть новости с <a href=\"url\">ссылкой</a>.\n"+
			"- ...\n\n"+
			"Материалы для обработки:\n%s",
		date, legendSB.String(), date, sb.String(),
	)

	resp, err := g.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: g.model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("groq: %w", err)
	}

	// Сохраняем лимиты из заголовков
	h := resp.GetRateLimitHeaders()
	now := time.Now().In(loc)
	if h.LimitRequests > 0 || h.LimitTokens > 0 || h.RemainingRequests > 0 {
		g.limits = fmt.Sprintf("🤖 <b>AI Лимиты:</b> запросов %d/%d, токенов %d/%d (обновлено %s)",
			h.RemainingRequests, h.LimitRequests, h.RemainingTokens, h.LimitTokens, now.Format("15:04:05"))
	} else {
		// Если заголовки не распознаны, но запрос прошел — ставим отметку активности
		g.limits = fmt.Sprintf("🤖 <b>AI Активен:</b> последний запрос успешно выполнен в %s",
			now.Format("15:04:05"))
	}
	slog.Debug("обновлены лимиты AI", "limits", g.limits)

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("groq: пустой ответ")
	}

	return resp.Choices[0].Message.Content, nil
}
