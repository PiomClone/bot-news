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
		fmt.Fprintf(&sb, "%d. %s\n   %s\n", i+1, a.Title, a.Link)
	}

	date := time.Now().Format("2 January 2006")
	prompt := fmt.Sprintf(
		"Ты — редактор новостного дайджеста. Сделай краткий тематический обзор следующих материалов за %s. "+
			"Сгруппируй по темам, выдели самое важное в 2-3 предложениях на каждую тему. "+
			"В конце добавь исходные ссылки списком. Отвечай на русском языке.\n\n"+
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
