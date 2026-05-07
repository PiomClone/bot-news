package summarizer

import (
	"context"
	"fmt"
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

	date := time.Now().Format("2 January 2006")
	prompt := fmt.Sprintf(
		"Ты — редактор новостного Telegram-дайджеста. Сделай краткую и информативную выжимку материалов за %s "+
			"на основе предоставленных заголовков и описаний.\n\n"+
			"Правила:\n"+
			"1. Сгруппируй статьи по 2-4 смысловым темам. Для каждой темы придумай заголовок с 2-3 подходящими эмодзи. "+
			"Заголовок выдели жирным (тег <b>...</b>).\n"+
			"2. Под каждой темой — краткие пункты «- » (дефис + пробел). "+
			"Один пункт может объединять несколько похожих новостей.\n"+

			"3. Каждый пункт начинай со СТРОГО закрепленного за каналом эмодзи и его названия (см. список ниже).\n"+
			"4. В каждом пункте ОБЯЗАТЕЛЬНО сделай гиперссылку на ключевое слово-действие или событие: "+
			"<a href=\"url\">слово</a>. Не вставляй голые ссылки.\n"+
			"5. Если новость не содержит важной информации (реклама, анонсы, самопиар) — пропусти её.\n"+
			"6. Используй только русский язык. Никаких вступлений и заключений — только чистый дайджест.\n\n"+
			"Список каналов и их эмодзи:\n%s\n"+
			"Формат вывода:\n"+
			"<b>Дайджест за %s</b>\n\n"+
			"<b>эмодзи ТЕМА</b>\n"+
			"- [эмодзи канала] @channel: Текст новости с <a href=\"url\">ссылкой</a>.\n"+
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
	if h.LimitRequests > 0 {
		g.limits = fmt.Sprintf("🤖 <b>AI Лимиты:</b> запросов %d/%d, токенов %d/%d",
			h.RemainingRequests, h.LimitRequests, h.RemainingTokens, h.LimitTokens)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("groq: пустой ответ")
	}

	return resp.Choices[0].Message.Content, nil
}
