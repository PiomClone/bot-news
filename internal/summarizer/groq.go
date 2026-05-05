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

// GroqSummarizer использует Groq API (OpenAI-совместимый) для AI-саммари.
type GroqSummarizer struct {
	client *openai.Client
	model  string
}

func NewGroq(apiKey, model string) *GroqSummarizer {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = groqBaseURL
	return &GroqSummarizer{
		client: openai.NewClientWithConfig(cfg),
		model:  model,
	}
}

func (g *GroqSummarizer) Summarize(ctx context.Context, articles []storage.Article) (string, error) {
	if len(articles) == 0 {
		return "", nil
	}

	// Формируем список статей для промпта
	var sb strings.Builder
	for i, a := range articles {
		fmt.Fprintf(&sb, "%d. Источник: %s\n   Заголовок: %s\n   Ссылка: %s\n", i+1, sourceLabel(a.FeedURL), a.Title, a.Link)
	}

	date := time.Now().Format("2 January 2006")
	prompt := fmt.Sprintf(
		"Ты — редактор новостного Telegram-дайджеста. Сделай выжимку материалов за %s.\n\n"+
			"Правила:\n"+
			"1. Сгруппируй по 2-4 смысловым темам. Для каждой темы — жирный заголовок с 2-3 подходящими эмодзи.\n"+
			"2. Под каждой темой — пункты «- » (дефис + пробел). Каждый пункт — одно предложение.\n"+
			"3. Каждый пункт начинай с источника в формате «@channel: ».\n"+
			"4. В каждом пункте ОБЯЗАТЕЛЬНО сделай гиперссылку на ключевое слово-действие (глагол или существо события): [слово](url). Не вставляй голую ссылку.\n"+
			"5. Пропускай рекламу, самопиар и неинформативные посты.\n"+
			"6. Только русский язык. Никаких пояснений от себя — только дайджест.\n\n"+
			"Формат:\n"+
			"*Выжимка за [дата]:*\n\n"+
			"*эмодзи Тема*\n"+
			"- @channel: Субъект [глагол](url) подробности.\n"+
			"- ...\n\n"+
			"Материалы:\n%s",
		date, sb.String(),
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

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("groq: пустой ответ")
	}

	header := fmt.Sprintf("*Дайджест за %s*\n\n", date)
	return header + resp.Choices[0].Message.Content, nil
}
