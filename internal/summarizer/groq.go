package summarizer

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"regexp"
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

	// Формируем список сырых новостей
	var sb strings.Builder
	for i, a := range articles {
		source := a.FeedTitle
		if source == "" {
			source = sourceLabel(a.FeedURL)
		}

		desc := a.Description
		if len([]rune(desc)) > 300 {
			desc = string([]rune(desc)[:300]) + "..."
		}
		fmt.Fprintf(&sb, "%d. Источник: %s\n   Заголовок: %s\n   Описание: %s\n   Ссылка: %s\n",
			i+1, source, a.Title, desc, a.Link)
	}

	loc, _ := time.LoadLocation("Europe/Moscow")
	if loc == nil {
		loc = time.UTC
	}
	prompt := fmt.Sprintf(
		"Ты — главный редактор новостного издания. Твоя задача: превратить список сырых новостей за последние 8 часов в лаконичную, элитную новостную выжимку для Telegram.\n\n"+
			"Критически важные правила:\n"+
			"1. СИНТЕЗ: Если несколько источников пишут об одной и той же новости, объедини их в один качественный пункт.\n"+
			"2. ГРУППИРОВКА: Объедини новости по 3-5 глобальным тематическим категориям. Для каждой категории придумай заголовок с 2-3 релевантными эмодзи в начале (например: 🇷🇺⚔️🇺🇦, 🚢🌊🇨🇾, 🌍🤝🗣️, 🤖💻📱). Заголовок категории выдели жирным (тег <b>...</b>).\n"+
			"3. ФОРМАТ ПУНКТОВ:\n"+
			"   - Каждый пункт начинается с «- » (дефис и пробел).\n"+
			"   - НЕ ставь эмодзи источника, название канала или '@channel:' в начале пункта. Начинай сразу с текста (например: «Песков заявил...», «Ozon подвергся...»).\n"+
			"   - Каждый пункт — ровно одно емкое, информативное предложение. НЕ используй жирный текст (<b> или **) внутри текста пункта.\n"+
			"4. ОРГАНИЧЕСКИЕ ВСТРОЕННЫЕ ССЫЛКИ:\n"+
			"   - В КАЖДОМ пункте ОБЯЗАТЕЛЬНО сделай гиперссылку <a href=\"url\">слово</a>, встроив её ОРГАНИЧНО внутрь предложения на сказуемое/глагол или ключевое слово (например: «Песков <a href=\"url\">заявил</a> о...», «Ozon <a href=\"url\">подвергся</a> атаке...»).\n"+
			"   - КАТЕГОРИЧЕСКИ ЗАПРЕЩЕНО ставить ссылки в конце предложения вида «[читайте]», «[подробно]», «(источник)».\n"+
			"   - Если источников несколько, сделай несколько ссылок на разные ключевые слова в предложении.\n"+
			"5. МУСОР: Игнорируй рекламу, вакансии, анонсы вебинаров и малозначимые события.\n\n"+
			"Формат вывода (строго соблюдай эту структуру):\n"+
			"<b>Выжимка за 8 часов:</b>\n\n"+
			"<b>[2-3 эмодзи] НАЗВАНИЕ КАТЕГОРИИ</b>\n"+
			"- Субъект <a href=\"url\">глагол</a> продолжение предложения.\n"+
			"- ...\n\n"+
			"Материалы для обработки:\n%s",
		sb.String(),
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

	htmlContent := convertMarkdownToHTML(resp.Choices[0].Message.Content)
	return ensureBulletLinks(htmlContent, articles), nil
}

var (
	reMarkdownLink = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^\s\)]+)\)`)
	reMarkdownBold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
)

func convertMarkdownToHTML(text string) string {
	text = reMarkdownLink.ReplaceAllString(text, `<a href="$2">$1</a>`)
	text = reMarkdownBold.ReplaceAllString(text, `<b>$1</b>`)
	return text
}

func ensureBulletLinks(text string, articles []storage.Article) string {
	lines := strings.Split(text, "\n")
	usedLinks := make(map[string]bool)

	for i := 0; i < len(lines); i++ {
		if !isBulletStart(lines[i]) {
			continue
		}

		end := i + 1
		for end < len(lines) && !startsNewBlock(lines[end]) {
			end++
		}

		block := strings.Join(lines[i:end], "\n")
		if strings.Contains(block, "<a ") {
			markUsedLinks(block, usedLinks)
			i = end - 1
			continue
		}

		link := pickFallbackLink(block, articles, usedLinks)
		if link == "" {
			i = end - 1
			continue
		}
		usedLinks[link] = true
		lines[i] = embedLinkInBulletLine(lines[i], link)
		i = end - 1
	}

	return strings.Join(lines, "\n")
}

func embedLinkInBulletLine(line, link string) string {
	escapedLink := html.EscapeString(link)
	prefix := "- "
	trimmed := line
	hasBullet := false
	if strings.HasPrefix(line, "- ") {
		prefix = "- "
		trimmed = strings.TrimPrefix(line, "- ")
		hasBullet = true
	} else if strings.HasPrefix(line, "• ") {
		prefix = "• "
		trimmed = strings.TrimPrefix(line, "• ")
		hasBullet = true
	}

	if !hasBullet {
		return line + fmt.Sprintf(" (<a href=\"%s\">ссылка</a>)", escapedLink)
	}

	words := strings.Fields(trimmed)
	if len(words) == 0 {
		return line
	}

	targetIdx := 0
	if len(words) > 1 {
		targetIdx = 1
	}

	words[targetIdx] = fmt.Sprintf("<a href=\"%s\">%s</a>", escapedLink, words[targetIdx])
	return prefix + strings.Join(words, " ")
}

func isBulletStart(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "- ")
}

func startsNewBlock(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	if strings.HasPrefix(trimmed, "- ") {
		return true
	}
	return strings.HasPrefix(trimmed, "<b>")
}

func markUsedLinks(line string, used map[string]bool) {
	parts := strings.Split(line, "<a href=\"")
	for _, part := range parts[1:] {
		link, _, ok := strings.Cut(part, "\"")
		if ok && link != "" {
			used[link] = true
		}
	}
}

func pickFallbackLink(line string, articles []storage.Article, used map[string]bool) string {
	lowerLine := strings.ToLower(line)

	for _, article := range articles {
		if article.Link == "" || used[article.Link] {
			continue
		}
		source := article.FeedTitle
		if source == "" {
			source = sourceLabel(article.FeedURL)
		}
		if source != "" && strings.Contains(lowerLine, strings.ToLower(source)) {
			return article.Link
		}
	}

	for _, article := range articles {
		if article.Link != "" && !used[article.Link] {
			return article.Link
		}
	}

	return ""
}
