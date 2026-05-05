package summarizer_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot-news/internal/storage"
	"bot-news/internal/summarizer"
)

const unexpectedErr = "неожиданная ошибка: %v"

func makeArticle(title, link, desc string) storage.Article {
	return storage.Article{Title: title, Link: link, Description: desc, FetchedAt: time.Now()}
}

func TestSimpleSummarizerEmpty(t *testing.T) {
	s := summarizer.NewSimple()
	text, err := s.Summarize(context.Background(), nil)
	if err != nil {
		t.Fatalf(unexpectedErr, err)
	}
	if text != "" {
		t.Fatalf("ожидали пустую строку для пустого списка, получили %q", text)
	}
}

func TestSimpleSummarizerContainsTitlesAndLinks(t *testing.T) {
	s := summarizer.NewSimple()
	articles := []storage.Article{
		makeArticle("Заголовок первый", "https://example.com/1", "Описание первого"),
		makeArticle("Заголовок второй", "https://example.com/2", ""),
	}

	text, err := s.Summarize(context.Background(), articles)
	if err != nil {
		t.Fatalf(unexpectedErr, err)
	}
	for _, want := range []string{"Заголовок первый", "Заголовок второй", "https://example.com/1", "Описание первого"} {
		if !strings.Contains(text, want) {
			t.Errorf("текст не содержит %q:\n%s", want, text)
		}
	}
}

func TestSimpleSummarizerWithoutLink(t *testing.T) {
	s := summarizer.NewSimple()
	text, err := s.Summarize(context.Background(), []storage.Article{
		makeArticle("Статья без ссылки", "", ""),
	})
	if err != nil {
		t.Fatalf(unexpectedErr, err)
	}
	if !strings.Contains(text, "Статья без ссылки") {
		t.Errorf("текст не содержит заголовок: %s", text)
	}
	if strings.Contains(text, "🔗") {
		t.Errorf("не должно быть 🔗 без ссылки: %s", text)
	}
}

func TestSimpleSummarizerDescriptionTruncated(t *testing.T) {
	s := summarizer.NewSimple()
	longDesc := strings.Repeat("а", 300)
	text, err := s.Summarize(context.Background(), []storage.Article{
		makeArticle("Заголовок", "https://example.com", longDesc),
	})
	if err != nil {
		t.Fatalf(unexpectedErr, err)
	}
	// Описание должно быть обрезано до 200 + "..."
	if strings.Contains(text, longDesc) {
		t.Error("длинное описание не было обрезано")
	}
	if !strings.Contains(text, "...") {
		t.Error("обрезанное описание должно заканчиваться на '...'")
	}
}

func TestSimpleSummarizerHeader(t *testing.T) {
	s := summarizer.NewSimple()
	text, err := s.Summarize(context.Background(), []storage.Article{
		makeArticle("А", "https://a.com", ""),
	})
	if err != nil {
		t.Fatalf(unexpectedErr, err)
	}
	if !strings.Contains(text, "Дайджест") {
		t.Errorf("нет заголовка дайджеста: %s", text)
	}
}
